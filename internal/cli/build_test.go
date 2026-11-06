package cli

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCredentialHelperToken(t *testing.T) {
	t.Setenv("MCLOUD_REGISTRY_TOKEN", "tok")
	var out strings.Builder
	if code := CredentialHelper([]string{"get"}, strings.NewReader("registry.gronblom.dev\n"), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	var c map[string]string
	json.Unmarshal([]byte(out.String()), &c)
	if c["Secret"] != "tok" || c["Username"] != "maxcloud" || c["ServerURL"] != "registry.gronblom.dev" {
		t.Errorf("got %v", c)
	}
}

func TestCredentialHelperGitHubOIDC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "bearer req" || r.URL.Query().Get("audience") != "registry.gronblom.dev" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"value":"oidc"}`))
	}))
	defer srv.Close()
	t.Setenv("MCLOUD_REGISTRY_TOKEN", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", srv.URL+"/?x=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "req")
	var out strings.Builder
	if code := CredentialHelper([]string{"get"}, strings.NewReader("https://registry.gronblom.dev"), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"Secret":"oidc"`) {
		t.Errorf("got %s", out.String())
	}

	// Outside both: docker's "no login" answer.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	out.Reset()
	if code := CredentialHelper([]string{"get"}, strings.NewReader("registry.gronblom.dev"), &out); code == 0 || !strings.Contains(out.String(), "credentials not found") {
		t.Errorf("exit %d: %s", code, out.String())
	}
}

func TestDockerEnvKeepsYourConfig(t *testing.T) {
	user := t.TempDir()
	os.WriteFile(filepath.Join(user, "config.json"), []byte(`{"auths":{"ghcr.io":{"auth":"x"}},"credHelpers":{"gcr.io":"gcloud"}}`), 0o600)
	os.MkdirAll(filepath.Join(user, "buildx"), 0o700)
	t.Setenv("DOCKER_CONFIG", user)
	d, err := newDockerEnv("registry.gronblom.dev", "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	var cfg struct {
		Auths       map[string]any    `json:"auths"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	b, _ := os.ReadFile(filepath.Join(d.dir, "config.json"))
	json.Unmarshal(b, &cfg)
	if cfg.Auths["ghcr.io"] == nil || cfg.CredHelpers["gcr.io"] != "gcloud" || cfg.CredHelpers["registry.gronblom.dev"] != "mcloud" {
		t.Errorf("config: %s", b)
	}
	if _, err := os.Stat(filepath.Join(d.dir, "buildx")); err != nil {
		t.Error("buildx dir not linked")
	}
	if _, err := os.Lstat(filepath.Join(d.dir, "mcloud-bin", "docker-credential-mcloud")); err != nil {
		t.Error("no credential helper link")
	}
	if b, _ := os.ReadFile(filepath.Join(user, "config.json")); strings.Contains(string(b), "mcloud") {
		t.Error("changed your config")
	}
}

func TestProjectBuildEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "project.toml")
	os.WriteFile(p, []byte("[_]\nschema-version = \"0.2\"\n\n[[io.buildpacks.build.env]]\nname = \"BP_NODE_RUN_SCRIPTS\"\nvalue = \"build\"\n\n[[io.buildpacks.build.env]]\nname = 'B'\nvalue = 'x'\n[io.buildpacks]\nname = \"not env\"\n"), 0o644)
	env, err := projectBuildEnv(p)
	if err != nil || env["BP_NODE_RUN_SCRIPTS"] != "build" || env["B"] != "x" || len(env) != 2 {
		t.Errorf("%v %v", env, err)
	}
}

func TestSourceTarLeavesOutIgnored(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "index.ts"), []byte("y"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
	b, err := sourceTar(dir) // not a git repo: everything but node_modules
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, b)
	if strings.Join(names, ",") != "package.json,src/index.ts" {
		t.Errorf("names %v", names)
	}
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}
