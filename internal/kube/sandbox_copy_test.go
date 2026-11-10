package kube

import (
	"archive/tar"
	"bytes"
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
