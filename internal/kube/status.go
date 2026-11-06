package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

// App is a registered app as the cluster sees it.
type App struct {
	Name     string
	Entry    *registry.Entry // from the provider; nil if not registered
	Deploy   Condition       // the app's Flux Kustomization
	Revision string          // last revision Flux applied
	Applied  bool            // the Kustomization exists
	InGit    bool            // registered in the homelab repo, not with mcloud
}

// Apps lists the registered apps.
func (c *Client) Apps(ctx context.Context) ([]App, error) {
	providers, err := c.list(ctx, gvrProvider, FluxNamespace, metav1.ListOptions{LabelSelector: registry.AppLabel + "=true"})
	if err != nil {
		return nil, err
	}
	var apps []App
	for i := range providers {
		a, err := c.app(ctx, &providers[i])
		if err != nil {
			return nil, err
		}
		apps = append(apps, *a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

func (c *Client) app(ctx context.Context, provider *unstructured.Unstructured) (*App, error) {
	a := &App{Name: provider.GetName()}
	{
		values, _, _ := unstructured.NestedMap(provider.Object, "spec", "defaultValues")
		s := func(k string) string {
			if v, ok := values[k]; ok && v != nil {
				return fmt.Sprint(v)
			}
			return ""
		}
		a.Entry = &registry.Entry{Name: s("name"), Repo: s("repo"), Path: s("path"), Branch: s("branch"), CPU: s("cpu"), Memory: s("memory"), Pods: s("pods"), Deploy: s("deploy"),
			Buckets: s("buckets"), DB: s("db"), DBInstances: s("dbInstances"), DBSize: s("dbSize"), Domains: s("domains")}
		a.InGit = provider.GetLabels()[registry.FluxOwnerLabel] != ""
		if a.Entry.Name != "" {
			a.Name = a.Entry.Name
		}
	}
	k, err := c.get(ctx, gvrKustomization, FluxNamespace, a.Name)
	if err != nil {
		return nil, err
	}
	if k != nil {
		a.Applied = true
		a.Deploy = condition(k, "Ready")
		a.Revision = str(k, "status", "lastAppliedRevision")
	}
	return a, nil
}

// Status is everything `mcloud status` shows for one app.
type Status struct {
	App
	Source *Condition // the GitRepository, for apps in their own repo
	// SourceAccessURL is where to give Flux's GitHub App access to the
	// app's repo (the GitRepository's annotation SourceAccessAnnotation).
	SourceAccessURL string
	Services        []Service
	Workloads       []Workload
	Databases       []Database
	Quota           []QuotaLine
	Problems        []string // pods that aren't well, recent warnings
}

type Service struct {
	Name     string
	URL      string
	Public   bool
	Ready    Condition
	Revision string
	Pods     int
}

type Workload struct {
	Kind, Name string
	State      string // e.g. "1/1 ready", "every 5m, last 2m ago", "idle"
	Ready      bool
}

type Database struct {
	Name, Phase string
	Ready       string
	Healthy     bool
}

type QuotaLine struct{ Resource, Used, Hard string }

// AppStatus collects the app's state. A name that isn't registered still
// gets whatever its namespace holds.
func (c *Client) AppStatus(ctx context.Context, name string) (*Status, error) {
	p, err := c.get(ctx, gvrProvider, FluxNamespace, name)
	if err != nil {
		return nil, err
	}
	st := &Status{}
	if p != nil {
		a, err := c.app(ctx, p)
		if err != nil {
			return nil, err
		}
		st.App = *a
	} else {
		st.App = App{Name: name}
		if k, err := c.get(ctx, gvrKustomization, FluxNamespace, name); err == nil && k != nil {
			st.Applied, st.Deploy, st.Revision = true, condition(k, "Ready"), str(k, "status", "lastAppliedRevision")
		}
	}
	if st.Entry != nil && st.Entry.Repo != "" {
		if g, err := c.get(ctx, gvrGitRepository, FluxNamespace, name); err == nil && g != nil {
			cond := condition(g, "Ready")
			st.Source = &cond
			st.SourceAccessURL = g.GetAnnotations()[SourceAccessAnnotation]
		}
	}

	if _, err := c.Core.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return st, nil
	} else if err != nil {
		return nil, err
	}
	ns := name

	pods, err := c.Core.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	running := map[string]int{} // Knative service -> running pods
	podNames := map[string]bool{}
	readyPods := map[string]bool{}
	for _, p := range pods.Items {
		podNames[p.Name] = true
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				readyPods[p.Name] = true
			}
		}
		if svc := p.Labels["serving.knative.dev/service"]; svc != "" && p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			running[svc]++
		}
		if pr := podProblem(&p); pr != "" {
			st.Problems = append(st.Problems, pr)
		}
	}

	ksvcs, err := c.list(ctx, gvrKService, ns, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range ksvcs {
		k := &ksvcs[i]
		st.Services = append(st.Services, Service{
			Name:     k.GetName(),
			URL:      str(k, "status", "url"),
			Public:   k.GetLabels()["run.gronblom.dev/expose"] == "public",
			Ready:    condition(k, "Ready"),
			Revision: str(k, "status", "latestReadyRevisionName"),
			Pods:     running[k.GetName()],
		})
	}

	deps, err := c.Core.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: "!serving.knative.dev/service"})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		st.Workloads = append(st.Workloads, Workload{
			Kind: "Deployment", Name: d.Name,
			State: fmt.Sprintf("%d/%d ready", d.Status.ReadyReplicas, want),
			Ready: d.Status.ReadyReplicas >= want,
		})
	}
	crons, err := c.Core.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, cj := range crons.Items {
		state := "schedule " + cj.Spec.Schedule
		if cj.Status.LastScheduleTime != nil {
			state += ", last ran " + ago(cj.Status.LastScheduleTime.Time)
		}
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			state += ", suspended"
		}
		st.Workloads = append(st.Workloads, Workload{Kind: "CronJob", Name: cj.Name, State: state, Ready: true})
	}
	for _, kind := range []struct {
		name string
		list []unstructured.Unstructured
	}{{"ScaledJob", must(c.list(ctx, gvrScaledJob, ns, metav1.ListOptions{}))}, {"ScaledObject", must(c.list(ctx, gvrScaledObject, ns, metav1.ListOptions{}))}} {
		for i := range kind.list {
			o := &kind.list[i]
			ready, active := condition(o, "Ready"), condition(o, "Active")
			state := "idle"
			if active.Ready() {
				state = "active"
			}
			if !ready.Ready() {
				state = "not ready: " + firstNonEmpty(ready.Message, ready.Reason)
			}
			st.Workloads = append(st.Workloads, Workload{Kind: kind.name, Name: o.GetName(), State: state, Ready: ready.Ready()})
		}
	}

	dbs, err := c.list(ctx, gvrCNPGCluster, ns, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range dbs {
		d := &dbs[i]
		st.Databases = append(st.Databases, Database{
			Name:    d.GetName(),
			Phase:   str(d, "status", "phase"),
			Ready:   fmt.Sprintf("%s/%s", orZero(str(d, "status", "readyInstances")), orZero(str(d, "spec", "instances"))),
			Healthy: condition(d, "Ready").Ready(),
		})
	}

	if q, err := c.Core.CoreV1().ResourceQuotas(ns).Get(ctx, "tenant", metav1.GetOptions{}); err == nil {
		for _, r := range []corev1.ResourceName{"requests.cpu", "requests.memory", "pods"} {
			hard, ok := q.Status.Hard[r]
			if !ok {
				continue
			}
			used := q.Status.Used[r]
			st.Quota = append(st.Quota, QuotaLine{Resource: string(r), Used: used.String(), Hard: hard.String()})
		}
	}

	events, err := c.Core.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "type=Warning"})
	if err == nil {
		// Warnings about pods that are gone are history, e.g. a Knative pod's
		// readiness failing while it drains on scale-down; failed probes of a
		// pod that's ready now were it starting up.
		var current []corev1.Event
		for _, e := range events.Items {
			if e.InvolvedObject.Kind == "Pod" {
				if !podNames[e.InvolvedObject.Name] || e.Reason == "Unhealthy" && readyPods[e.InvolvedObject.Name] {
					continue
				}
			}
			current = append(current, e)
		}
		st.Problems = append(st.Problems, recentWarnings(current, 15*time.Minute, 5)...)
	}
	return st, nil
}

// podProblem describes a pod that isn't well, or returns "".
func podProblem(p *corev1.Pod) string {
	if p.Status.Phase == corev1.PodSucceeded || p.DeletionTimestamp != nil {
		return ""
	}
	for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" && w.Reason != "ContainerCreating" {
			return fmt.Sprintf("pod %s: %s %s: %s", p.Name, cs.Name, w.Reason, oneLine(w.Message))
		}
		if t := cs.LastTerminationState.Terminated; t != nil && cs.RestartCount > 0 && time.Since(t.FinishedAt.Time) < time.Hour {
			return fmt.Sprintf("pod %s: %s restarted %d times, last exit %d (%s) %s", p.Name, cs.Name, cs.RestartCount, t.ExitCode, t.Reason, ago(t.FinishedAt.Time))
		}
	}
	if p.Status.Phase == corev1.PodFailed {
		return fmt.Sprintf("pod %s: failed: %s", p.Name, firstNonEmpty(oneLine(p.Status.Message), p.Status.Reason))
	}
	if p.Status.Phase == corev1.PodPending && time.Since(p.CreationTimestamp.Time) > time.Minute {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
				return fmt.Sprintf("pod %s: not scheduled: %s", p.Name, oneLine(c.Message))
			}
		}
		return fmt.Sprintf("pod %s: pending for %s", p.Name, ago(p.CreationTimestamp.Time))
	}
	return ""
}

// recentWarnings returns the newest distinct Warning events.
func recentWarnings(events []corev1.Event, within time.Duration, max int) []string {
	last := func(e corev1.Event) time.Time {
		ts := []time.Time{e.LastTimestamp.Time, e.EventTime.Time, e.CreationTimestamp.Time}
		if e.Series != nil {
			ts = append([]time.Time{e.Series.LastObservedTime.Time}, ts...)
		}
		for _, t := range ts {
			if !t.IsZero() {
				return t
			}
		}
		return time.Time{}
	}
	sort.Slice(events, func(i, j int) bool { return last(events[i]).After(last(events[j])) })
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if time.Since(last(e)) > within || len(out) >= max {
			break
		}
		key := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name + "/" + e.Reason
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("%s %s/%s %s: %s", ago(last(e)), strings.ToLower(e.InvolvedObject.Kind), e.InvolvedObject.Name, e.Reason, oneLine(e.Message)))
	}
	return out
}

// must drops the error: KEDA's kinds are optional, and a failed list just
// leaves them out of the status.
func must[T any](v T, _ error) T { return v }

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
