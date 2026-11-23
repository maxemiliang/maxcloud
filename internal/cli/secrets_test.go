package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/functions"
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

func TestReservedEnv(t *testing.T) {
	for name, want := range map[string]bool{"DATABASE_URL": true, "AWS_REGION": true, "MAXCLOUD_APP": true, "STRIPE_KEY": false} {
		if reservedEnv(name) != want {
			t.Errorf("%s: %v", name, !want)
		}
	}
}

// fakeSecretsAPI is the apps API's secrets routes, with values in memory.
type fakeSecretsAPI struct {
	values  map[string]string
	version int
	synced  bool
	puts    []string
}

func (f *fakeSecretsAPI) client(t *testing.T) *appapi.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apps/shop/secrets", func(w http.ResponseWriter, r *http.Request) {
		out := appapi.SecretsResponse{App: "shop", Version: f.version, Secrets: []appapi.SecretName{}}
		for n := range f.values {
			out.Secrets = append(out.Secrets, appapi.SecretName{Name: n})
		}
		json.NewEncoder(w).Encode(out)
	})
	change := func(w http.ResponseWriter, name string) {
		f.version++
		status := http.StatusOK
		out := appapi.SecretChange{App: "shop", Name: name, Version: f.version, Synced: f.synced}
		if !f.synced {
			status, out.Message = http.StatusAccepted, "OpenBao has the change; the Secret app-secrets gets it on the ExternalSecret's next sync: it hasn't synced within 30s"
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(out)
	}
	mux.HandleFunc("PUT /apps/shop/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Value string }
		json.NewDecoder(r.Body).Decode(&in)
		f.puts = append(f.puts, r.PathValue("name")+"="+in.Value)
		f.values[r.PathValue("name")] = in.Value
		change(w, r.PathValue("name"))
	})
	mux.HandleFunc("DELETE /apps/shop/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.values[r.PathValue("name")]; !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "no secret " + r.PathValue("name")})
			return
		}
		delete(f.values, r.PathValue("name"))
		change(w, r.PathValue("name"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &appapi.Client{Base: srv.URL + "/apps/", HTTP: srv.Client()}
}

// A platform that offers secrets: mcloud goes through its API, never
// touches the Secret, and still refuses names the app's env has.
func TestSecretsThroughAPI(t *testing.T) {
	e := newEnvTest(t, "app: shop\nenv:\n  LOG_LEVEL: info\n", corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: functions.AppEnv}, Data: map[string]string{"API_BASE": "x"},
	})
	f := &fakeSecretsAPI{values: map[string]string{"OLD": "o"}, version: 1, synced: true}
	e.api = f.client(t)

	if _, err := e.run("sk_123\n", "secrets", "set", "STRIPE_KEY", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || f.puts[0] != "STRIPE_KEY=sk_123" {
		t.Errorf("puts %v", f.puts)
	}
	if s, _ := e.kube.SecretData(context.Background(), "shop", functions.AppSecrets); len(s) != 0 {
		t.Errorf("the Secret was written: %v", s)
	}
	for _, name := range []string{"LOG_LEVEL", "API_BASE", "AWS_REGION"} {
		if _, err := e.run("v\n", "secrets", "set", name, "--no-restart"); err == nil {
			t.Errorf("%s: set", name)
		}
	}
	if len(f.puts) != 1 {
		t.Errorf("refused names reached the API: %v", f.puts)
	}

	out, err := e.run("", "secrets", "list", "--json")
	if err != nil || !strings.Contains(out, `"version": 2`) || !strings.Contains(out, `"name": "OLD"`) || !strings.Contains(out, `"name": "STRIPE_KEY"`) {
		t.Errorf("list: %v\n%s", err, out)
	}

	if _, err := e.run("", "secrets", "unset", "NOPE", "--no-restart"); err == nil || err.Error() != "no secret NOPE" {
		t.Errorf("unset of nothing: %v", err)
	}
	if _, err := e.run("", "secrets", "unset", "OLD", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.values["OLD"]; ok {
		t.Error("not removed")
	}

	// Not synced in time: no restart (it would start the old value), and
	// mcloud says so.
	f.synced = false
	var errOut bytes.Buffer
	g := &globals{in: strings.NewReader("v\n"), out: io.Discard, errOut: &errOut, plain: true, testKube: e.kube, testAppAPI: e.api}
	cmd := newRoot(g)
	cmd.SetArgs([]string{"secrets", "set", "LATE"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "hasn't synced") {
		t.Errorf("no warning:\n%s", errOut.String())
	}
}

// An older platform (no secrets in its apps API): mcloud edits the Secret.
// But not one External Secrets Operator makes: that change would be lost.
func TestSecretsDirectFallback(t *testing.T) {
	e := newEnvTest(t, "app: shop\n")
	if _, err := e.run("v1\n", "secrets", "set", "A", "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.kube.SecretData(context.Background(), "shop", functions.AppSecrets); s["A"] != "v1" {
		t.Fatalf("Secret %v", s)
	}
	out, err := e.run("", "secrets", "list", "--json")
	if err != nil || strings.Contains(out, "version") || !strings.Contains(out, `"name": "A"`) {
		t.Errorf("list: %v\n%s", err, out)
	}

	s, _ := e.kube.Core.CoreV1().Secrets("shop").Get(context.Background(), functions.AppSecrets, metav1.GetOptions{})
	s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "external-secrets.io/v1", Kind: "ExternalSecret", Name: "app-secrets", UID: "u"}}
	e.kube.Core.CoreV1().Secrets("shop").Update(context.Background(), s, metav1.UpdateOptions{})
	if _, err := e.run("v2\n", "secrets", "set", "A", "--no-restart"); err == nil || !strings.Contains(err.Error(), "overwritten") {
		t.Fatalf("set on an ESO-made Secret: %v", err)
	}
	if s, _ := e.kube.SecretData(context.Background(), "shop", functions.AppSecrets); s["A"] != "v1" {
		t.Errorf("Secret changed: %v", s)
	}
}
