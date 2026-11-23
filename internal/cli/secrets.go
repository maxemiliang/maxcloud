package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/envname"
	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/kustomize"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// legacySecretsFile is where GitOps apps kept their secrets before they
// moved to the cluster: SOPS-encrypted in the manifests directory.
const legacySecretsFile = "secrets.sops.yaml"

// reservedEnv: names the platform sets itself, which a secret or variable
// would shadow or be shadowed by.
func reservedEnv(name string) bool { return envname.Reserved(name) }

// checkEnvName: name can be a secret or variable of the app's. The
// platform's apps API checks secrets' names the same way (the controller
// has a copy of package envname).
func checkEnvName(name string) error { return envname.Check(name) }

func secretsCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "secrets",
		Short: "An app's secrets: environment variables for all its functions",
		Long: `Secrets reach every function of the app as environment variables (the
SDKs' secret("NAME") reads one), from the Secret ` + functions.AppSecrets + ` in the app's
namespace, for GitOps and direct apps alike: nothing to commit, no keys to
hold.

On a platform whose apps API offers secrets, they're kept in OpenBao, and
mcloud changes them through the API (as you: the app's owners and admins
may). External Secrets Operator copies them into the Secret, and mcloud
waits for that. On older platforms mcloud edits the Secret itself, and
the maxcloud controller keeps an encrypted copy of it.

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
	c.Flags().StringVar(&f.app, "app", "", "the app (default: $MCLOUD_APP, else maxcloud.yaml's)")
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
			if err := checkEnvName(name); err != nil {
				return fmt.Errorf("secret %w", err)
			}
			app, err := appName(nonEmpty(f.app))
			if err != nil {
				return err
			}
			if _, ok := projectEnvFor(app)[name]; ok {
				return fmt.Errorf("%s is in %s's env, which wins over a secret: take it out of there first", name, ProjectFile)
			}
			value, err := readSecretValue(g, name)
			if err != nil {
				return err
			}
			return g.changeSecret(cmd.Context(), f, name, &value)
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
			return g.changeSecret(cmd.Context(), f, args[0], nil)
		},
	}
	f.bind(c, true)
	return c
}

func secretsListCmd(g *globals) *cobra.Command {
	var f secretsFlags
	var asJSON bool
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
			var names []string
			version := 0
			api, err := g.appAPI(cmd.Context(), k, appapi.Secrets)
			switch {
			case err == nil:
				list, err := api.ListSecrets(cmd.Context(), app)
				if err != nil {
					return err
				}
				for _, s := range list.Secrets {
					names = append(names, s.Name)
				}
				version = list.Version
			case errors.Is(err, errNoAppAPI):
				// An older platform: the Secret is where they are.
				data, err := k.SecretData(cmd.Context(), app, functions.AppSecrets)
				if err != nil {
					return err
				}
				for n := range data {
					names = append(names, n)
				}
			default:
				return err
			}
			sort.Strings(names)
			if asJSON {
				type secret struct {
					Name string `json:"name"`
				}
				out := struct {
					App     string   `json:"app"`
					Version int      `json:"version,omitempty"`
					Secrets []secret `json:"secrets"`
				}{App: app, Version: version, Secrets: []secret{}}
				for _, n := range names {
					out.Secrets = append(out.Secrets, secret{Name: n})
				}
				return g.printJSON(out)
			}
			if len(names) == 0 {
				g.UI().Note("no secrets")
			}
			for _, n := range names {
				fmt.Fprintln(g.out, n)
			}
			if p, err := loadProject(); err == nil && p.App == app && legacySecretsPath(p) != "" {
				g.UI().Note("%s is still in git; mcloud secrets migrate moves the app off it", legacySecretsPath(p))
			}
			return nil
		},
	}
	f.bind(c, false)
	jsonFlag(c, &asJSON)
	return c
}

// changeSecret sets name to *value, or removes it if value is nil, and
// restarts the functions (unless told not to). Through the platform's apps
// API if it offers secrets (they're in OpenBao then), else by editing the
// Secret itself, as older platforms need.
func (g *globals) changeSecret(ctx context.Context, f secretsFlags, name string, value *string) error {
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
	done := "Removed " + name
	if value != nil {
		done = "Set " + name
		// A plain variable of the same name would be two values for one
		// name; the secret would win, silently.
		env, err := k.ConfigMapData(ctx, app, functions.AppEnv)
		if err != nil {
			return err
		}
		if _, ok := env[name]; ok {
			return fmt.Errorf("%s is a plain variable of %s's (mcloud env); mcloud env unset %s first", name, app, name)
		}
	}

	api, err := g.appAPI(ctx, k, appapi.Secrets)
	if errors.Is(err, errNoAppAPI) {
		return g.editSecretDirectly(ctx, k, f, app, name, value, done, err)
	}
	if err != nil {
		return err
	}
	var change *appapi.SecretChange
	if value != nil {
		change, err = api.SetSecret(ctx, app, name, *value)
	} else {
		change, err = api.UnsetSecret(ctx, app, name)
	}
	var re *appapi.RequestError
	if errors.As(err, &re) {
		return errors.New(re.Msg)
	}
	if err != nil {
		return err
	}
	if !change.Synced {
		// Restarting now would start the functions with the old value.
		g.UI().Success("%s", done)
		g.UI().Warn("%s", change.Message)
		g.UI().Hint("Functions get it when they next start after that.")
		return nil
	}
	return g.restartAfterChange(ctx, k, app, f.noRestart, done, nil)
}

// editSecretDirectly changes the Secret app-secrets itself, on a platform
// whose apps API doesn't offer secrets (why says why not).
func (g *globals) editSecretDirectly(ctx context.Context, k *kube.Client, f secretsFlags, app, name string, value *string, done string, why error) error {
	// If External Secrets Operator makes the Secret, a change here would
	// be gone on its next sync.
	if eso, err := k.SecretFromExternalSecret(ctx, app, functions.AppSecrets); err != nil {
		return err
	} else if eso {
		return fmt.Errorf("%s/%s is made from the platform's secret store, so a change to it would be overwritten, and mcloud can't reach the apps API to change it there: %s", app, functions.AppSecrets, apiHint(why, "try again, or check mcloud doctor"))
	}
	s, err := k.SecretData(ctx, app, functions.AppSecrets)
	if err != nil {
		return err
	}
	if value != nil {
		s[name] = *value
	} else {
		if _, ok := s[name]; !ok {
			return fmt.Errorf("no secret %s", name)
		}
		delete(s, name)
	}
	if err := k.ApplySecret(ctx, app, functions.AppSecrets, s); err != nil {
		return err
	}
	return g.restartAfterChange(ctx, k, app, f.noRestart, done, nil)
}

// restartAfterChange reports a change to the app's secrets or env, and
// restarts its functions so they have it (unless noRestart).
func (g *globals) restartAfterChange(ctx context.Context, k *kube.Client, app string, noRestart bool, done string, hints []string) error {
	u := g.UI()
	if noRestart {
		u.Success("%s", done)
		for _, h := range hints {
			u.Hint("%s", h)
		}
		u.Hint("Functions get it when they next start.")
		return nil
	}
	restarted, err := k.RestartFunctions(ctx, app)
	if err != nil {
		return fmt.Errorf("%s, but restarting the functions failed (they get it when they next start): %w", strings.ToLower(done[:1])+done[1:], err)
	}
	u.Success("%s", done)
	for _, h := range hints {
		u.Hint("%s", h)
	}
	if len(restarted) > 0 {
		u.Hint("restarting %s", strings.Join(restarted, ", "))
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
				g.UI().Note("nothing to migrate: no %s here", legacySecretsFile)
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
			g.UI().Success("The cluster's Secret %s/%s is mcloud's now, and %s is gone", p.App, functions.AppSecrets, rel)
			g.UI().Hint("Commit and push: git commit -am \"secrets: kept in the cluster\" && git push")
			return nil
		},
	}
}

// readSecretValue reads the value from stdin: a hidden prompt on a
// terminal, else all of it (one trailing newline dropped).
func readSecretValue(g *globals, name string) (string, error) {
	if f, ok := g.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		v, err := g.UI().Secret("Value for " + name)
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", errors.New("empty value")
		}
		return v, nil
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
