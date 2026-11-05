package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// The SDKs' queue table (packages/functions/src/queue.ts, functions/queue.go).
const jobsTable = "maxcloud_jobs"

var jobStates = []string{"pending", "running", "failed", "done"}

func queueCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "queue",
		Short: "Look at and retry an app's queued jobs",
		Long: `The jobs of an app's queue functions live in its Postgres database (table
maxcloud_jobs). These commands run psql in the database's primary pod, with
your kubeconfig.`,
	}
	c.AddCommand(queueLsCmd(g), queueRetryCmd(g))
	return c
}

type queueFlags struct {
	cluster, queue string
}

func (f *queueFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.cluster, "db", "db", "the app's database (CloudNativePG cluster) holding the queue")
	c.Flags().StringVar(&f.queue, "queue", "", "only this queue")
}

func queueLsCmd(g *globals) *cobra.Command {
	var f queueFlags
	var state string
	var limit int
	c := &cobra.Command{
		Use:   "ls [APP]",
		Short: "Jobs per queue and state, and the latest jobs in one state",
		Example: `  mcloud queue ls shop                    # counts, and the latest failed jobs
  mcloud queue ls shop --state pending --queue mail`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args)
			if err != nil {
				return err
			}
			if state != "all" && !contains(jobStates, state) {
				return fmt.Errorf("--state %q: one of %s, or all", state, strings.Join(jobStates, ", "))
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			vars := map[string]string{"queue": f.queue, "state": state, "lim": strconv.Itoa(limit)}
			script := tableCheck + `
SELECT queue,
       count(*) FILTER (WHERE state = 'pending'),
       count(*) FILTER (WHERE state = 'running'),
       count(*) FILTER (WHERE state = 'failed'),
       count(*) FILTER (WHERE state = 'done')
FROM ` + jobsTable + `
WHERE :'queue' = '' OR queue = :'queue'
GROUP BY queue ORDER BY queue;
SELECT '---';
SELECT id, queue, state, attempts, to_char(created_at, 'YYYY-MM-DD HH24:MI'),
       coalesce(replace(left(last_error, 100), E'\n', ' '), '')
FROM ` + jobsTable + `
WHERE (:'queue' = '' OR queue = :'queue') AND (:'state' = 'all' OR state = :'state')
ORDER BY id DESC LIMIT :lim;
`
			out, err := k.Psql(cmd.Context(), name, f.cluster, script, vars)
			if err != nil {
				return err
			}
			counts, jobs, _ := strings.Cut(out, "---\n")
			w := tabwriter.NewWriter(g.out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "QUEUE\tPENDING\tRUNNING\tFAILED\tDONE")
			rows := 0
			for _, line := range lines(counts) {
				fmt.Fprintln(w, line)
				rows++
			}
			w.Flush()
			if rows == 0 {
				fmt.Fprintln(g.out, "(no jobs)")
				return nil
			}
			fmt.Fprintf(g.out, "\nLatest %s jobs:\n", state)
			w = tabwriter.NewWriter(g.out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tQUEUE\tSTATE\tATTEMPTS\tCREATED\tLAST ERROR")
			n := 0
			for _, line := range lines(jobs) {
				fmt.Fprintln(w, line)
				n++
			}
			w.Flush()
			if n == 0 {
				fmt.Fprintln(g.out, "(none)")
			} else if state == "failed" {
				fmt.Fprintf(g.out, "\nRetry: mcloud queue retry %s <id>... (or --failed)\n", name)
			}
			return nil
		},
	}
	f.bind(c)
	c.Flags().StringVar(&state, "state", "failed", "jobs to list: pending, running, failed, done or all")
	c.Flags().IntVar(&limit, "limit", 20, "how many jobs to list")
	return c
}

func queueRetryCmd(g *globals) *cobra.Command {
	var f queueFlags
	var allFailed bool
	c := &cobra.Command{
		Use:   "retry [APP] [ID...]",
		Short: "Run failed jobs again",
		Long: `Puts failed jobs back in their queue with their attempts reset, so they
get the full number of tries again. Only failed jobs: pending and running
ones are left alone. Inside an app's repo, the app comes from maxcloud.yaml
and every argument is a job ID.`,
		Example: `  mcloud queue retry shop 41 42
  mcloud queue retry shop --failed --queue mail`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The app is optional: a first argument that isn't a number.
			var appArgs []string
			if len(args) > 0 {
				if _, err := strconv.ParseInt(args[0], 10, 64); err != nil {
					appArgs, args = args[:1], args[1:]
				}
			}
			name, err := appName(appArgs)
			if err != nil {
				return err
			}
			var ids []string
			for _, a := range args {
				if _, err := strconv.ParseInt(a, 10, 64); err != nil {
					return fmt.Errorf("%q isn't a job ID", a)
				}
				ids = append(ids, a)
			}
			if len(ids) == 0 && !allFailed {
				return fmt.Errorf("which jobs? Give their IDs (mcloud queue ls %s), or --failed for all failed ones", name)
			}
			if len(ids) > 0 && allFailed {
				return fmt.Errorf("either job IDs or --failed, not both")
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			// IDs are checked numbers; the array literal is a psql variable.
			vars := map[string]string{"queue": f.queue, "ids": "{" + strings.Join(ids, ",") + "}"}
			script := tableCheck + `
WITH retried AS (
  UPDATE ` + jobsTable + `
  SET state = 'pending', attempts = 0, run_after = now(), locked_until = NULL, last_error = NULL, done_at = NULL
  WHERE state = 'failed'
    AND (:'queue' = '' OR queue = :'queue')
    AND (cardinality(:'ids'::bigint[]) = 0 OR id = ANY(:'ids'::bigint[]))
  RETURNING id
)
SELECT count(*) FROM retried;
`
			out, err := k.Psql(cmd.Context(), name, f.cluster, script, vars)
			if err != nil {
				return err
			}
			n := strings.TrimSpace(out)
			fmt.Fprintf(g.out, "%s failed job(s) back in the queue.\n", n)
			if len(ids) > 0 && n != strconv.Itoa(len(ids)) {
				fmt.Fprintln(g.out, "Some of those IDs aren't failed jobs (or don't exist): see mcloud queue ls --state all.")
			}
			return nil
		},
	}
	f.bind(c)
	c.Flags().BoolVar(&allFailed, "failed", false, "retry every failed job (of --queue, if given)")
	return c
}

// tableCheck stops the script with a clear error when the app has never
// used a queue (psql exits on it: ON_ERROR_STOP).
const tableCheck = `DO $$ BEGIN
  IF to_regclass('` + jobsTable + `') IS NULL THEN
    RAISE EXCEPTION 'no table ` + jobsTable + ` in this database: does the app have queue functions, and is --db right?';
  END IF;
END $$;
`

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
