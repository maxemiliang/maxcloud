package cli

import (
	"os"
	"path/filepath"
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
