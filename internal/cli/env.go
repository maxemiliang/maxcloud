package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

func envCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "env",
		Short: "An app's plain environment variables, for all its functions",
		Long: `Plain (not secret) environment variables for every function of the app,
like a Worker's vars. They come from two places:

  - env: in maxcloud.yaml, kept in git and deployed with the app: the
    manifests carry them, so a change is a deploy. These win.
  - mcloud env set: kept in the ConfigMap ` + functions.AppEnv + ` in the app's
    namespace, for values that change without a deploy. The maxcloud
    controller keeps a copy and puts it back after a cluster rebuild.

Setting or removing one restarts the app's HTTP and queue functions, so
they have it right away (--no-restart: on their next start). Scheduled
functions get it on their next run. For anything secret, use mcloud
secrets: values here are readable by anyone who can see the app.

Run these in the app's repo, or name the app with --app.`,
	}
	c.AddCommand(envSetCmd(g), envGetCmd(g), envUnsetCmd(g), envListCmd(g))
	return c
}

func envSetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "set NAME=VALUE...",
		Short: "Set (or change) variables",
		Example: `  mcloud env set LOG_LEVEL=debug
  mcloud env set API_BASE=https://api.example.com FEATURE_X=on`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set := map[string]string{}
			var order []string
			for _, a := range args {
				name, value, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("%q: expected NAME=VALUE (NAME= for an empty value)", a)
				}
				if err := checkEnvName(name); err != nil {
					return err
				}
				if _, dup := set[name]; !dup {
					order = append(order, name)
				}
				set[name] = value
			}
			app, err := appName(nonEmpty(f.app))
			if err != nil {
				return err
			}
			if file := projectEnvFor(app); file != nil {
				for _, n := range order {
					if _, ok := file[n]; ok {
						return fmt.Errorf("%s is in %s's env, which wins over mcloud env set: change it there", n, ProjectFile)
					}
				}
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			secrets, err := k.SecretData(cmd.Context(), app, functions.AppSecrets)
			if err != nil {
				return err
			}
			for _, n := range order {
				if _, ok := secrets[n]; ok {
					return fmt.Errorf("%s is a secret of %s's; to make it a plain variable, mcloud secrets unset %s first", n, app, n)
				}
			}
			var changes []string
			err = g.editEnv(cmd.Context(), k, app, f, func(env map[string]string) error {
				for _, n := range order {
					old, had := env[n]
					switch {
					case !had:
						changes = append(changes, fmt.Sprintf("%s=%s", n, set[n]))
					case old != set[n]:
						changes = append(changes, fmt.Sprintf("%s: %s -> %s", n, old, set[n]))
					}
					env[n] = set[n]
				}
				if len(changes) == 0 {
					return errUnchanged
				}
				return nil
			}, "Set "+strings.Join(order, ", "), &changes)
			if errors.Is(err, errUnchanged) {
				g.UI().Note("nothing to change: %s already as given", strings.Join(order, ", "))
				return nil
			}
			return err
		},
	}
	f.bind(c, true)
	return c
}

func envUnsetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "unset NAME...",
		Short: "Remove variables set with mcloud env set",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := appName(nonEmpty(f.app))
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			file := projectEnvFor(app)
			return g.editEnv(cmd.Context(), k, app, f, func(env map[string]string) error {
				for _, n := range args {
					if _, ok := env[n]; !ok {
						if _, inFile := file[n]; inFile {
							return fmt.Errorf("%s is set in %s's env: remove it there", n, ProjectFile)
						}
						return fmt.Errorf("no variable %s", n)
					}
					delete(env, n)
				}
				return nil
			}, "Removed "+strings.Join(args, ", "), nil)
		},
	}
	f.bind(c, true)
	return c
}

func envGetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "get NAME",
		Short: "Print a variable's value",
		Long: `Print a variable's value, as the app's functions get it: maxcloud.yaml's
env (in this checkout) wins over what mcloud env set keeps in the cluster.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			app, err := appName(nonEmpty(f.app))
			if err != nil {
				return err
			}
			if v, ok := projectEnvFor(app)[name]; ok {
				fmt.Fprintln(g.out, v)
				return nil
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			env, err := k.ConfigMapData(cmd.Context(), app, functions.AppEnv)
			if err != nil {
				return err
			}
			v, ok := env[name]
			if !ok {
				return fmt.Errorf("no variable %s (mcloud env list shows them all)", name)
			}
			fmt.Fprintln(g.out, v)
			return nil
		},
	}
	f.bind(c, false)
	return c
}

func envListCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "list",
		Short: "List the app's variables and their values",
		Long: `List the app's variables as NAME=VALUE lines (a .env file): those from
maxcloud.yaml's env (in this checkout), then those set with mcloud env set,
each group under a comment. A variable in both is listed once, with
maxcloud.yaml's value: that's the one the functions get.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := appName(nonEmpty(f.app))
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			cluster, err := k.ConfigMapData(cmd.Context(), app, functions.AppEnv)
			if err != nil {
				return err
			}
			file := projectEnvFor(app)
			for n := range file {
				delete(cluster, n)
			}
			if len(file) == 0 && len(cluster) == 0 {
				g.UI().Note("no variables")
				return nil
			}
			if len(file) > 0 {
				fmt.Fprintf(g.out, "# %s\n", ProjectFile)
				writeDotenv(g.out, file)
			}
			if len(cluster) > 0 {
				if len(file) > 0 {
					fmt.Fprintln(g.out)
				}
				fmt.Fprintln(g.out, "# mcloud env set")
				writeDotenv(g.out, cluster)
			}
			return nil
		},
	}
	f.bind(c, false)
	return c
}

// errUnchanged: an edit that leaves the variables as they are.
var errUnchanged = errors.New("unchanged")

// editEnv reads the app's ConfigMap app-env, changes it with edit, writes
// it back and restarts the functions (unless told not to). changes, if
// given, are shown as hints after the change.
func (g *globals) editEnv(ctx context.Context, k *kube.Client, app string, f secretsFlags, edit func(map[string]string) error, done string, changes *[]string) error {
	env, err := k.ConfigMapData(ctx, app, functions.AppEnv)
	if err != nil {
		return err
	}
	if err := edit(env); err != nil {
		return err
	}
	if err := k.ApplyConfigMap(ctx, app, functions.AppEnv, env); err != nil {
		return err
	}
	var hints []string
	if changes != nil {
		hints = *changes
	}
	return g.restartAfterChange(ctx, k, app, f.noRestart, done, hints)
}

// manifestEnv is maxcloud.yaml's env for app's manifests: nil without a
// maxcloud.yaml (or one for another app), an error if it can't be read.
func manifestEnv(app string) (map[string]string, error) {
	p, err := loadProject()
	if errors.Is(err, errNoProject) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.App != app {
		return nil, nil
	}
	return p.Env, nil
}

// projectEnvFor is maxcloud.yaml's env, if the file here is app's.
func projectEnvFor(app string) map[string]string {
	p, err := loadProject()
	if err != nil || p.App != app {
		return nil
	}
	return p.Env
}

// writeDotenv writes env as sorted NAME=VALUE lines, quoting values that
// a .env reader would otherwise take differently.
func writeDotenv(w io.Writer, env map[string]string) {
	names := make([]string, 0, len(env))
	for n := range env {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "%s=%s\n", n, dotenvValue(env[n]))
	}
}

func dotenvValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\n\r\"'#$\\`") {
		return strconv.Quote(v)
	}
	return v
}
