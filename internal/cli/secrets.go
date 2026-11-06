package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kustomize"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// legacySecretsFile is where GitOps apps kept their secrets before they
// moved to the cluster: SOPS-encrypted in the manifests directory.
const legacySecretsFile = "secrets.sops.yaml"

var secretNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// reservedSecret: names the platform sets itself, which a secret would
// shadow or be shadowed by.
func reservedSecret(name string) bool {
	return name == "DATABASE_URL" || name == "FUNCTION_TARGET" || name == "PORT" ||
		strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "MAXCLOUD_") || strings.HasPrefix(name, "K_")
}

func secretsCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "secrets",
		Short: "An app's secrets: environment variables for all its functions",
		Long: `Secrets reach every function of the app as environment variables (the
SDKs' secret("NAME") reads one). They live in the Secret ` + functions.AppSecrets + ` in the
app's namespace, for GitOps and direct apps alike: nothing to commit, no
keys to hold. The maxcloud controller keeps an encrypted copy and puts it
back after a cluster rebuild.

Setting or removing one restarts the app's HTTP and queue functions, so
they have it right away (--no-restart: on their next start). Scheduled
functions get it on their next run.

Run these in the app's repo, or name the app with --app.`,
	}
	c.AddCommand(secretsSetCmd(g), secretsUnsetCmd(g), secretsListCmd(g), secretsMigrateCmd(g))
	return c
}

type secretsFlags struct {
	app       string
	noRestart bool
}

func (f *secretsFlags) bind(c *cobra.Command, restart bool) {
	c.Flags().StringVar(&f.app, "app", "", "the app (default: maxcloud.yaml's, or $MCLOUD_APP)")
	if restart {
		c.Flags().BoolVar(&f.noRestart, "no-restart", false, "don't restart the functions; they get the change when they next start")
	}
}

func secretsSetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "set NAME",
		Short: "Set a secret; the value comes from stdin (or a hidden prompt)",
		Example: `  mcloud secrets set STRIPE_KEY            # asks for the value
  op read op://vault/stripe/key | mcloud secrets set STRIPE_KEY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !secretNameRe.MatchString(name) {
				return fmt.Errorf("secret %q: use an environment variable name: A-Z, 0-9 and _, not starting with a digit", name)
			}
			if reservedSecret(name) {
				return fmt.Errorf("secret %q: the platform sets that one itself", name)
			}
			value, err := readSecretValue(g, name)
			if err != nil {
				return err
			}
			return g.editSecrets(cmd.Context(), f, func(s map[string]string) error {
				s[name] = value
				return nil
			}, "Set "+name)
		},
	}
	f.bind(c, true)
	return c
}

func secretsUnsetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "unset NAME",
		Short: "Remove a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			return g.editSecrets(cmd.Context(), f, func(s map[string]string) error {
				if _, ok := s[name]; !ok {
					return fmt.Errorf("no secret %s", name)
				}
				delete(s, name)
				return nil
			}, "Removed "+name)
		},
	}
	f.bind(c, true)
	return c
}

func secretsListCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "list",
		Short: "List the app's secrets (names only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := appName(nonEmpty(f.app))
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			data, err := k.SecretData(cmd.Context(), app, functions.AppSecrets)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(data))
			for n := range data {
				names = append(names, n)
			}
			sort.Strings(names)
			if len(names) == 0 {
				fmt.Fprintln(g.out, "(no secrets)")
			}
			for _, n := range names {
				fmt.Fprintln(g.out, n)
			}
			if p, err := loadProject(); err == nil && p.App == app && legacySecretsPath(p) != "" {
				fmt.Fprintf(g.errOut, "note: %s is still in git; mcloud secrets migrate moves the app off it\n", legacySecretsPath(p))
			}
			return nil
		},
	}
	f.bind(c, false)
	return c
}

// editSecrets reads the app's secrets, changes them with edit, writes them
// back, and restarts the functions (unless told not to).
func (g *globals) editSecrets(ctx context.Context, f secretsFlags, edit func(map[string]string) error, done string) error {
	app, err := appName(nonEmpty(f.app))
	if err != nil {
		return err
	}
	// A secrets.sops.yaml that Flux still applies would put its values back
	// over these.
	if p, err := loadProject(); err == nil && p.App == app {
		if file := legacySecretsPath(p); file != "" {
			return fmt.Errorf("%s still holds this app's secrets, and Flux would overwrite changes made here: run mcloud secrets migrate first", file)
		}
	}
	k, err := g.kube()
	if err != nil {
		return err
	}
	s, err := k.SecretData(ctx, app, functions.AppSecrets)
	if err != nil {
		return err
	}
	if err := edit(s); err != nil {
		return err
	}
	if err := k.ApplySecret(ctx, app, functions.AppSecrets, s); err != nil {
		return err
	}
	if f.noRestart {
		fmt.Fprintf(g.out, "%s. Functions get it when they next start.\n", done)
		return nil
	}
	restarted, err := k.RestartFunctions(ctx, app)
	if err != nil {
		return fmt.Errorf("%s, but restarting the functions failed (they get it when they next start): %w", strings.ToLower(done[:1])+done[1:], err)
	}
	if len(restarted) == 0 {
		fmt.Fprintf(g.out, "%s.\n", done)
	} else {
		fmt.Fprintf(g.out, "%s; restarting %s.\n", done, strings.Join(restarted, ", "))
	}
	return nil
}

// legacySecretsPath is the app's secrets.sops.yaml if it still has one
// (relative to the repo), else "".
func legacySecretsPath(p *project) string {
	dir := p.Path
	if dir == "" {
		dir = registry.DefaultPath
	}
	rel := filepath.Join(dir, legacySecretsFile)
	if _, err := os.Stat(filepath.Join(p.Dir, rel)); err != nil {
		return ""
	}
	return rel
}

func secretsMigrateCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Move a GitOps app's secrets from secrets.sops.yaml in git to the cluster",
		Long: `GitOps apps used to keep their secrets SOPS-encrypted in
deploy/k8s/` + legacySecretsFile + `. This takes over the Secret Flux decrypted
from it (so no key is needed), keeps Flux from deleting it, and removes the
file from the repo: commit and push that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadProject()
			if err != nil {
				return err
			}
			rel := legacySecretsPath(p)
			if rel == "" {
				fmt.Fprintln(g.out, "Nothing to migrate: no "+legacySecretsFile+" here.")
				return nil
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			ok, err := k.AdoptSecret(cmd.Context(), p.App, functions.AppSecrets)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("the cluster has no Secret %s/%s to take over (has Flux applied %s yet? mcloud status %s)", p.App, functions.AppSecrets, rel, p.App)
			}
			dir := filepath.Dir(filepath.Join(p.Dir, rel))
			kfile := filepath.Join(dir, "kustomization.yaml")
			if ks, err := os.ReadFile(kfile); err == nil {
				out, changed, err := kustomize.RemoveResource(string(ks), legacySecretsFile)
				if err != nil {
					return fmt.Errorf("%s: %w", kfile, err)
				}
				if changed {
					if err := os.WriteFile(kfile, []byte(out), 0o644); err != nil {
						return err
					}
				}
			}
			if err := os.Remove(filepath.Join(p.Dir, rel)); err != nil {
				return err
			}
			fmt.Fprintf(g.out, "The cluster's Secret %s/%s is mcloud's now, and %s is gone.\nCommit and push:\n  git commit -am \"secrets: kept in the cluster\" && git push\n", p.App, functions.AppSecrets, rel)
			return nil
		},
	}
}

// readSecretValue reads the value from stdin: a hidden prompt on a
// terminal, else all of it (one trailing newline dropped).
func readSecretValue(g *globals, name string) (string, error) {
	if f, ok := g.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(g.errOut, "Value for %s: ", name)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(g.errOut)
		if err != nil {
			return "", err
		}
		if len(b) == 0 {
			return "", errors.New("empty value")
		}
		return string(b), nil
	}
	b, err := io.ReadAll(g.in)
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", errors.New("empty value on stdin")
	}
	return v, nil
}
