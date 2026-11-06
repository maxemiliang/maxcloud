package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sourceDir is the app's source folder, where its image is built from and
// mcloud dev runs it: source: in maxcloud.yaml (relative to the file, e.g.
// worker when the repo's root is something else), else the folder
// maxcloud.yaml is in.
func (p *project) sourceDir() (string, error) {
	return resolveSource(p.Dir, p.Source)
}

// resolveSource checks source (from maxcloud.yaml in root): a relative
// path to a directory inside root, with no "..".
func resolveSource(root, source string) (string, error) {
	if source == "" || source == "." {
		return root, nil
	}
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("source: %q in %s %s", source, ProjectFile, why)
	}
	if filepath.IsAbs(source) || strings.HasPrefix(source, "/") || strings.HasPrefix(source, `\`) {
		return bad("must be relative to the repo (like worker)")
	}
	if slices.Contains(strings.FieldsFunc(source, func(r rune) bool { return r == '/' || r == '\\' }), "..") {
		return bad("can't contain ..")
	}
	dir := filepath.Join(root, filepath.FromSlash(source))
	info, err := os.Stat(dir)
	if err != nil {
		return bad("isn't a directory here")
	}
	if !info.IsDir() {
		return bad("is a file, not a directory")
	}
	// A symlink out of the repo would build something else than the repo.
	realRoot, err1 := filepath.EvalSymlinks(root)
	realDir, err2 := filepath.EvalSymlinks(dir)
	if err1 == nil && err2 == nil {
		if rel, err := filepath.Rel(realRoot, realDir); err != nil || !filepath.IsLocal(rel) && rel != "." {
			return bad("leads out of the repo")
		}
	}
	return dir, nil
}
