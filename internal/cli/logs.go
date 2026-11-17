package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

func logsCmd(g *globals) *cobra.Command {
	var o kube.LogOptions
	var source, since, until string
	c := &cobra.Command{
		Use:   "logs [APP [NAME]]",
		Short: "Show an app's logs",
		Long: `Show the logs of an app's pods: all of them, or those of one service,
Deployment, CronJob or ScaledJob (NAME).

They come from one of two places (--source):

  - pods: the pods running now and those recently finished, from the
    cluster. A service that scaled to zero has none.
  - loki: every pod's, gone ones included, from the platform's Loki (kept
    30 days). --since and --until pick the time, --tail the newest lines.
    The platform answers only the app's owners and admins.

The default, auto, reads Loki when --since or --until is given, else the
pods; if the platform doesn't offer Loki, it says so and reads the pods.

With -f it follows them, including pods that start later, so it also shows a
service waking up from zero. After --since it first prints what Loki has,
then follows the pods (a line where the two meet may show twice).`,
		Example: `  mcloud logs shop                          # every pod of the app, now
  mcloud logs shop web -f                   # follow the web service
  mcloud logs --since 2h --timestamps       # the last two hours, from Loki
  mcloud logs shop nightly --since 2026-11-16T22:00:00Z --until 2026-11-16T23:00:00Z
  mcloud logs -f                            # in an app's repo, with a maxcloud.yaml`,
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

			if since != "" {
				t, err := parseWhen("--since", since)
				if err != nil {
					return err
				}
				o.Since = time.Since(t)
			}
			var end time.Time
			if until != "" {
				if end, err = parseWhen("--until", until); err != nil {
					return err
				}
			}
			switch source {
			case "pods":
				if until != "" {
					return errors.New("--until needs Loki (--source loki): the pods only have their own logs up to now")
				}
			case "loki", "auto":
				if source == "auto" && o.Since == 0 && until == "" {
					break
				}
				if o.Selector != "" {
					if source == "loki" {
						return errors.New("--selector works with the pods only: Loki knows pods by name")
					}
					break
				}
				api, err := g.appAPI(ctx, k, appapi.Logs)
				if err != nil {
					if source == "loki" || until != "" {
						return fmt.Errorf("%s", apiHint(err, "older logs are in Grafana (Loki)"))
					}
					g.UI().Note("%s", apiHint(err, "showing the pods' logs instead"))
					break
				}
				last, err := g.lokiLogs(ctx, k, api, o, end)
				if err != nil || !o.Follow {
					return err
				}
				// Carry on with the pods, after Loki's last line (Loki is a
				// few seconds behind; the pods have those).
				o.Since, o.Tail = time.Since(last).Truncate(time.Second)+time.Second, 0
			default:
				return fmt.Errorf("--source %q: auto, loki or pods", source)
			}
			return k.Logs(ctx, o, g.out, g.errOut)
		},
	}
	f := c.Flags()
	f.BoolVarP(&o.Follow, "follow", "f", false, "keep streaming, including pods that start later")
	f.StringVar(&since, "since", "", "only lines newer than this: a duration ago (10m, 48h) or a time (2026-11-16T22:00:00Z, \"2026-11-16 22:00\"); from Loki unless --source pods")
	f.StringVar(&until, "until", "", "only lines older than this, from Loki: a duration ago (30m) or a time (2026-11-16T23:00:00Z, \"2026-11-16 23:00\")")
	f.Int64Var(&o.Tail, "tail", 0, "only the last N lines per container (from Loki: the last N lines in all; default 1000, at most 5000)")
	f.StringVarP(&o.Selector, "selector", "l", "", "pods matching this label selector instead of NAME (pods only)")
	f.BoolVar(&o.AllContainers, "all-containers", false, "include Knative's queue-proxy sidecar")
	f.BoolVar(&o.Timestamps, "timestamps", false, "start each line with its time")
	f.StringVar(&source, "source", "auto", "where the logs come from: auto, loki or pods (see above)")
	return c
}

// lokiLogs prints an app's logs from the platform's Loki: from o.Since ago
// (an hour if unset) until end (now if zero), the newest o.Tail lines.
// Returns the time of the last line (the start of the range if none).
func (g *globals) lokiLogs(ctx context.Context, k *kube.Client, api *appapi.Client, o kube.LogOptions, end time.Time) (time.Time, error) {
	pod, err := k.LogPodPattern(ctx, o.Namespace, o.Name)
	if err != nil {
		return time.Time{}, err
	}
	q := appapi.LogQuery{Until: end, Pod: pod, AllContainers: o.AllContainers, Limit: int(o.Tail)}
	if o.Since > 0 {
		q.Since = time.Now().Add(-o.Since)
	}
	if q.Limit > 5000 {
		q.Limit = 5000
	}
	resp, err := api.Logs(ctx, o.Namespace, q)
	if err != nil {
		return time.Time{}, err
	}
	if len(resp.Entries) == 0 {
		if !o.Follow {
			g.UI().Note("no lines%s in %s from %s to %s", forName(o.Name), o.Namespace, resp.Start.Local().Format("2006-01-02 15:04"), resp.End.Local().Format("2006-01-02 15:04"))
		}
		return resp.Start, nil
	}
	if resp.Limited {
		g.UI().Note("the newest %d lines; --tail (up to 5000) or a shorter range for others", len(resp.Entries))
	}
	containers := map[string]map[string]bool{}
	for _, e := range resp.Entries {
		if containers[e.Pod] == nil {
			containers[e.Pod] = map[string]bool{}
		}
		containers[e.Pod][e.Container] = true
	}
	p := kube.NewLogPrinter(g.out, g.errOut, o.Color)
	for _, e := range resp.Entries {
		prefix := e.Pod
		if len(containers[e.Pod]) > 1 {
			prefix += "/" + e.Container
		}
		line := e.Line
		if o.Timestamps {
			line = e.Time.Format(time.RFC3339Nano) + " " + line
		}
		p.Line(prefix, line)
	}
	return resp.Entries[len(resp.Entries)-1].Time, nil
}

func forName(name string) string {
	if name == "" {
		return ""
	}
	return " of " + name
}

// parseWhen reads a flag's time as mcloud db restore's --at does: a
// duration ago, or a time.
func parseWhen(flag, s string) (time.Time, error) {
	t, err := parseAt(s, time.Now())
	if err != nil {
		return t, errors.New(strings.Replace(err.Error(), "--at", flag, 1))
	}
	return t, nil
}
