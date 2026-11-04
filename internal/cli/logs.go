package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

func logsCmd(g *globals) *cobra.Command {
	var o kube.LogOptions
	c := &cobra.Command{
		Use:   "logs [APP [NAME]]",
		Short: "Show an app's logs",
		Long: `Show the logs of an app's pods: all of them, or those of one service,
Deployment, CronJob or ScaledJob (NAME). Only running pods and recently
finished ones have logs; older logs are in Grafana (Loki).

With -f it follows them, including pods that start later, so it also shows a
service waking up from zero.`,
		Example: `  mcloud logs shop            # every pod of the app
  mcloud logs shop web -f     # follow the web service
  mcloud logs -f              # in an app's repo, with a maxcloud.yaml`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			o.Namespace = name
			if len(args) == 2 {
				o.Name = args[1]
			}
			o.Color = isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == ""
			k, err := g.kube()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return k.Logs(ctx, o, g.out, g.errOut)
		},
	}
	f := c.Flags()
	f.BoolVarP(&o.Follow, "follow", "f", false, "keep streaming, including pods that start later")
	f.DurationVar(&o.Since, "since", 0, "only lines newer than this, e.g. 10m")
	f.Int64Var(&o.Tail, "tail", 0, "only the last N lines per container")
	f.StringVarP(&o.Selector, "selector", "l", "", "pods matching this label selector instead of NAME")
	f.BoolVar(&o.AllContainers, "all-containers", false, "include Knative's queue-proxy sidecar")
	return c
}
