package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
)

func generateCmd(g *globals) *cobra.Command {
	var from, dir, image string
	c := &cobra.Command{
		Use:   "generate",
		Short: "Write deploy/k8s from the app's functions",
		Long: `Write the manifests for the app's functions into deploy/k8s:
functions.yaml (rewritten every time) and the kustomization listing it.
Queue functions need the app's database: db: true in maxcloud.yaml.

The deploy workflow runs this on every push, from the image it built. Run it
locally to see what a deploy would change: it asks the built app
(npx maxcloud-functions --describe), so build the app first.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var data []byte
			var err error
			switch from {
			case "":
				data, err = describeLocal()
			case "-":
				data, err = io.ReadAll(g.in)
			default:
				data, err = os.ReadFile(from)
			}
			if err != nil {
				return err
			}
			d, err := functions.Parse(data)
			if err != nil {
				return err
			}
			app, _ := appName(nil)
			changed, err := functions.WriteDir(dir, d, functions.Options{Image: image, App: app})
			if err != nil {
				return err
			}
			for _, f := range d.Functions {
				fmt.Fprintf(g.errOut, "%-9s %s\n", f.Kind, f.Name)
			}
			if len(changed) == 0 {
				fmt.Fprintf(g.errOut, "%s is up to date.\n", dir)
			}
			for _, p := range changed {
				fmt.Fprintf(g.errOut, "wrote %s\n", p)
			}
			if _, err := os.Stat(filepath.Join(dir, functions.LegacyDatabaseFile)); d.NeedsDatabase() && err != nil {
				hint := functions.NeedsDatabaseHint(app)
				if os.Getenv("GITHUB_ACTIONS") == "true" {
					// Shown on the workflow run's summary page.
					fmt.Fprintln(g.errOut, "::notice title=Queue functions need a database::"+hint)
				} else {
					fmt.Fprintln(g.errOut, "note: "+hint)
				}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&from, "from", "", "read the description from this file, or - for stdin, instead of asking the app")
	f.StringVar(&dir, "dir", "deploy/k8s", "manifests directory")
	f.StringVar(&image, "image", "app", "image name in the manifests, which the deploy workflow replaces")
	return c
}

// describeLocal asks the app in the current directory for its functions.
func describeLocal() ([]byte, error) {
	cmd := exec.Command("npx", "--no-install", "maxcloud-functions", "--describe")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("maxcloud-functions --describe: %v\n%s(is @max-cloud/functions installed, and the app built?)", err, stderr.String())
	}
	return out, nil
}
