package kube

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTar(t *testing.T, entries ...tar.Header) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "data"
			h.Size = int64(len(body))
		}
		h.Mode = 0o644
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	return tar.NewReader(&buf)
}

func unpack(t *testing.T, tr *tar.Reader, dir string) (int, error) {
	left, err := unpackLeft(t, tr, dir)
	return left.Special, err
}

func unpackLeft(t *testing.T, tr *tar.Reader, dir string) (LeftOut, error) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	return unpackTar(tr, first, root)
}

// A tree goes into a tar and comes back out the same: files, modes,
// nested directories. Links go in, and are left out coming back.
func TestPackUnpack(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "deep", "b.txt"), []byte("b"), 0o600)
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Skip("no symlinks here")
	}

	var buf bytes.Buffer
	if err := packTar(&buf, src); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	skipped, err := unpack(t, tar.NewReader(&buf), dst)
	if err != nil || skipped != 1 {
		t.Fatalf("skipped %d, %v", skipped, err)
	}
	for name, want := range map[string]string{"a.txt": "a", "sub/run.sh": "#!/bin/sh\n", "sub/deep/b.txt": "b"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	if info, _ := os.Stat(filepath.Join(dst, "sub", "run.sh")); info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode %v", info.Mode())
	}
	if _, err := os.Lstat(filepath.Join(dst, "link")); err == nil {
		t.Error("the link should have been left out")
	}
}

// One file packs as one entry named after it.
func TestPackFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(src, []byte("n"), 0o644)
	var buf bytes.Buffer
	if err := packTar(&buf, src); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	h, err := tr.Next()
	if err != nil || h.Name != "notes.md" || h.Typeflag != tar.TypeReg {
		t.Fatalf("%+v, %v", h, err)
	}
	if _, err := tr.Next(); err == nil {
		t.Error("want one entry")
	}
}

// What a session sends isn't trusted: names that climb out of the
// destination stop the copy, and nothing lands outside it.
func TestUnpackRefusesPathsOutside(t *testing.T) {
	for _, name := range []string{"../evil", "sub/../../evil", "/etc/evil", "./../evil"} {
		parent := t.TempDir()
		dst := filepath.Join(parent, "dst")
		os.Mkdir(dst, 0o755)
		_, err := unpack(t, writeTar(t, tar.Header{Name: "./", Typeflag: tar.TypeDir}, tar.Header{Name: name, Typeflag: tar.TypeReg}), dst)
		if err == nil || !strings.Contains(err.Error(), "points outside the copy") {
			t.Errorf("%s: err = %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
			t.Errorf("%s: written outside the destination", name)
		}
	}
}

// A link in the archive is never made, so a later entry can't be written
// through it; and one that's already in the destination isn't followed out.
func TestUnpackDoesNotFollowLinks(t *testing.T) {
	parent := t.TempDir()
	dst := filepath.Join(parent, "dst")
	outside := filepath.Join(parent, "outside")
	os.Mkdir(dst, 0o755)
	os.Mkdir(outside, 0o755)

	skipped, err := unpack(t, writeTar(t,
		tar.Header{Name: "./", Typeflag: tar.TypeDir},
		tar.Header{Name: "./out", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
		tar.Header{Name: "./out/f", Typeflag: tar.TypeReg},
	), dst)
	if err != nil || skipped != 1 {
		t.Fatalf("skipped %d, %v", skipped, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Error("written through a link from the archive")
	}

	if err := os.Symlink(outside, filepath.Join(dst, "there")); err != nil {
		t.Skip("no symlinks here")
	}
	_, err = unpack(t, writeTar(t, tar.Header{Name: "./", Typeflag: tar.TypeDir}, tar.Header{Name: "./there/g", Typeflag: tar.TypeReg}), dst)
	if err == nil {
		t.Error("want an error for a path through a link that leaves the destination")
	}
	if _, err := os.Stat(filepath.Join(outside, "g")); err == nil {
		t.Error("written through a link in the destination")
	}
}

func TestRemotePath(t *testing.T) {
	for in, want := range map[string]string{"": "/work", "repo": "/work/repo", "repo/": "/work/repo", "/tmp/x": "/tmp/x", "../etc": "/etc"} {
		if got := remotePath(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// Git's own files stay in the session: a hook or a config that came out
// would run on the user's machine. Whatever the file system would take
// for .git counts.
func TestUnpackLeavesOutGit(t *testing.T) {
	dst := t.TempDir()
	left, err := unpackLeft(t, writeTar(t,
		tar.Header{Name: "./", Typeflag: tar.TypeDir},
		tar.Header{Name: "./.git/", Typeflag: tar.TypeDir},
		tar.Header{Name: "./.git/hooks/pre-commit", Typeflag: tar.TypeReg, Mode: 0o755},
		tar.Header{Name: "./.git/config", Typeflag: tar.TypeReg},
		tar.Header{Name: "./sub/.GIT/config", Typeflag: tar.TypeReg},
		tar.Header{Name: "./sub/.git", Typeflag: tar.TypeReg},
		tar.Header{Name: "./.gitignore", Typeflag: tar.TypeReg},
		tar.Header{Name: "./src/git.go", Typeflag: tar.TypeReg},
	), dst)
	if err != nil || left.Git != 4 || left.Special != 0 {
		t.Fatalf("%+v, %v", left, err)
	}
	for name, want := range map[string]bool{".git": false, "sub/.GIT": false, "sub/.git": false, ".gitignore": true, "src/git.go": true} {
		if _, err := os.Lstat(filepath.Join(dst, name)); (err == nil) != want {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Each name on its own, through an unpack: as a file, and as a
	// directory with a file in it.
	for name, git := range map[string]bool{
		".git": true, ".GIT": true, ".git.": true, ".git ": true, "GIT~1": true, ".g\u200cit": true,
		".git::$DATA": true, ".git::$INDEX_ALLOCATION": true, ".git:x": true, ".Git. .::$DATA": true,
		".gitignore": false, "git": false, ".github": false, "a.git": false, ".gitmodules": false,
	} {
		if isDotGit(name) != git {
			t.Errorf("isDotGit(%q) = %v", name, !git)
		}
		dst := t.TempDir()
		left, err := unpackLeft(t, writeTar(t,
			tar.Header{Name: "./", Typeflag: tar.TypeDir},
			tar.Header{Name: "./a/" + name, Typeflag: tar.TypeReg},
			tar.Header{Name: "./b/" + name + "/", Typeflag: tar.TypeDir},
			tar.Header{Name: "./b/" + name + "/config", Typeflag: tar.TypeReg},
		), dst)
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		wrote := 0
		filepath.WalkDir(dst, func(p string, d os.DirEntry, _ error) error {
			if d != nil && !d.IsDir() {
				wrote++
			}
			return nil
		})
		if want := map[bool]int{true: 0, false: 2}[git]; wrote != want || left.Git != 2-want {
			t.Errorf("%q: wrote %d files, left out %d", name, wrote, left.Git)
		}
	}
	if !inDotGit("/work/repo/.git/hooks") || !inDotGit(`repo\.git`) || inDotGit("/work/repo/src") {
		t.Error("inDotGit")
	}
	// Asking for them by name is refused before anything is run.
	c := sandboxClient()
	for _, src := range []string{"repo/.git/config", "repo/.git", "repo/.git::$DATA", ".GIT"} {
		if _, _, err := c.CopyFromSession(context.Background(), "alice", "work", src, dst); !errors.Is(err, ErrGitFiles) {
			t.Errorf("cp of %s: %v", src, err)
		}
	}
}
