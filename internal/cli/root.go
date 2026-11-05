// Package cli is the mcloud command line.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"sigs.k8s.io/yaml"

	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

// Version is set at build time (-ldflags "-X ...cli.Version=v0.1.0").
var Version = ""

// ProjectFile, in an app's repo, names the app so commands run there don't
// need it spelled out.
const ProjectFile = "maxcloud.yaml"

type globals struct {
	kubeconfig  string
	kubeContext string
	registry    string // <owner>/<repo> of the homelab repo
	branch      string

	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// New builds the root command.
func New() *cobra.Command {
	g := &globals{in: os.Stdin, out: os.Stdout, errOut: os.Stderr}
	root := &cobra.Command{
		Use:   "mcloud",
		Short: "Deploy and run apps on maxcloud, the homelab's run platform",
		Long: `mcloud registers apps on maxcloud (Knative on the homelab cluster) and
shows how they're doing.

An app is an entry in the homelab repo's app registry (apps/maxcloud): its
namespace, quota and a Flux reconciler that applies the app's own repo.
Registering, changing and removing apps are commits to that repo, which Flux
deploys within a minute.

Cluster access uses $MCLOUD_KUBECONFIG, else the usual $KUBECONFIG.
GitHub access uses $GH_TOKEN, else the GitHub CLI's login.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version(),
	}
	f := root.PersistentFlags()
	f.StringVar(&g.kubeconfig, "kubeconfig", "", "kubeconfig file (default $MCLOUD_KUBECONFIG, else $KUBECONFIG)")
	f.StringVar(&g.kubeContext, "context", "", "kubeconfig context")
	f.StringVar(&g.registry, "registry", envOr("MCLOUD_REGISTRY", "maxemiliang/homelab"), "GitHub repo holding the app registry ($MCLOUD_REGISTRY)")
	f.StringVar(&g.branch, "registry-branch", "main", "branch of the registry repo")
	_ = f.MarkHidden("registry-branch")
	_ = f.MarkHidden("registry-dir")

	root.AddCommand(initCmd(g), deployCmd(g), appsCmd(g), statusCmd(g), logsCmd(g), generateCmd(g), queueCmd(g), secretsCmd(g), dbCmd(g), loginCmd(g), logoutCmd(g), credentialCmd(g), kubeconfigCmd(g))
	return root
}

func version() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func (g *globals) kube() (*kube.Client, error) { return kube.New(g.kubeconfig, g.kubeContext) }

func (g *globals) github() (*github.Client, error) {
	t, err := github.Token()
	if err != nil {
		return nil, err
	}
	return github.New(t), nil
}

// project is an app repo's maxcloud.yaml: the whole app's definition, like
// a wrangler.toml. mcloud deploy keeps the app's registry entry in line with
// it; fields left out keep what the entry has (or the defaults).
type project struct {
	App    string `json:"app"`
	Deploy string `json:"deploy"` // "direct" for direct deploys
	Image  string `json:"image"`  // image repository for direct deploys

	// GitOps source: the repo (default: the origin remote), manifests
	// directory and branch.
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"`

	Quota   *projectQuota `json:"quota"`
	Buckets []string      `json:"buckets"` // "uploads", "assets:public"
	DB      *projectDB    `json:"db"`      // db: true, or db: {size: 2Gi, instances: 2}

	Dir string `json:"-"` // where the file is: the repo's root
}

type projectQuota struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	Pods   string `json:"pods"`
}

// projectDB is the app's database: `db: true`, `db: false`, or settings.
type projectDB struct {
	Enabled   bool
	Size      string
	Instances string
}

func (d *projectDB) UnmarshalJSON(b []byte) error {
	var on bool
	if err := json.Unmarshal(b, &on); err == nil {
		d.Enabled = on
		return nil
	}
	var v struct {
		Size      string `json:"size"`
		Instances any    `json:"instances"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("db: true, false, or {size: 2Gi, instances: 2}")
	}
	d.Enabled, d.Size = true, v.Size
	if v.Instances != nil {
		d.Instances = fmt.Sprint(v.Instances)
	}
	return nil
}

// loadProject finds maxcloud.yaml in the current directory or a parent.
func loadProject() (*project, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for {
		file := filepath.Join(dir, ProjectFile)
		data, err := os.ReadFile(file)
		if err == nil {
			var p project
			if err := yaml.Unmarshal(data, &p); err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			if p.App == "" {
				return nil, fmt.Errorf("%s has no app: field", file)
			}
			p.Dir = dir
			return &p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, errors.New("which app? Pass its name, or run this in a repo with a maxcloud.yaml")
		}
		dir = parent
	}
}

// appName is the first argument, else $MCLOUD_APP, else the app named in
// maxcloud.yaml in the current directory or a parent.
func appName(args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if v := os.Getenv("MCLOUD_APP"); v != "" {
		return v, nil
	}
	p, err := loadProject()
	if err != nil {
		return "", err
	}
	return p.App, nil
}

// confirm asks a yes/no question on the terminal.
func (g *globals) confirm(question string) (bool, error) {
	if !isTerminal(g.in) {
		return false, errors.New("not a terminal: pass --yes to go ahead")
	}
	fmt.Fprintf(g.out, "%s [y/N] ", question)
	ans, err := bufio.NewReader(g.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes", nil
}

// typeToConfirm makes the user type want.
func (g *globals) typeToConfirm(prompt, want string) (bool, error) {
	if !isTerminal(g.in) {
		return false, errors.New("not a terminal: pass --yes to go ahead")
	}
	fmt.Fprintf(g.out, "%s ", prompt)
	ans, err := bufio.NewReader(g.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return strings.TrimSpace(ans) == want, nil
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
