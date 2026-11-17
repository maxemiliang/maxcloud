package cli

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// Machine-readable output: every command that lists or shows state takes
// --json and then prints one JSON object on stdout instead of its table
// (notes and warnings still go to stderr). Field names are camelCase and
// stable: new fields may appear, existing ones keep their name and meaning.
// docs/mcloud.md ("JSON output") lists them.

// jsonFlag adds --json to a command.
func jsonFlag(c *cobra.Command, v *bool) {
	c.Flags().BoolVar(v, "json", false, "print JSON instead of a table (fields: docs/mcloud.md, JSON output)")
}

// printJSON writes v as indented JSON to stdout.
func (g *globals) printJSON(v any) error {
	enc := json.NewEncoder(g.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// condJSON is a status condition.
type condJSON struct {
	Status  string `json:"status"` // True, False, Unknown, or "" when there's none yet
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func condOf(c kube.Condition) condJSON {
	return condJSON{Status: c.Status, Reason: c.Reason, Message: c.Message}
}

// appJSON is a registered app, as apps list and status show it.
type appJSON struct {
	Name   string   `json:"name"`
	Mode   string   `json:"mode"` // gitops or direct; "" when not registered
	Repo   string   `json:"repo,omitempty"`
	Path   string   `json:"path,omitempty"`
	Branch string   `json:"branch,omitempty"`
	Owners []string `json:"owners"` // empty: an admin's
	// RegisteredIn: cluster (mcloud), or git (the platform's own apps).
	RegisteredIn string `json:"registeredIn"`
	// Deploy is the app's Flux Kustomization (GitOps apps): whether the
	// last revision applied.
	Deploy *deployJSON `json:"deploy,omitempty"`
	// Sync: the app's pushed maxcloud.yaml and whether the platform took it.
	Sync *syncJSON `json:"sync,omitempty"`
}

type deployJSON struct {
	condJSON
	Revision string `json:"revision,omitempty"`
}

type syncJSON struct {
	Revision string `json:"revision,omitempty"`
	Error    string `json:"error,omitempty"`
}

func appOf(a kube.App) appJSON {
	out := appJSON{Name: a.Name, Owners: []string{}, RegisteredIn: "cluster"}
	if a.InGit {
		out.RegisteredIn = "git"
	}
	if e := a.Entry; e != nil {
		if e.Direct() {
			out.Mode = "direct"
		} else {
			out.Mode = "gitops"
			out.Repo, out.Path, out.Branch = e.Repo, e.Path, e.Branch
			if out.Path == "" {
				out.Path = registry.DefaultPath
			}
			if out.Branch == "" {
				out.Branch = registry.DefaultBranch
			}
		}
		if o := e.OwnerList(); len(o) > 0 {
			out.Owners = o
		}
	}
	if a.Applied {
		out.Deploy = &deployJSON{condJSON: condOf(a.Deploy), Revision: a.Revision}
	}
	if a.SyncError != "" || a.SyncedRevision != "" {
		out.Sync = &syncJSON{Revision: a.SyncedRevision, Error: a.SyncError}
	}
	return out
}

// statusJSON is mcloud status --json.
type statusJSON struct {
	appJSON
	Registered bool          `json:"registered"`
	Fetch      *condJSON     `json:"fetch,omitempty"` // Flux fetching the app's repo
	Quota      []quotaJSON   `json:"quota"`
	Services   []serviceJSON `json:"services"`
	Workloads  []workJSON    `json:"workloads"`
	Databases  []dbJSON      `json:"databases"`
	Buckets    []bucketJSON  `json:"buckets"`
	Domains    []domainJSON  `json:"domains"`
	Problems   []string      `json:"problems"`
	// Alerts are the platform's active alerts about the app; null when
	// they can't be read (an older platform, or not an owner).
	Alerts []appapi.Alert `json:"alerts"`
}

type quotaJSON struct {
	Resource string `json:"resource"`
	Used     string `json:"used"`
	Hard     string `json:"hard"`
}

type serviceJSON struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Public   bool     `json:"public"`
	Ready    condJSON `json:"ready"`
	Revision string   `json:"revision"`
	Pods     int      `json:"pods"`
}

type workJSON struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	State string `json:"state"`
	Ready bool   `json:"ready"`
}

type dbJSON struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Instances string `json:"instances"` // ready/wanted
	Healthy   bool   `json:"healthy"`
}

type bucketJSON struct {
	Name      string   `json:"name"`
	Bucket    string   `json:"bucket"`
	Public    bool     `json:"public"`
	URL       string   `json:"url,omitempty"`
	SizeBytes *int64   `json:"sizeBytes"`
	Objects   *int64   `json:"objects"`
	Ready     condJSON `json:"ready"`
}

type domainJSON struct {
	Host    string   `json:"host"`
	Service string   `json:"service"`
	Proxied bool     `json:"proxied"`
	URL     string   `json:"url,omitempty"`
	Ready   condJSON `json:"ready"`
}

func statusOf(st *kube.Status, alerts []appapi.Alert) statusJSON {
	out := statusJSON{appJSON: appOf(st.App), Registered: st.Entry != nil, Alerts: alerts,
		Quota: []quotaJSON{}, Services: []serviceJSON{}, Workloads: []workJSON{}, Databases: []dbJSON{},
		Buckets: []bucketJSON{}, Domains: []domainJSON{}, Problems: []string{}}
	if st.Source != nil {
		c := condOf(*st.Source)
		out.Fetch = &c
	}
	for _, q := range st.Quota {
		out.Quota = append(out.Quota, quotaJSON{Resource: q.Resource, Used: q.Used, Hard: q.Hard})
	}
	for _, s := range st.Services {
		out.Services = append(out.Services, serviceJSON{Name: s.Name, URL: s.URL, Public: s.Public, Ready: condOf(s.Ready), Revision: s.Revision, Pods: s.Pods})
	}
	for _, w := range st.Workloads {
		out.Workloads = append(out.Workloads, workJSON{Kind: w.Kind, Name: w.Name, State: w.State, Ready: w.Ready})
	}
	for _, d := range st.Databases {
		out.Databases = append(out.Databases, dbJSON{Name: d.Name, Phase: d.Phase, Instances: d.Ready, Healthy: d.Healthy})
	}
	for _, b := range st.Buckets {
		out.Buckets = append(out.Buckets, bucketJSON{Name: b.Name, Bucket: b.Bucket, Public: b.Public, URL: b.URL, SizeBytes: b.Size, Objects: b.Objects, Ready: condOf(b.Ready)})
	}
	for _, d := range st.Domains {
		out.Domains = append(out.Domains, domainJSON{Host: d.Host, Service: d.Service, Proxied: d.Proxied, URL: d.URL, Ready: condOf(d.Ready)})
	}
	out.Problems = append(out.Problems, st.Problems...)
	return out
}

// rfc3339 formats a time for JSON, "" for none.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// splitTabs splits a psql output line.
func splitTabs(line string) []string { return strings.Split(line, "\t") }

// envVarJSON is one of mcloud env list --json's variables.
type envVarJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// From: maxcloud.yaml (its env:, in this checkout) or mcloud (mcloud
	// env set). A name in both is listed once, from maxcloud.yaml.
	From string `json:"from"`
}

func envJSON(app string, file, cluster map[string]string) any {
	out := struct {
		App       string       `json:"app"`
		Variables []envVarJSON `json:"variables"`
	}{App: app, Variables: []envVarJSON{}}
	for _, src := range []struct {
		from string
		vars map[string]string
	}{{ProjectFile, file}, {"mcloud", cluster}} {
		names := make([]string, 0, len(src.vars))
		for n := range src.vars {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out.Variables = append(out.Variables, envVarJSON{Name: n, Value: src.vars[n], From: src.from})
		}
	}
	return out
}

// branchJSON is one of mcloud db branch list --json's branches.
type branchJSON struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`
	Cluster string `json:"cluster"`
	// At: the point in time it was restored to; "" for the latest.
	At             string `json:"at"`
	FromGeneration int64  `json:"fromGeneration"`
	Created        string `json:"created"`
	Message        string `json:"message,omitempty"`
}

func branchesJSON(app string, bs []kube.Branch) any {
	out := struct {
		App      string       `json:"app"`
		Branches []branchJSON `json:"branches"`
	}{App: app, Branches: []branchJSON{}}
	for _, b := range bs {
		out.Branches = append(out.Branches, branchJSON{Name: b.Name, Phase: b.Phase, Cluster: b.Cluster, At: b.At,
			FromGeneration: b.FromGeneration, Created: rfc3339(b.Created), Message: b.Message})
	}
	return out
}

// queueJSON is mcloud queue ls --json.
type queueJSON struct {
	App    string           `json:"app"`
	Queues []queueCountJSON `json:"queues"`
	// State is the state Jobs lists (--state), Jobs the newest in it.
	State string    `json:"state"`
	Jobs  []jobJSON `json:"jobs"`
}

type queueCountJSON struct {
	Name    string `json:"name"`
	Pending int64  `json:"pending"`
	Running int64  `json:"running"`
	Failed  int64  `json:"failed"`
	Done    int64  `json:"done"`
}

type jobJSON struct {
	ID        int64  `json:"id"`
	Queue     string `json:"queue"`
	State     string `json:"state"`
	Attempts  int64  `json:"attempts"`
	Created   string `json:"created"` // RFC 3339, UTC
	LastError string `json:"lastError,omitempty"`
}

// queueOf reads queue ls's psql output: counts per queue, "---", then the
// jobs (id, queue, state, attempts, local created, last error, created in
// RFC 3339).
func queueOf(app, state, counts, jobs string) queueJSON {
	out := queueJSON{App: app, State: state, Queues: []queueCountJSON{}, Jobs: []jobJSON{}}
	n := func(s string) int64 { v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return v }
	for _, line := range lines(counts) {
		c := splitTabs(line)
		if len(c) == 5 {
			out.Queues = append(out.Queues, queueCountJSON{Name: c[0], Pending: n(c[1]), Running: n(c[2]), Failed: n(c[3]), Done: n(c[4])})
		}
	}
	for _, line := range lines(jobs) {
		c := splitTabs(line)
		if len(c) >= 7 {
			out.Jobs = append(out.Jobs, jobJSON{ID: n(c[0]), Queue: c[1], State: c[2], Attempts: n(c[3]), Created: c[6], LastError: c[5]})
		}
	}
	return out
}
