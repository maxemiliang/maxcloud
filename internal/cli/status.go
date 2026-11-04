package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

func statusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status [APP]",
		Short: "Show an app's deploy, services, workloads, databases and problems",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			st, err := k.AppStatus(cmd.Context(), name)
			if err != nil {
				return err
			}
			if st.Entry == nil && !st.Applied && st.Services == nil && st.Workloads == nil {
				return fmt.Errorf("no app %s on the cluster; `mcloud apps list` shows the registered ones", name)
			}
			printStatus(g.out, st, g.registry)
			return nil
		},
	}
}

func printStatus(w io.Writer, st *kube.Status, registryRepo string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	defer tw.Flush()

	fmt.Fprintf(tw, "App\t%s\n", st.Name)
	if st.Entry != nil {
		fmt.Fprintf(tw, "Source\t%s\n", source(st.Entry, registryRepo))
	} else {
		fmt.Fprintf(tw, "Source\tnot in the registry\n")
	}
	if st.Source != nil && !st.Source.Ready() {
		fmt.Fprintf(tw, "Fetch\t%s %s\n", mark(st.Source.Status, true), st.Source.Message)
	}
	if st.Applied {
		fmt.Fprintf(tw, "Deploy\t%s %s\n", mark(st.Deploy.Status, true), firstLine(st.Deploy.Message))
	} else {
		fmt.Fprintf(tw, "Deploy\t%s\n", "not created yet (Flux picks up a new entry within a minute)")
	}
	if len(st.Quota) > 0 {
		var q []string
		for _, l := range st.Quota {
			q = append(q, fmt.Sprintf("%s %s/%s", strings.TrimPrefix(l.Resource, "requests."), l.Used, l.Hard))
		}
		fmt.Fprintf(tw, "Quota\t%s\n", strings.Join(q, ", "))
	}

	if len(st.Services) > 0 {
		fmt.Fprintf(tw, "\nSERVICE\tURL\tREADY\tREVISION\tPODS\n")
		for _, s := range st.Services {
			url := s.URL
			if !s.Public {
				url += " (private)"
			}
			ready := mark(s.Ready.Status, true)
			if !s.Ready.Ready() && s.Ready.Message != "" {
				ready += " " + truncate(s.Ready.Message, 60)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", s.Name, url, ready, s.Revision, s.Pods)
		}
	}
	if len(st.Workloads) > 0 {
		fmt.Fprintf(tw, "\nWORKLOAD\tKIND\tSTATE\n")
		for _, wl := range st.Workloads {
			fmt.Fprintf(tw, "%s\t%s\t%s %s\n", wl.Name, wl.Kind, mark(boolStatus(wl.Ready), true), wl.State)
		}
	}
	if len(st.Databases) > 0 {
		fmt.Fprintf(tw, "\nDATABASE\tINSTANCES\tSTATE\n")
		for _, d := range st.Databases {
			fmt.Fprintf(tw, "%s\t%s\t%s %s\n", d.Name, d.Ready, mark(boolStatus(d.Healthy), true), d.Phase)
		}
	}
	if len(st.Problems) > 0 {
		fmt.Fprintf(tw, "\nPROBLEMS\n")
		for _, p := range st.Problems {
			fmt.Fprintf(tw, "  %s\n", p)
		}
	}
}

func boolStatus(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// mark is a one-character summary of a Ready condition.
func mark(status string, exists bool) string {
	switch {
	case !exists:
		return "-"
	case status == "True":
		return "✓"
	case status == "False":
		return "✗"
	}
	return "…"
}

// shortRev turns "refs/heads/main@sha1:2495009b8..." into "main@2495009".
func shortRev(rev string) string {
	if rev == "" {
		return "-"
	}
	ref, sha, ok := strings.Cut(rev, "@sha1:")
	if !ok {
		return truncate(rev, 20)
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return strings.TrimPrefix(ref, "refs/heads/") + "@" + sha
}

func truncate(s string, n int) string {
	s = firstLine(s)
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
