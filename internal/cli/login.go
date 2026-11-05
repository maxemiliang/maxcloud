package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/maxemiliang/maxcloud/internal/auth"
)

type authFlags struct {
	issuer, clientID string
}

func (f *authFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.issuer, "issuer", envOr("MCLOUD_OIDC_ISSUER", auth.DefaultIssuer), "OpenID Connect provider the cluster trusts ($MCLOUD_OIDC_ISSUER)")
	c.Flags().StringVar(&f.clientID, "client-id", envOr("MCLOUD_OIDC_CLIENT_ID", auth.DefaultClientID), "its client for the cluster, the API server's audience ($MCLOUD_OIDC_CLIENT_ID)")
}

func (f *authFlags) config() auth.Config { return auth.Config{Issuer: f.issuer, ClientID: f.clientID} }

func loginCmd(g *globals) *cobra.Command {
	var f authFlags
	var browser bool
	c := &cobra.Command{
		Use:   "login",
		Short: "Log in to the cluster through Authelia",
		Long: `Log in to the cluster with the homelab's identity provider (Authelia):
mcloud, and kubectl with the kubeconfig from mcloud kubeconfig, then act as
you, with what your groups allow (admins: everything).

By default it's the device code flow: mcloud shows a link and a code, and
you approve them in any browser (also from another machine, e.g. over SSH).
--browser opens the login in this machine's browser instead.

The tokens are cached in ~/.cache/mcloud. The ID token lasts an hour and is
refreshed by itself; logging in again is needed when the refresh token
expires (12 hours) or with mcloud logout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := g.login(cmd.Context(), f.config(), browser, g.out)
			if err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Logged in as %s (groups: %s) until %s.\n", t.User, strings.Join(t.Groups, ", "), t.Expiry.Local().Format("15:04"))
			return nil
		},
	}
	f.bind(c)
	c.Flags().BoolVar(&browser, "browser", false, "log in in this machine's browser instead of with a device code")
	return c
}

// login runs a flow and caches the result. Messages go to w.
func (g *globals) login(ctx context.Context, cfg auth.Config, browser bool, w interface{ Write([]byte) (int, error) }) (*auth.Tokens, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var t *auth.Tokens
	var err error
	if browser {
		t, err = auth.BrowserLogin(ctx, cfg, func(u string) {
			fmt.Fprintf(w, "Opening the login page. If no browser opens, go to:\n  %s\n", u)
			openBrowser(u)
		})
	} else {
		t, err = auth.DeviceLogin(ctx, cfg, func(u, code string) {
			fmt.Fprintf(w, "To log in to the cluster, open\n  %s\nand check that it shows the code %s.\n", u, code)
			openBrowser(u)
		})
	}
	if err != nil {
		return nil, err
	}
	if err := auth.Save(cfg, t); err != nil {
		return nil, err
	}
	return t, nil
}

func logoutCmd(g *globals) *cobra.Command {
	var f authFlags
	c := &cobra.Command{
		Use:   "logout",
		Short: "Forget the cached login",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := auth.Forget(f.config()); err != nil {
				return err
			}
			fmt.Fprintln(g.out, "Logged out (the cached tokens are gone).")
			return nil
		},
	}
	f.bind(c)
	return c
}

// credentialCmd is a client-go exec credential plugin: kubectl and mcloud
// run it (from the kubeconfig mcloud kubeconfig writes) for a token.
func credentialCmd(g *globals) *cobra.Command {
	var f authFlags
	c := &cobra.Command{
		Use:    "credential",
		Short:  "Print a Kubernetes ExecCredential with the cached ID token (for kubeconfigs)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := f.config()
			t, err := auth.Load(cfg)
			if err != nil {
				return err
			}
			if !t.Valid(time.Now()) && t != nil && t.RefreshToken != "" {
				if r, err := auth.Refresh(cmd.Context(), cfg, t); err == nil {
					t = r
					if err := auth.Save(cfg, t); err != nil {
						return err
					}
				}
			}
			if !t.Valid(time.Now()) {
				// Log in now if someone can: the prompt goes to stderr
				// (stdout is the credential).
				if !term.IsTerminal(int(os.Stderr.Fd())) {
					return errors.New("not logged in to the cluster (or the login expired): run mcloud login")
				}
				fmt.Fprintln(g.errOut, "The cluster login expired.")
				if t, err = g.login(cmd.Context(), cfg, false, g.errOut); err != nil {
					return err
				}
			}
			return json.NewEncoder(g.out).Encode(map[string]any{
				"apiVersion": "client.authentication.k8s.io/v1",
				"kind":       "ExecCredential",
				"status": map[string]any{
					"token":               t.IDToken,
					"expirationTimestamp": t.Expiry.Add(-time.Minute).UTC().Format(time.RFC3339),
				},
			})
		},
	}
	f.bind(c)
	return c
}

func kubeconfigCmd(g *globals) *cobra.Command {
	var f authFlags
	var out, name string
	c := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Write a kubeconfig that logs in through Authelia (mcloud login)",
		Long: `Write a kubeconfig for the cluster in your current kubeconfig (its API
server and CA), whose user is you through Authelia: kubectl and mcloud run
mcloud credential for a token, which logs in when needed. Point
MCLOUD_KUBECONFIG (and KUBECONFIG) at it.

The admin kubeconfig isn't needed day to day after that; keep a way to get
one for when Authelia or the cluster is down (upctl kubernetes config).`,
		Example: `  mcloud kubeconfig
  set -Ux MCLOUD_KUBECONFIG ~/.kube/homelab-oidc.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rules := clientcmd.NewDefaultClientConfigLoadingRules()
			if g.kubeconfig != "" {
				rules.ExplicitPath = g.kubeconfig
			} else if v := os.Getenv("MCLOUD_KUBECONFIG"); v != "" {
				rules.ExplicitPath = v
			}
			raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
				&clientcmd.ConfigOverrides{CurrentContext: g.kubeContext}).RawConfig()
			if err != nil {
				return fmt.Errorf("reading your kubeconfig for the cluster's address: %w", err)
			}
			ctxName := g.kubeContext
			if ctxName == "" {
				ctxName = raw.CurrentContext
			}
			kctx, ok := raw.Contexts[ctxName]
			if !ok {
				return fmt.Errorf("no context %q in your kubeconfig", ctxName)
			}
			cluster, ok := raw.Clusters[kctx.Cluster]
			if !ok {
				return fmt.Errorf("no cluster %q in your kubeconfig", kctx.Cluster)
			}
			ca := cluster.CertificateAuthorityData
			if len(ca) == 0 && cluster.CertificateAuthority != "" {
				if ca, err = os.ReadFile(cluster.CertificateAuthority); err != nil {
					return err
				}
			}
			args := []string{"credential"}
			if f.issuer != auth.DefaultIssuer {
				args = append(args, "--issuer", f.issuer)
			}
			if f.clientID != auth.DefaultClientID {
				args = append(args, "--client-id", f.clientID)
			}
			cfg := clientcmdapi.NewConfig()
			cfg.Clusters[name] = &clientcmdapi.Cluster{Server: cluster.Server, CertificateAuthorityData: ca}
			cfg.AuthInfos[name+"-authelia"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
				APIVersion:      "client.authentication.k8s.io/v1",
				Command:         "mcloud",
				Args:            args,
				InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
				InstallHint:     "mcloud: https://github.com/maxemiliang/maxcloud (mise use -g github:maxemiliang/maxcloud)",
			}}
			cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name + "-authelia"}
			cfg.CurrentContext = name
			if strings.HasPrefix(out, "~/") {
				home, _ := os.UserHomeDir()
				out = filepath.Join(home, out[2:])
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
				return err
			}
			if err := clientcmd.WriteToFile(*cfg, out); err != nil {
				return err
			}
			if err := os.Chmod(out, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(g.out, "Wrote %s (cluster %s, login through %s).\nUse it: set -Ux MCLOUD_KUBECONFIG %s   (and KUBECONFIG for kubectl)\nThen: kubectl auth whoami\n", out, cluster.Server, f.issuer, out)
			return nil
		},
	}
	f.bind(c)
	c.Flags().StringVar(&out, "out", "~/.kube/homelab-oidc.yaml", "where to write it")
	c.Flags().StringVar(&name, "name", "homelab", "cluster and context name in it")
	return c
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return // no desktop (e.g. over SSH): the link is printed
		}
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}
