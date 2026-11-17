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
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
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

// A machine the API server doesn't take connections from gets the gateway
// in its kubeconfig; one that can reach it gets the API server.
func TestWriteKubeconfigThroughGateway(t *testing.T) {
	ca := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	const server, gateway = "https://api.example:6443", "https://gateway.example"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"apiServer":%q,"gateway":%q,"certificateAuthorityData":%q}`, server, gateway, base64.StdEncoding.EncodeToString([]byte(ca)))
	}))
	defer srv.Close()
	t.Setenv("MCLOUD_PLATFORM_URL", srv.URL)
	reachable := false
	probed := ""
	old := canReach
	canReach = func(_ context.Context, u string) bool { probed = u; return reachable }
	defer func() { canReach = old }()

	var errOut strings.Builder
	g := &globals{out: io.Discard, errOut: &errOut}
	out := filepath.Join(t.TempDir(), "kubeconfig")
	write := func(via string) *clientcmdapi.Cluster {
		t.Helper()
		errOut.Reset()
		f := authFlags{issuer: "https://auth.lab.gronblom.dev", clientID: "kubernetes", via: via}
		if _, err := g.writeKubeconfig(context.Background(), f, out, "homelab", false); err != nil {
			t.Fatal(err)
		}
		cfg, err := clientcmd.LoadFromFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Clusters["homelab"]
	}

	// Not reachable: the gateway, with no CA (its certificate is a public one).
	if cl := write(viaAuto); cl.Server != gateway || len(cl.CertificateAuthorityData) != 0 {
		t.Fatalf("unreachable API server: cluster %+v, want the gateway without a CA", cl)
	}
	if probed != server || !strings.Contains(errOut.String(), "gateway") {
		t.Errorf("probed %q, said %q", probed, errOut.String())
	}
	// Nothing changed: nothing rewritten, nothing said.
	if write(viaAuto); errOut.Len() != 0 {
		t.Errorf("unchanged, but said %q", errOut.String())
	}
	// On a network the API server takes connections from: back to it.
	reachable = true
	if cl := write(viaAuto); cl.Server != server || string(cl.CertificateAuthorityData) != ca {
		t.Fatalf("reachable API server: cluster %+v, want it directly", cl)
	}
	if !strings.Contains(errOut.String(), "directly") {
		t.Errorf("said %q", errOut.String())
	}
	// --via gateway says so whatever the probe would find, and --via direct
	// doesn't probe.
	if cl := write(viaGateway); cl.Server != gateway {
		t.Fatalf("--via gateway: cluster %+v", cl)
	}
	reachable, probed = false, ""
	if cl := write(viaDirect); cl.Server != server || probed != "" {
		t.Fatalf("--via direct: cluster %+v, probed %q", cl, probed)
	}

	f := authFlags{via: "tunnel"}
	if _, err := g.writeKubeconfig(context.Background(), f, out, "homelab", false); err == nil || !strings.Contains(err.Error(), "auto, direct or gateway") {
		t.Errorf("--via tunnel: %v", err)
	}
}

// A platform without a gateway: --via gateway is an error, and auto doesn't
// probe (there's nothing else to pick).
func TestWriteKubeconfigNoGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"apiServer":"https://api.example:6443","certificateAuthorityData":"TUlJQg=="}`)
	}))
	defer srv.Close()
	t.Setenv("MCLOUD_PLATFORM_URL", srv.URL)
	old := canReach
	canReach = func(context.Context, string) bool { t.Error("probed without a gateway to fall back to"); return false }
	defer func() { canReach = old }()
	g := &globals{out: io.Discard, errOut: io.Discard}
	out := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := g.writeKubeconfig(context.Background(), authFlags{via: viaAuto}, out, "homelab", false); err != nil {
		t.Fatal(err)
	}
	if _, err := g.writeKubeconfig(context.Background(), authFlags{via: viaGateway}, out, "homelab", false); err == nil {
		t.Error("--via gateway without a gateway: no error")
	}
}
