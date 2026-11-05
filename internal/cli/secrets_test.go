package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The GitOps path end to end, with the real sops and a throwaway age key.
func TestSecretsGitOps(t *testing.T) {
	for _, bin := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("needs %s", bin)
		}
	}
	tmp := t.TempDir()
	keyFile := filepath.Join(tmp, "key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyFile).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", keyFile).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCLOUD_AGE_RECIPIENT", strings.TrimSpace(string(pub)))
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)

	repo := filepath.Join(tmp, "repo")
	k8s := filepath.Join(repo, "deploy", "k8s")
	os.MkdirAll(k8s, 0o755)
	os.WriteFile(filepath.Join(repo, ProjectFile), []byte("app: shop\n"), 0o644)
	os.WriteFile(filepath.Join(k8s, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - functions.yaml\n"), 0o644)
	t.Chdir(repo)

	run := func(stdin string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		g := &globals{in: strings.NewReader(stdin), out: &out, errOut: &out}
		c := secretsCmd(g)
		c.SetArgs(args)
		c.SetOut(&out)
		c.SetErr(&out)
		if err := c.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("secrets %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}

	run("sk_test_123\n", "set", "STRIPE_KEY")
	run("multi\nline", "set", "PEM")
	file := filepath.Join(k8s, secretsFile)
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), "sk_test_123") || !strings.Contains(string(raw), "ENC[") {
		t.Fatalf("not encrypted:\n%s", raw)
	}
	if k, _ := os.ReadFile(filepath.Join(k8s, "kustomization.yaml")); !strings.Contains(string(k), "- "+secretsFile) {
		t.Errorf("kustomization doesn't list %s:\n%s", secretsFile, k)
	}
	if got := strings.Fields(run("", "list")); !slices.Equal(got, []string{"PEM", "STRIPE_KEY"}) {
		t.Errorf("list = %v", got)
	}

	run("", "unset", "STRIPE_KEY")
	plain, err := exec.Command("sops", "decrypt", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	s, err := secretStringData(plain)
	if err != nil || len(s) != 1 || s["PEM"] != "multi\nline" {
		t.Errorf("after unset: %v, %v\n%s", s, err, plain)
	}
	if !strings.Contains(string(plain), "name: app-secrets") {
		t.Errorf("not the app-secrets Secret:\n%s", plain)
	}
}

func TestSecretNames(t *testing.T) {
	for _, bad := range []string{"lower", "1ABC", "A-B", "DATABASE_URL", "AWS_REGION", "MAXCLOUD_APP", "PORT"} {
		if secretNameRe.MatchString(bad) && !reservedSecret(bad) {
			t.Errorf("%s should be refused", bad)
		}
	}
	if !secretNameRe.MatchString("STRIPE_KEY") || reservedSecret("STRIPE_KEY") {
		t.Error("STRIPE_KEY should be fine")
	}
}

func TestAgeRecipientFromSopsYAML(t *testing.T) {
	y := "creation_rules:\n  - path_regex: '[^/]+\\.sops\\.ya?ml$'\n    encrypted_regex: '^(data|stringData)$'\n    age: 'age1s5mjgvz24gpuq46uhch3exsq4873vwa0wmyuvn745kmf4dv8y5dsclgqxa'\n"
	m := ageRecipientRe.FindStringSubmatch(y)
	if m == nil || m[1] != "age1s5mjgvz24gpuq46uhch3exsq4873vwa0wmyuvn745kmf4dv8y5dsclgqxa" {
		t.Errorf("got %v", m)
	}
}
