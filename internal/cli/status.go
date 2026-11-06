package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/ui"
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
			printStatus(g.UI(), st)
			return nil
		},
	}
}

func printStatus(u *ui.UI, st *kube.Status) {
	kv := [][2]string{{"App", u.Bold(st.Name)}}
	if st.Entry != nil {
		kv = append(kv, [2]string{"Source", source(st.Entry)})
	} else {
		kv = append(kv, [2]string{"Source", u.Amber("not in the registry")})
	}
	if st.Source != nil && !st.Source.Ready() {
		kv = append(kv, [2]string{"Fetch", u.Mark(st.Source.Status, true) + " " + firstLine(st.Source.Message)})
		if st.Source.NoRepoAccess() {
			kv = append(kv, [2]string{"", "give Flux's GitHub App access to the repo: " + u.Link(repoAccessURL(st))})
		}
	}
	switch {
	case st.Entry != nil && st.Entry.Direct():
		kv = append(kv, [2]string{"Deploy", "direct: applied by mcloud deploy, nothing from git"})
	case st.Applied:
		kv = append(kv, [2]string{"Deploy", u.Mark(st.Deploy.Status, true) + " " + firstLine(st.Deploy.Message)})
	default:
		kv = append(kv, [2]string{"Deploy", u.Amber("not created yet") + " (Flux picks up a new entry within a minute)"})
	}
	if len(st.Quota) > 0 {
		var q []string
		for _, l := range st.Quota {
			q = append(q, fmt.Sprintf("%s %s/%s", strings.TrimPrefix(l.Resource, "requests."), l.Used, l.Hard))
		}
		kv = append(kv, [2]string{"Quota", strings.Join(q, ", ")})
	}
	u.KV(kv)

	if len(st.Services) > 0 {
		var rows [][]string
		for _, s := range st.Services {
			url := u.Link(s.URL)
			if !s.Public {
				url += u.Dim(" (private)")
			}
			ready := u.Mark(s.Ready.Status, true)
			if !s.Ready.Ready() && s.Ready.Message != "" {
				ready += " " + truncate(s.Ready.Message, 60)
			}
			rows = append(rows, []string{u.Accent(s.Name), url, ready, u.Dim(s.Revision), fmt.Sprint(s.Pods)})
		}
		u.Println()
		u.Table([]string{"Service", "URL", "Ready", "Revision", "Pods"}, rows)
	}
	if len(st.Workloads) > 0 {
		var rows [][]string
		for _, wl := range st.Workloads {
			rows = append(rows, []string{u.Accent(wl.Name), wl.Kind, u.Mark(boolStatus(wl.Ready), true) + " " + wl.State})
		}
		u.Println()
		u.Table([]string{"Workload", "Kind", "State"}, rows)
	}
	if len(st.Databases) > 0 {
		var rows [][]string
		for _, d := range st.Databases {
			rows = append(rows, []string{u.Accent(d.Name), d.Ready, u.Mark(boolStatus(d.Healthy), true) + " " + d.Phase})
		}
		u.Println()
		u.Table([]string{"Database", "Instances", "State"}, rows)
	}
	if len(st.Problems) > 0 {
		u.Heading("Problems")
		for _, p := range st.Problems {
			u.Printf("  %s %s", u.Red("•"), p)
		}
	}
}

func boolStatus(b bool) string {
	if b {
		return "True"
	}
	return "False"
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
