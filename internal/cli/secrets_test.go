package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A GitOps app that still has secrets.sops.yaml: set refuses before
// touching anything (Flux would put the file's values back), and says to
// migrate.
func TestSecretsSetRefusesWhileSOPSFileIsThere(t *testing.T) {
	repo := t.TempDir()
	k8s := filepath.Join(repo, "deploy", "k8s")
	os.MkdirAll(k8s, 0o755)
	os.WriteFile(filepath.Join(repo, ProjectFile), []byte("app: shop\n"), 0o644)
	os.WriteFile(filepath.Join(k8s, legacySecretsFile), []byte("stringData: {}\n"), 0o644)
	t.Chdir(repo)

	g := &globals{in: strings.NewReader("v\n"), out: io.Discard, errOut: io.Discard}
	cmd := secretsCmd(g)
	cmd.SetArgs([]string{"set", "API_KEY"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mcloud secrets migrate") {
		t.Fatalf("err = %v", err)
	}
}

func TestReservedSecret(t *testing.T) {
	for name, want := range map[string]bool{"DATABASE_URL": true, "AWS_REGION": true, "MAXCLOUD_APP": true, "STRIPE_KEY": false} {
		if reservedSecret(name) != want {
			t.Errorf("%s: %v", name, !want)
		}
	}
}
