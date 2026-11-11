package kube

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// Copying between this machine and a session goes as a tar stream through
// an exec of tar in the session, like kubectl cp. A session has no
// credentials of the user's, so this is how their own files get in and
// out.

// packTar writes src to w as a tar: a directory's tree with names relative
// to it, or a single file named after it. Symlinks go as symlinks; sockets,
// devices and the like are left out.
func packTar(w io.Writer, src string) error {
	tw := tar.NewWriter(w)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	add := func(p, name string, info fs.FileInfo) error {
		link := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case !info.IsDir() && !info.Mode().IsRegular():
			return nil
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		// Whoever unpacks it owns it.
		h.Name, h.Uid, h.Gid, h.Uname, h.Gname = name, 0, 0, "", ""
		if info.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	if !info.IsDir() {
		if err := add(src, filepath.Base(src), info); err != nil {
			return err
		}
		return tw.Close()
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return add(p, filepath.ToSlash(rel), info)
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// tarEntryName is an entry's path inside the directory being unpacked, or
// an error for one that points outside it. "" is the directory itself.
func tarEntryName(name string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." {
		return "", nil
	}
	if !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("the session sent a file named %q, which points outside the copy: nothing more was written", name)
	}
	return filepath.FromSlash(clean), nil
}

// LeftOut counts what a copy out of a session didn't write.
type LeftOut struct {
	// Special: links and special files.
	Special int
	// Git: git's own files (.git). Hooks and configuration in them would
	// run on this machine the next time git is used there.
	Git int
}

// isDotGit: name is what some file system takes for ".git". The cases git
// itself guards against: other capitals; on NTFS (a Windows disk, or a
// share mounted here) trailing dots and spaces, the short name, and a
// stream of the file (.git::$DATA); characters macOS ignores.
func isDotGit(name string) bool {
	name = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
	name, _, _ = strings.Cut(name, ":")
	name = strings.TrimRight(name, ". ")
	return name == ".git" || name == "git~1"
}

// inDotGit: p (slash or backslash separated) is git's own directory, or
// in one.
func inDotGit(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if isDotGit(part) {
			return true
		}
	}
	return false
}

// unpackTar writes the tree in r into root and returns what it left out.
// What comes out of a session isn't trusted: only directories and regular
// files are written, links and special files are left out, git's own
// files are left out (commits come with SessionBundle), and root keeps
// every path inside the destination whatever the names say.
func unpackTar(tr *tar.Reader, first *tar.Header, root *os.Root) (left LeftOut, err error) {
	for h := first; ; {
		name, err := tarEntryName(h.Name)
		if err != nil {
			return left, err
		}
		switch {
		case name == "":
		case inDotGit(name):
			if h.Typeflag != tar.TypeDir {
				left.Git++
			}
		case h.Typeflag == tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return left, err
			}
		case h.Typeflag == tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return left, err
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o777)
			if err != nil {
				return left, err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return left, err
			}
		default:
			left.Special++
		}
		if h, err = tr.Next(); errors.Is(err, io.EOF) {
			return left, nil
		} else if err != nil {
			return left, err
		}
	}
}

// remotePath is p as the session sees it: relative paths are in its work
// directory.
func remotePath(p string) string {
	if p == "" {
		return SandboxWorkDir
	}
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(SandboxWorkDir, p)
}

// Like cp: into dst if it's a directory, else dst is the copy's name.
const (
	putDir  = `d="$1"; if [ -d "$d" ]; then d="$d/$2"; fi; mkdir -p "$d" && exec tar -xf - -C "$d"`
	putFile = `d="$1"; if [ -d "$d" ]; then exec tar -xf - -C "$d"; fi
mkdir -p "$(dirname "$d")" && t=$(mktemp -d "$(dirname "$d")/.mcloud-cp.XXXXXX") && tar -xf - -C "$t" && mv -f "$t/$2" "$d" && rmdir "$t"`
	getPath = `p="$1"; if [ -d "$p" ]; then exec tar -cf - -C "$p" .; elif [ -e "$p" ]; then exec tar -cf - -C "$(dirname "$p")" "$(basename "$p")"; else echo "no such file or directory: $p" >&2; exit 1; fi`
)

// execError adds what the command said on stderr to its exit error.
func execError(err error, stderr *bytes.Buffer) error {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("%s (%w)", oneLine(msg), err)
	}
	return err
}

// CopyToSession copies the local file or directory src into user's
// session at dst (relative to its work directory): into dst if that's a
// directory there, else as dst.
func (c *Client) CopyToSession(ctx context.Context, user, name, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	script := putFile
	if info.IsDir() {
		script = putDir
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(packTar(pw, abs)) }()
	var stderr bytes.Buffer
	err = c.SessionExec(ctx, user, name, []string{"sh", "-c", script, "sh", remotePath(dst), filepath.Base(abs)}, pr, io.Discard, &stderr)
	pr.Close()
	if err != nil {
		return execError(err, &stderr)
	}
	return nil
}

// ErrGitFiles: a copy out of a session was asked for git's own files.
var ErrGitFiles = errors.New("git's own files (.git) don't come out of a session with cp: hooks and configuration in them would run here. mcloud sandbox pull brings its commits")

// CopyFromSession copies src (relative to the work directory) out of
// user's session to the local dst: into dst if that's a directory, else as
// dst. It returns where the copy is, and what it left out.
func (c *Client) CopyFromSession(ctx context.Context, user, name, src, dst string) (target string, left LeftOut, err error) {
	src = remotePath(src)
	if inDotGit(src) {
		return "", left, ErrGitFiles
	}
	target = dst
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		target = filepath.Join(dst, path.Base(src))
	}
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	go func() {
		pw.CloseWithError(c.SessionExec(ctx, user, name, []string{"sh", "-c", getPath, "sh", src}, nil, pw, &stderr))
	}()
	defer pr.Close()
	tr := tar.NewReader(pr)
	first, err := tr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("the session sent nothing")
		}
		return "", left, execError(err, &stderr)
	}
	// A directory comes as "./" and what's in it; a file as one entry.
	if first.Typeflag != tar.TypeDir {
		if first.Typeflag != tar.TypeReg {
			return "", left, fmt.Errorf("%s is neither a file nor a directory", src)
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(first.Mode)&0o777)
		if err != nil {
			return "", left, err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", left, execError(err, &stderr)
		}
		_, err = io.Copy(io.Discard, pr)
		return target, left, err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", left, err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return "", left, err
	}
	defer root.Close()
	left, err = unpackTar(tr, first, root)
	if err != nil {
		return "", left, execError(err, &stderr)
	}
	return target, left, nil
}
