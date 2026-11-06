package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/registry"
	"github.com/maxemiliang/maxcloud/internal/ui"
)

// The files a new functions app starts with. Each is written only if it
// doesn't exist yet.
var starter = map[string]string{
	"package.json": `{
  "name": "%[1]s",
  "private": true,
  "type": "module",
  "main": "dist/index.js",
  "engines": {
    "node": "24.x"
  },
  "scripts": {
    "build": "tsc",
    "start": "maxcloud-functions",
    "dev": "mcloud dev"
  },
  "dependencies": {
    "@max-cloud/functions": "^%[3]s"
  },
  "devDependencies": {
    "@types/node": "^24.19.2",
    "typescript": "^7.0.2"
  }
}
`,
	"tsconfig.json": `{
  "compilerOptions": {
    "target": "es2023",
    "module": "nodenext",
    "moduleResolution": "nodenext",
    "types": ["node"],
    "strict": true,
    "outDir": "dist",
    "rootDir": "src",
    "skipLibCheck": true
  },
  "include": ["src"]
}
`,
	"src/index.ts": `import { onRequest } from "@max-cloud/functions";

// Every exported function is deployed on its own. This one is a Knative
// service: private (cluster-local) until you pass { public: true }, and
// scaled to zero when idle. See onSchedule and onQueue for the other kinds.
export const hello = onRequest(async (req) => {
  return new Response(` + "`Hello from ${new URL(req.url).pathname}\\n`" + `);
});
`,
	".gitignore": `node_modules/
dist/
# mcloud dev's secrets
.env
`,
	// Paketo only compiles TypeScript when told to.
	"project.toml": `# Buildpacks settings (Paketo): run "npm run build" before the image is made.
[_]
schema-version = "0.2"

[[io.buildpacks.build.env]]
name = "BP_NODE_RUN_SCRIPTS"
value = "build"
`,
}

// The files a new Go functions app starts with.
var goStarter = map[string]string{
	".gitignore": `# mcloud dev's secrets
.env
`,
	"go.mod": `module %[2]s

go 1.25.0
`,
	"main.go": `package main

import (
	"fmt"
	"net/http"

	"github.com/maxemiliang/maxcloud/functions"
)

func main() {
	// Every registered function is deployed on its own. This one is a
	// Knative service: private (cluster-local) until you add
	// functions.Public(), and scaled to zero when idle. See
	// functions.Schedule and functions.Queue for the other kinds.
	functions.HTTPFunc("hello", hello)
	functions.Start()
}

func hello(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello from %s\n", r.URL.Path)
}
`,
}

// Always part of an app, whatever language it's in.
var appFiles = map[string]string{
	"maxcloud.yaml": `# This repo's app on maxcloud: the whole app's definition. mcloud deploy
# registers the app from it the first time and keeps it in line after that
# (no commit to the homelab repo); mcloud status, logs and the rest use it too.
app: %[1]s
# What the app may reserve (defaults: 200m CPU, 1Gi memory, 20 pods):
# quota: {cpu: 500m, memory: 2Gi}
# S3 buckets in UpCloud Object Storage (bucketName("uploads") in the SDK);
# ":public" lets anyone read one, at https://<bucket>--<app>.gronblom.dev/:
# buckets: [uploads, assets:public]
# A Postgres database (DATABASE_URL), backed up and restorable to a point in
# time; queue functions need it:
# db: true            # or {size: 2Gi, instances: 2}
# The folder the app is built from and mcloud dev runs in, if it isn't this
# one (e.g. the web app in worker/ of a repo whose root is a Go CLI):
# source: worker
`,
	".github/workflows/deploy.yaml": `# Every push to main deploys this app to maxcloud (maxemiliang/maxcloud,
# .github/workflows/run-deploy.yaml): builds the image, writes the
# functions' manifests to deploy/k8s, pins the image and commits; Flux
# applies it.
name: deploy

on:
  push:
    branches: [main]
    # The deploy commit itself only changes deploy/.
    paths-ignore: ["deploy/**"]
  workflow_dispatch: {}

permissions: {}

jobs:
  deploy:
    permissions:
      contents: write # the deploy commit
      id-token: write # pushing to the maxcloud registry, as this repo
    uses: maxemiliang/maxcloud/.github/workflows/run-deploy.yaml@main
    with:
      functions: true
`,
	"deploy/k8s/kustomization.yaml": `# What Flux applies from this repo into the app's namespace. functions.yaml
# is generated from the app's functions on every deploy; list your own
# manifests here too.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
# The deploy workflow replaces app with the image it built, pinned by digest.
images:
  - name: app
`,
}

func initCmd(g *globals) *cobra.Command {
	var lang, deploy string
	var dryRun bool
	c := &cobra.Command{
		Use:   "init [NAME]",
		Short: "Set up this repo as a functions app on maxcloud",
		Long: `Set up the repo in the current directory as a maxcloud functions app
in TypeScript (@max-cloud/functions) or Go (--lang go, the functions
package). In an empty repo it writes a starter app; in an existing Node.js or
Go app it adds only what deploys need and says what's still missing.
Existing files are never overwritten.

NAME (default: the directory's name) becomes the app's namespace and part of
its URLs. The first mcloud deploy registers the app from maxcloud.yaml.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			name := filepath.Base(dir)
			if len(args) > 0 {
				name = args[0]
			}
			validName := func(n string) error {
				e := registry.Entry{Name: n, Repo: "placeholder/x"}
				return e.Validate()
			}
			u := g.UI()
			// On a terminal, ask for what wasn't passed.
			if len(args) == 0 {
				def := name
				if validName(def) != nil {
					def = ""
				}
				if name, err = u.Input("App name (its namespace, and part of its URLs)", def, validName); err != nil {
					return err
				}
			}
			_, hasGo := os.Stat("go.mod")
			_, hasNode := os.Stat("package.json")
			if !cmd.Flags().Changed("lang") && hasGo != nil && hasNode != nil {
				if lang, err = u.Select("Language", []ui.Option{
					{Label: "TypeScript  (@max-cloud/functions)", Value: "node"},
					{Label: "Go          (github.com/maxemiliang/maxcloud/functions)", Value: "go"},
				}, lang); err != nil {
					return err
				}
			}
			if !cmd.Flags().Changed("deploy") {
				if deploy, err = u.Select("How it deploys", []ui.Option{
					{Label: "GitOps  push to main; GitHub builds, Flux deploys (for anything that matters)", Value: "gitops"},
					{Label: "Direct  mcloud deploy builds and applies from here, no git (experiments)", Value: "direct"},
				}, deploy); err != nil {
					return err
				}
			}
			if err := validName(name); err != nil {
				return fmt.Errorf("%w (pass a name: mcloud init NAME)", err)
			}
			if u.Fancy() && len(args) == 0 {
				langName := map[string]string{"node": "TypeScript", "go": "Go"}[lang]
				if hasGo == nil {
					langName = "Go (go.mod)"
				} else if hasNode == nil {
					langName = "TypeScript (package.json)"
				}
				u.Success("%s %s", u.Bold(name), u.Dim("· "+langName+" · "+map[string]string{"gitops": "GitOps", "direct": "direct deploys"}[deploy]))
			}

			files := map[string]string{}
			for p, content := range appFiles {
				files[p] = content
			}
			repo := originRepo()
			if _, err := os.Stat("go.mod"); err == nil {
				lang = "go"
				u.Note("go.mod: register your functions in main and call functions.Start() (go get github.com/maxemiliang/maxcloud/functions)")
			} else if pkg, err := os.ReadFile("package.json"); err == nil {
				lang = "node"
				for _, w := range checkPackageJSON(pkg, latestSDK(cmd.Context())) {
					u.Warn("package.json: %s", w)
				}
				if strings.Contains(string(pkg), `"build"`) {
					files["project.toml"] = starter["project.toml"]
				}
			} else {
				var start map[string]string
				switch lang {
				case "node":
					start = starter
				case "go":
					start = goStarter
				default:
					return fmt.Errorf("--lang %q: node or go", lang)
				}
				for p, content := range start {
					files[p] = content
				}
			}
			switch deploy {
			case "", "gitops":
			case "direct":
				// Nothing deploys from git: no workflow, no manifests directory.
				delete(files, ".github/workflows/deploy.yaml")
				delete(files, "deploy/k8s/kustomization.yaml")
				files["maxcloud.yaml"] += "# Deployed with `mcloud deploy` straight from this directory, not from git.\ndeploy: direct\n"
			default:
				return fmt.Errorf("--deploy %q: gitops or direct", deploy)
			}
			module := name
			if repo != "" {
				module = "github.com/" + repo
			}

			sdk := ""
			if lang == "node" {
				sdk = latestSDK(cmd.Context())
			}
			verb := "wrote"
			if dryRun {
				verb = "writes"
			}
			for _, p := range sortedKeys(files) {
				if _, err := os.Stat(p); err == nil {
					u.Printf("  %s %s", u.Dim("·"), u.Dim(p+" (exists)"))
					continue
				}
				if !dryRun {
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						return err
					}
					content := strings.NewReplacer("%[1]s", name, "%[2]s", module, "%[3]s", sdk).Replace(files[p])
					if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
						return err
					}
				}
				if u.Fancy() {
					u.Printf("  %s %s", u.Green("+"), p)
				} else {
					u.Printf("  %s %s", verb, p)
				}
			}

			var next [][2]string
			if lang == "go" {
				next = append(next, [2]string{"go get github.com/maxemiliang/maxcloud/functions@latest && go mod tidy", ""})
			} else {
				next = append(next, [2]string{"npm install", "creates package-lock.json, which the build needs"})
				next = append(next, [2]string{"mcloud dev", "run it here"})
			}
			if deploy == "direct" {
				next = append(next, [2]string{"mcloud deploy", "registers the app, builds here, applies"})
			} else {
				next = append(next, [2]string{`git add -A && git commit -m "maxcloud app"`, ""})
				if repo == "" {
					next = append(next, [2]string{"gh repo create --source . --push", "its GitHub repo"})
				}
				next = append(next, [2]string{"mcloud deploy", "registers the app, pushes, follows the build and rollout"})
			}
			u.Println()
			u.Printf("%s %s %s", u.Green("●"), u.Bold(name), u.Dim("is ready to go. Next:"))
			w := 0
			for _, n := range next {
				w = max(w, len(n[0]))
			}
			for _, n := range next {
				line := "  " + u.Accent(n[0])
				if n[1] != "" {
					line += strings.Repeat(" ", w-len(n[0])+2) + u.Dim("# "+n[1])
				}
				u.Printf("%s", line)
			}
			return nil
		},
	}
	c.Flags().StringVar(&lang, "lang", "node", "language of a new app: node (TypeScript) or go; an existing go.mod or package.json decides by itself")
	c.Flags().StringVar(&deploy, "deploy", "gitops", "gitops (push to deploy) or direct (mcloud deploy builds and applies from here, no git)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show the files it would write, write none")
	return c
}

// checkPackageJSON says what an existing app's package.json still needs.
// latest is the SDK's newest version ("" if unknown).
func checkPackageJSON(data []byte, latest string) []string {
	var p struct {
		Type         string            `json:"type"`
		Main         string            `json:"main"`
		Scripts      map[string]string `json:"scripts"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return []string{"can't read it: " + err.Error()}
	}
	var out []string
	if r, ok := p.Dependencies["@max-cloud/functions"]; !ok {
		out = append(out, "add the SDK: npm install @max-cloud/functions")
	} else if latest != "" && !caretAllows(r, latest) {
		out = append(out, fmt.Sprintf("the SDK is held at %s, which leaves out %s: npm install @max-cloud/functions@latest", r, latest))
	}
	if s := p.Scripts["start"]; s != "maxcloud-functions" {
		if s == "" {
			out = append(out, `add "start": "maxcloud-functions" to scripts (the container runs npm start)`)
		} else {
			out = append(out, fmt.Sprintf(`scripts.start is %q; the container runs it, so it should be "maxcloud-functions"`, s))
		}
	}
	if p.Main == "" {
		out = append(out, `set "main" to the module that exports your functions (e.g. dist/index.js)`)
	}
	return out
}

var remoteRe = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

// originRepo is <owner>/<repo> of the origin remote, if it's on GitHub.
func originRepo() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	if m := remoteRe.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
		return m[1]
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
