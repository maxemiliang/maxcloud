package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

func runCmd(g *globals) *cobra.Command {
	var app string
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "run FUNCTION",
		Short: "Run a scheduled function now, and show its output",
		Long: `Start a run of a scheduled function (its CronJob) now, outside its
schedule, follow its logs, and say whether it succeeded. The run is
removed an hour after it ends.`,
		Example: `  mcloud run nightly
  mcloud run nightly --app shop`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(nonEmpty(app))
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			job, err := k.RunCronJob(ctx, name, args[0])
			if err != nil {
				return fmt.Errorf("%s isn't a scheduled function of %s? %w", args[0], name, err)
			}
			fmt.Fprintf(g.errOut, "Started %s (Job %s/%s).\n", args[0], name, job)
			if err := k.FollowJob(ctx, name, job, g.out); err != nil {
				return err
			}
			fmt.Fprintf(g.errOut, "%s succeeded.\n", args[0])
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "the app (default: maxcloud.yaml's, or $MCLOUD_APP)")
	c.Flags().DurationVar(&timeout, "timeout", time.Hour, "stop following after this long (the run carries on)")
	return c
}

func callCmd(g *globals) *cobra.Command {
	var app, method, data string
	var headers []string
	c := &cobra.Command{
		Use:   "call FUNCTION [PATH]",
		Short: "Send a request to an HTTP function, private ones too",
		Long: `Send one HTTP request to a function and print the response (status,
headers, body). It's sent from a short-lived pod in the app's namespace, so
private (cluster-local) functions answer it too, and a function scaled to
zero starts for it.`,
		Example: `  mcloud call api /health
  mcloud call api /items -X POST -d '{"name":"x"}' -H 'Content-Type: application/json'
  mcloud call api /items -d @item.json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(nonEmpty(app))
			if err != nil {
				return err
			}
			path := "/"
			if len(args) == 2 {
				path = args[1]
				if !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
			}
			if strings.HasPrefix(data, "@") {
				b, err := os.ReadFile(data[1:])
				if err != nil {
					return err
				}
				data = string(b)
			}
			if method == "" {
				method = "GET"
				if data != "" {
					method = "POST"
				}
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			return k.Call(ctx, name, kube.Request{
				Method:  strings.ToUpper(method),
				URL:     fmt.Sprintf("http://%s.%s.svc.cluster.local%s", args[0], name, path),
				Headers: headers,
				Body:    data,
			}, g.out)
		},
	}
	f := c.Flags()
	f.StringVar(&app, "app", "", "the app (default: maxcloud.yaml's, or $MCLOUD_APP)")
	f.StringVarP(&method, "request", "X", "", "HTTP method (default GET, or POST with -d)")
	f.StringVarP(&data, "data", "d", "", "request body; @FILE reads it from a file")
	f.StringArrayVarP(&headers, "header", "H", nil, `a header, "Name: value" (repeatable)`)
	return c
}

// nonEmpty is args for appName: [v] if v is set, else none.
func nonEmpty(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}
