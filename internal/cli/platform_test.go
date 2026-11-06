package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func TestOlderThan(t *testing.T) {
	for _, c := range []struct {
		v, min string
		older  bool
	}{
		{"v0.9.0", "v0.10.0", true},
		{"v0.10.0", "v0.10.0", false},
		{"v1.0.0", "0.10.0", false},
		{"dev", "v0.10.0", false},
	} {
		if got := olderThan(c.v, c.min); got != c.older {
			t.Errorf("olderThan(%q, %q) = %v", c.v, c.min, got)
		}
	}
}

func TestWriteKubeconfigFromDiscovery(t *testing.T) {
	ca := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	server := "https://api.example:6443"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"apiServer":%q,"certificateAuthorityData":%q}`, server, base64.StdEncoding.EncodeToString([]byte(ca)))
	}))
	defer srv.Close()
	t.Setenv("MCLOUD_PLATFORM_URL", srv.URL)
	var errOut strings.Builder
	g := &globals{out: io.Discard, errOut: &errOut}
	f := authFlags{issuer: "https://auth.lab.gronblom.dev", clientID: "kubernetes"}
	out := filepath.Join(t.TempDir(), "kubeconfig")

	if _, err := g.writeKubeconfig(context.Background(), f, out, "homelab", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.LoadFromFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if cl := cfg.Clusters["homelab"]; cl == nil || cl.Server != server || string(cl.CertificateAuthorityData) != ca {
		t.Fatalf("cluster: %+v", cfg.Clusters["homelab"])
	}
	if u := cfg.AuthInfos["homelab-authelia"]; u == nil || u.Exec == nil || u.Exec.Command != "mcloud" || len(u.Exec.Args) != 1 {
		t.Fatalf("user: %+v", u)
	}

	// A rebuilt cluster: a new address is written over the old one.
	server = "https://api2.example:6443"
	if _, err := g.writeKubeconfig(context.Background(), f, out, "homelab", false); err != nil {
		t.Fatal(err)
	}
	cfg, _ = clientcmd.LoadFromFile(out)
	if cfg.Clusters["homelab"].Server != server || !strings.Contains(errOut.String(), "changed") {
		t.Errorf("not rewritten: %s / %q", cfg.Clusters["homelab"].Server, errOut.String())
	}
}
