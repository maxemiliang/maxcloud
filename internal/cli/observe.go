package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/appapi"
)

func metricsCmd(g *globals) *cobra.Command {
	var window string
	var asJSON bool
	c := &cobra.Command{
		Use:   "metrics [APP]",
		Short: "Show an app's requests, errors, latency, queues and jobs",
		Long: `Show an app's numbers from the platform's Prometheus, the ones the
"Run platform" Grafana dashboard has: per HTTP function (Knative service)
its requests over the window, how many were 5xx, the 95th percentile
latency and its pods now; per queue function the jobs waiting, as KEDA
last read them; and the app's running and failed Jobs.

The platform answers only the app's owners and admins, with your mcloud
login.`,
		Example: `  mcloud metrics                  # the last hour
  mcloud metrics shop --window 7d
  mcloud metrics --json | jq '.services[] | select(.errorRatio > 0.01)'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			api, err := g.appAPI(cmd.Context(), k, appapi.Metrics)
			if err != nil {
				return fmt.Errorf("%s", apiHint(err, "the numbers are on Grafana's \"Run platform\" dashboard"))
			}
			m, err := api.Metrics(cmd.Context(), name, window)
			if err != nil {
				return err
			}
			if asJSON {
				return g.printJSON(m)
			}
			printMetrics(g, m)
			return nil
		},
	}
	c.Flags().StringVar(&window, "window", "1h", "how far back the numbers go: 5m to 15d (e.g. 15m, 24h, 7d)")
	jsonFlag(c, &asJSON)
	return c
}

func printMetrics(g *globals, m *appapi.MetricsResponse) {
	u := g.UI()
	if len(m.Services) == 0 && len(m.Queues) == 0 && m.Jobs.Running == 0 && m.Jobs.Failed == 0 {
		u.Note("no metrics for %s in the last %s", m.App, m.Window)
		return
	}
	if len(m.Services) > 0 {
		var rows [][]string
		for _, s := range m.Services {
			errs := "-"
			if s.ErrorRatio != nil {
				errs = fmt.Sprintf("%.0f (%s)", s.Errors, percent(*s.ErrorRatio))
				if *s.ErrorRatio >= 0.05 {
					errs = u.Red(errs)
				} else if s.Errors > 0 {
					errs = u.Amber(errs)
				}
			}
			p95 := "-"
			if s.P95Seconds != nil {
				p95 = seconds(*s.P95Seconds)
			}
			pods := "-"
			if s.Pods != nil {
				pods = fmt.Sprintf("%.0f", *s.Pods)
			}
			rows = append(rows, []string{u.Accent(s.Name), fmt.Sprintf("%.0f", s.Requests), rate(s.RequestsPerSecond), errs, p95, pods})
		}
		u.Table([]string{"Service", "Requests", "Req/s", "5xx", "p95", "Pods"}, rows)
	}
	if len(m.Queues) > 0 {
		var rows [][]string
		for _, q := range m.Queues {
			rows = append(rows, []string{u.Accent(q.Name), q.Kind, fmt.Sprintf("%.0f", q.Backlog)})
		}
		u.Println()
		u.Table([]string{"Queue", "Kind", "Waiting"}, rows)
	}
	jobs := fmt.Sprintf("%.0f running, %.0f failed", m.Jobs.Running, m.Jobs.Failed)
	if m.Jobs.Failed > 0 {
		jobs += u.Dim(" (mcloud logs <name> for why; mcloud queue ls for queue jobs)")
	}
	u.Println()
	u.KV([][2]string{{"Jobs", jobs}, {"Window", "the last " + m.Window}})
}

func percent(r float64) string {
	if r > 0 && r < 0.001 {
		return "<0.1%"
	}
	return fmt.Sprintf("%.1f%%", r*100)
}

func seconds(s float64) string {
	if s < 1 {
		return fmt.Sprintf("%.0fms", s*1000)
	}
	return fmt.Sprintf("%.2fs", s)
}

func rate(r float64) string {
	switch {
	case r == 0:
		return "0"
	case r < 0.01:
		return "<0.01"
	}
	return fmt.Sprintf("%.2f", r)
}

func alertsCmd(g *globals) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "alerts [APP]",
		Short: "Show the platform's active alerts about an app",
		Long: `Show the platform's alerts about an app that are active now: firing, or
pending (the condition holds, not yet for long enough). They're the
alerts the platform's admins get, picked for the app: 5xx errors of a
service, failed Jobs, crash-looping or Pending pods, a blocked database,
and so on. mcloud status lists them too.

Alerts from the app's own PrometheusRules aren't included: Prometheus
doesn't keep their queries to the app's metrics.

The platform answers only the app's owners and admins, with your mcloud
login. To be told without looking, run mcloud alerts --json on a
schedule (it prints {"alerts": []} when all is well).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			api, err := g.appAPI(cmd.Context(), k, appapi.Alerts)
			if err != nil {
				return fmt.Errorf("%s", apiHint(err, "the admins get the alerts in the meantime"))
			}
			a, err := api.Alerts(cmd.Context(), name)
			if err != nil {
				return err
			}
			if asJSON {
				return g.printJSON(a)
			}
			u := g.UI()
			if len(a.Alerts) == 0 {
				u.Success("No alerts about %s", name)
				return nil
			}
			for i, al := range a.Alerts {
				if i > 0 {
					u.Println()
				}
				u.Printf("%s", alertLine(u, al))
				if al.Description != "" {
					u.Hint("%s", strings.Join(strings.Fields(al.Description), " "))
				}
				var labels []string
				for _, k := range []string{"kn_service_name", "pod", "container", "job_name", "cronjob", "scaledObject"} {
					if v := al.Labels[k]; v != "" {
						labels = append(labels, k+"="+v)
					}
				}
				if al.Value != "" {
					labels = append(labels, "value="+al.Value)
				}
				if len(labels) > 0 {
					u.Hint("%s", strings.Join(labels, " "))
				}
			}
			return nil
		},
	}
	jsonFlag(c, &asJSON)
	return c
}
