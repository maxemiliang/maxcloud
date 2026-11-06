package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
)

// The Postgres mcloud dev runs for an app (the version maxcloud's
// databases run).
// renovate: datasource=docker depName=postgres
const devPostgresImage = "postgres:18"

// devSDKMin is the first @max-cloud/functions with a dev mode.
const devSDKMin = "0.4.0"

func devCmd(g *globals) *cobra.Command {
	var db, secrets, entry string
	var port int
	var remoteBuckets, migrations bool
	c := &cobra.Command{
		Use:   "dev",
		Short: "Run the app here, all its functions in one process",
		Long: `Run the app in the current repo on this machine, with what it gets on
maxcloud:

  - every HTTP function at http://<name>.localhost:8080/ (a list at
    http://localhost:8080/), queue workers running, and scheduled
    functions run with POST http://localhost:8080/_run/<name>;
  - DATABASE_URL: a local Postgres in Docker (--db local, the default when
    maxcloud.yaml has db:), a branch of the app's database through a
    port-forward (--db branch:NAME, see mcloud db branch), or none; the
    pending migrations in migrations/ are applied to it first (see mcloud
    db migrate);
  - MAXCLOUD_APP, so bucketName() works;
  - secrets from .env in the repo (keep it out of git), or the app's own
    from the cluster (--secrets remote);
  - the app's real buckets with --buckets remote (careful: real data).

TypeScript runs your source with Node's type stripping and restarts on
changes (node --watch); Go apps run with go run (restart it yourself). A
Nuxt app (a nuxt.config) runs nuxt dev on the port instead, at
http://localhost:8080/; @max-cloud/nuxt fills its runtimeConfig from the
same environment.`,
		Example: `  mcloud dev
  mcloud dev --db branch:try-migration
  mcloud dev --secrets remote --buckets remote`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadProject()
			if err != nil {
				return err
			}
			u := g.UI()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			env := map[string]string{
				"MAXCLOUD_APP": p.App,
				"MAXCLOUD_DEV": "1",
				"PORT":         strconv.Itoa(port),
			}
			// Secrets: .env, then (if asked) the cluster's.
			dotenv, err := readDotenv(filepath.Join(p.Dir, ".env"))
			if err != nil {
				return err
			}
			for k, v := range dotenv {
				env[k] = v
			}
			if len(dotenv) > 0 {
				u.KV([][2]string{{"secrets", fmt.Sprintf("%d from .env", len(dotenv))}})
			}
			needKube := secrets == "remote" || remoteBuckets || strings.HasPrefix(db, "branch:")
			if needKube {
				k, err := g.kube()
				if err != nil {
					return err
				}
				if secrets == "remote" {
					s, err := k.SecretData(ctx, p.App, functions.AppSecrets)
					if err != nil {
						return err
					}
					for k, v := range s {
						env[k] = v
					}
					u.KV([][2]string{{"secrets", fmt.Sprintf("%d from the cluster", len(s))}})
				}
				if remoteBuckets {
					s, err := k.SecretData(ctx, p.App, functions.StorageSecret)
					if err != nil {
						return err
					}
					if len(s) == 0 {
						return fmt.Errorf("%s has no buckets (buckets: in maxcloud.yaml)", p.App)
					}
					for k, v := range s {
						env[k] = v
					}
					u.KV([][2]string{{"buckets", u.Amber("the app's real ones") + " (" + s["AWS_ENDPOINT_URL_S3"] + ")"}})
				}
				if branch, ok := strings.CutPrefix(db, "branch:"); ok {
					url, err := k.ProxyDatabase(ctx, p.App, dbCluster(branch), 0)
					if err != nil {
						return err
					}
					env["DATABASE_URL"] = url
					u.KV([][2]string{{"database", "branch " + u.Accent(branch) + ", through a port-forward"}})
				}
			}
			if db == "" {
				db = "none"
				if p.DB != nil && p.DB.Enabled {
					db = "local"
				}
			}
			switch {
			case db == "local":
				url, err := g.devPostgres(ctx, p.App)
				if err != nil {
					return err
				}
				env["DATABASE_URL"] = url
			case db == "none":
				u.KV([][2]string{{"database", "none " + u.Dim("(--db local for a local Postgres)")}})
			case strings.HasPrefix(db, "branch:"):
			default:
				return fmt.Errorf("--db %q: local, branch:NAME or none", db)
			}

			if migrations {
				if err := g.devMigrate(ctx, p, env["DATABASE_URL"]); err != nil {
					return err
				}
			}

			run, err := devCommand(ctx, p.Dir, entry, port)
			if err != nil {
				return err
			}
			run.Env = os.Environ()
			for k, v := range env {
				run.Env = append(run.Env, k+"="+v)
			}
			run.Stdin, run.Stdout, run.Stderr = os.Stdin, g.out, g.errOut
			// Ctrl-C reaches the app (same process group); give it time to stop.
			run.Cancel = func() error { return run.Process.Signal(os.Interrupt) }
			run.WaitDelay = 30 * time.Second
			if err := run.Start(); err != nil {
				return err
			}
			if err := run.Wait(); err != nil && ctx.Err() == nil {
				return err
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&db, "db", "", "local, branch:NAME or none (default: local if maxcloud.yaml has db:, else none)")
	f.StringVar(&secrets, "secrets", "local", "local (.env) or remote (the app's secrets on the cluster, and .env)")
	f.BoolVar(&remoteBuckets, "buckets", false, "use the app's real buckets (its S3 credentials from the cluster)")
	f.BoolVar(&migrations, "migrate", true, "apply the pending migrations in migrations/ to the database first")
	f.IntVar(&port, "port", 8080, "port to serve the HTTP functions on")
	f.StringVar(&entry, "entry", "", "TypeScript: the module exporting the functions (default src/index.ts, else package.json main)")
	return c
}

// devCommand is the SDK's dev mode for the app in dir.
func devCommand(ctx context.Context, dir, entry string, port int) (*exec.Cmd, error) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		cmd := exec.CommandContext(ctx, "go", "run", ".")
		cmd.Dir = dir
		return cmd, nil
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return nil, errors.New("no package.json or go.mod here: which app is this?")
	}
	if isNuxt(dir) {
		// Nuxt's own dev server (Vite, hot reload); @max-cloud/nuxt turns
		// the environment into runtimeConfig there.
		bin := findBin(dir, "nuxt")
		if bin == "" {
			return nil, errors.New("this is a Nuxt app, but nuxt isn't installed: npm install")
		}
		cmd := exec.CommandContext(ctx, bin, "dev", "--port", strconv.Itoa(port), "--host", "127.0.0.1")
		cmd.Dir = dir
		return cmd, nil
	}
	sdk := filepath.Join(dir, "node_modules", "@max-cloud", "functions")
	b, err := os.ReadFile(filepath.Join(sdk, "package.json"))
	if err != nil {
		return nil, errors.New("@max-cloud/functions isn't installed: npm install")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &pkg)
	if olderThan(pkg.Version, devSDKMin) {
		return nil, fmt.Errorf("mcloud dev needs @max-cloud/functions %s or newer (this app has %s): npm install @max-cloud/functions@latest", devSDKMin, pkg.Version)
	}
	args := []string{"--watch", filepath.Join(sdk, "dist", "cli.js"), "dev", "--port", strconv.Itoa(port)}
	if entry != "" {
		args = append(args, "--entry", entry)
	}
	cmd := exec.CommandContext(ctx, "node", args...)
	cmd.Dir = dir
	return cmd, nil
}

// isNuxt reports whether dir is a Nuxt app (a nuxt.config next to package.json).
func isNuxt(dir string) bool {
	for _, ext := range []string{"ts", "mts", "js", "mjs"} {
		if _, err := os.Stat(filepath.Join(dir, "nuxt.config."+ext)); err == nil {
			return true
		}
	}
	return false
}

// findBin finds node_modules/.bin/name in dir or a parent (npm
// workspaces hoist them), or "".
func findBin(dir, name string) string {
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, "node_modules", ".bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// devPostgres runs (or reuses) a Postgres container for the app, keeping
// its data in a volume between runs, and returns its URL.
func (g *globals) devPostgres(ctx context.Context, app string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("--db local needs docker (or --db branch:NAME, or --db none)")
	}
	name := "mcloud-dev-" + app
	step := g.UI().Step("Starting Postgres " + name)
	fail := func(err error) (string, error) {
		step.Fail("Postgres didn't start")
		return "", err
	}
	state, _ := cmdOutput(exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name))
	switch state {
	case "true":
	case "false":
		if _, err := cmdOutput(exec.CommandContext(ctx, "docker", "start", name)); err != nil {
			return fail(err)
		}
	default:
		step.Status("a new container (%s)", devPostgresImage)
		if _, err := cmdOutput(exec.CommandContext(ctx, "docker", "run", "-d", "--name", name,
			"-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=dev", "-e", "POSTGRES_DB=app",
			"-v", name+":/var/lib/postgresql", devPostgresImage)); err != nil {
			return fail(fmt.Errorf("starting Postgres: %w", err))
		}
	}
	out, err := cmdOutput(exec.CommandContext(ctx, "docker", "port", name, "5432/tcp"))
	if err != nil {
		return fail(err)
	}
	hostPort := strings.Fields(out)[0]
	for i := 0; ; i++ {
		if exec.CommandContext(ctx, "docker", "exec", name, "pg_isready", "-q", "-U", "postgres").Run() == nil {
			break
		}
		if i > 60 {
			return fail(fmt.Errorf("Postgres in %s isn't ready after a minute: docker logs %s", name, name))
		}
		if err := sleep(ctx, time.Second); err != nil {
			return fail(err)
		}
	}
	step.Done("Postgres %s is up", name)
	g.UI().KV([][2]string{{"database", "local Postgres " + g.UI().Accent(name) + " at " + hostPort + g.UI().Dim(" (docker rm -f "+name+"; docker volume rm "+name+" to start over)")}})
	return "postgres://postgres:dev@" + hostPort + "/app?sslmode=disable", nil
}

// readDotenv reads KEY=VALUE lines (# comments, optional export and
// quotes). A missing file is no secrets.
func readDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: not KEY=VALUE", path, n)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			if v[0] == '"' {
				if u, err := strconv.Unquote(v); err == nil {
					v = u
				} else {
					v = v[1 : len(v)-1]
				}
			} else {
				v = v[1 : len(v)-1]
			}
		}
		out[k] = v
	}
	return out, sc.Err()
}
