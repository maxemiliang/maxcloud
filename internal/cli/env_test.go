package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

func TestParseProjectEnv(t *testing.T) {
	env, err := parseProjectEnv([]byte(`app: shop
env:
  LOG_LEVEL: debug
  RATIO: 0.10
  MODE: 010
  ENABLED: true
  EMPTY: ""
  QUOTED: "null"
  MULTI: |
    a
    b
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"LOG_LEVEL": "debug", "RATIO": "0.10", "MODE": "010", "ENABLED": "true", "EMPTY": "", "QUOTED": "null", "MULTI": "a\nb\n"}
	if len(env) != len(want) {
		t.Errorf("env = %q", env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}

	if env, err := parseProjectEnv([]byte("app: shop\n")); env != nil || err != nil {
		t.Errorf("no env: %v, %v", env, err)
	}
	if env, err := parseProjectEnv([]byte("app: shop\nenv:\n")); env != nil || err != nil {
		t.Errorf("empty env: %v, %v", env, err)
	}
	for in, want := range map[string]string{
		"env: [A]\n":              "a map",
		"env:\n  lower: x\n":      "environment variable name",
		"env:\n  PORT: 1\n":       "platform sets",
		"env:\n  AWS_REGION: x\n": "platform sets",
		"env:\n  A: [1, 2]\n":     "not a list or map",
		"env:\n  A:\n":            "has no value",
		"env:\n  A: ~\n":          "has no value",
		"env:\n  A: 1\n  A: 2\n":  "twice",
	} {
		_, err := parseProjectEnv([]byte("app: shop\n" + in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", in, err, want)
		}
	}
}

// envTest runs mcloud commands in a repo for app shop, against a fake
// cluster holding objs.
type envTest struct {
	t    *testing.T
	kube *kube.Client
}

func newEnvTest(t *testing.T, project string, objs ...corev1.ConfigMap) *envTest {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ProjectFile), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("MCLOUD_APP", "")
	cs := fake.NewClientset()
	for i := range objs {
		cs.CoreV1().ConfigMaps(objs[i].Namespace).Create(context.Background(), &objs[i], metav1.CreateOptions{})
	}
	return &envTest{t: t, kube: &kube.Client{Core: cs}}
}

func (e *envTest) run(stdin string, args ...string) (string, error) {
	e.t.Helper()
	var out bytes.Buffer
	g := &globals{in: strings.NewReader(stdin), out: &out, errOut: io.Discard, plain: true, testKube: e.kube}
	cmd := newRoot(g)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	return out.String(), err
}

func (e *envTest) cluster() map[string]string {
	e.t.Helper()
	d, err := e.kube.ConfigMapData(context.Background(), "shop", functions.AppEnv)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func TestEnvSetGetListUnset(t *testing.T) {
	e := newEnvTest(t, "app: shop\nenv:\n  LOG_LEVEL: info\n")
	if _, err := e.run("", "env", "set", "API_BASE=https://api.example.com", "GREETING=hello world", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if c := e.cluster(); c["API_BASE"] != "https://api.example.com" || c["GREETING"] != "hello world" || len(c) != 2 {
		t.Fatalf("cluster = %v", c)
	}
	// Update in place; an empty value is a value.
	if _, err := e.run("", "env", "set", "API_BASE=https://v2.example.com", "EMPTY=", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if c := e.cluster(); c["API_BASE"] != "https://v2.example.com" || c["EMPTY"] != "" || len(c) != 3 {
		t.Fatalf("cluster = %v", c)
	}

	for name, want := range map[string]string{"API_BASE": "https://v2.example.com\n", "LOG_LEVEL": "info\n"} {
		if got, err := e.run("", "env", "get", name); err != nil || got != want {
			t.Errorf("get %s = %q, %v", name, got, err)
		}
	}
	if _, err := e.run("", "env", "get", "NOPE"); err == nil {
		t.Error("get of a missing variable succeeded")
	}

	got, err := e.run("", "env", "list")
	if err != nil {
		t.Fatal(err)
	}
	want := "# maxcloud.yaml\nLOG_LEVEL=info\n\n# mcloud env set\nAPI_BASE=https://v2.example.com\nEMPTY=\"\"\nGREETING=\"hello world\"\n"
	if got != want {
		t.Errorf("list =\n%s\nwant\n%s", got, want)
	}

	if _, err := e.run("", "env", "unset", "GREETING", "EMPTY", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if c := e.cluster(); len(c) != 1 || c["API_BASE"] == "" {
		t.Errorf("cluster = %v", c)
	}
	if _, err := e.run("", "env", "unset", "GREETING", "--no-restart"); err == nil {
		t.Error("unset of a missing variable succeeded")
	}
	if _, err := e.run("", "env", "unset", "LOG_LEVEL", "--no-restart"); err == nil || !strings.Contains(err.Error(), "maxcloud.yaml") {
		t.Errorf("unset of maxcloud.yaml's variable: %v", err)
	}
}

func TestEnvSetRefuses(t *testing.T) {
	e := newEnvTest(t, "app: shop\nenv:\n  LOG_LEVEL: info\n")
	e.kube.ApplySecret(context.Background(), "shop", functions.AppSecrets, map[string]string{"STRIPE_KEY": "sk"})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"LOG_LEVEL=debug"}, "maxcloud.yaml"},
		{[]string{"STRIPE_KEY=x"}, "mcloud secrets unset"},
		{[]string{"DATABASE_URL=x"}, "platform sets"},
		{[]string{"lower=x"}, "environment variable name"},
		{[]string{"NO_VALUE"}, "NAME=VALUE"},
	} {
		_, err := e.run("", append([]string{"env", "set", "--no-restart"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
	}
	if c := e.cluster(); len(c) != 0 {
		t.Errorf("wrote %v", c)
	}
}

// A secret can't take a name that's a plain variable already, in the
// cluster or maxcloud.yaml.
func TestSecretsSetRefusesEnvNames(t *testing.T) {
	e := newEnvTest(t, "app: shop\nenv:\n  LOG_LEVEL: info\n")
	e.kube.ApplyConfigMap(context.Background(), "shop", functions.AppEnv, map[string]string{"API_BASE": "x"})
	for name, want := range map[string]string{"API_BASE": "mcloud env unset", "LOG_LEVEL": "maxcloud.yaml"} {
		_, err := e.run("v\n", "secrets", "set", name, "--no-restart")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	if s, _ := e.kube.SecretData(context.Background(), "shop", functions.AppSecrets); len(s) != 0 {
		t.Errorf("secrets = %v", s)
	}
}

// mcloud generate (what the deploy workflow runs) puts maxcloud.yaml's env
// in the manifests.
func TestGenerateWritesProjectEnv(t *testing.T) {
	e := newEnvTest(t, "app: shop\nenv:\n  LOG_LEVEL: debug\n  RATIO: 0.10\n")
	desc := `{"sdk":"node","version":"0.5.0","functions":[{"name":"api","export":"api","kind":"http"}]}`
	if _, err := e.run(desc, "generate", "--from", "-"); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join("deploy", "k8s", "functions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- name: LOG_LEVEL\n", "value: debug\n", "- name: RATIO\n", `value: "0.10"`, "name: app-env\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("functions.yaml lacks %q:\n%s", want, out)
		}
	}
}
