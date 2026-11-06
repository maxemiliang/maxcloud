package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDotenv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("# c\nA=1\nexport B=\"two words\\n\"\nC='x=y'\n\n"), 0o600)
	m, err := readDotenv(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "two words\n" || m["C"] != "x=y" || len(m) != 3 {
		t.Errorf("got %q", m)
	}
	if m, err := readDotenv(filepath.Join(t.TempDir(), "none")); err != nil || m != nil {
		t.Errorf("missing file: %v %v", m, err)
	}
}

func TestDevCommandNuxt(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "node_modules", ".bin")
	app := filepath.Join(root, "packages", "web")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(bin, "nuxt"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(app, "package.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(app, "nuxt.config.ts"), []byte("export default {}"), 0o644)

	cmd, err := devCommand(t.Context(), app, "", 8081)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(bin, "nuxt"), "dev", "--port", "8081", "--host", "127.0.0.1"}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") || cmd.Dir != app {
		t.Errorf("command %v in %s", cmd.Args, cmd.Dir)
	}

	os.Remove(filepath.Join(bin, "nuxt"))
	if _, err := devCommand(t.Context(), app, "", 8081); err == nil || !strings.Contains(err.Error(), "nuxt isn't installed") {
		t.Errorf("without nuxt: %v", err)
	}
}
