package kube

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type LogOptions struct {
	Namespace string
	// Name picks the pods of one Knative service, Deployment, CronJob,
	// ScaledJob or ScaledObject target; empty means every pod.
	Name          string
	Selector      string // a label selector instead of Name
	Follow        bool
	Since         time.Duration
	Tail          int64 // lines per container; 0 means all
	AllContainers bool  // include Knative's queue-proxy sidecar
	Color         bool
	PollInterval  time.Duration
}

// matcher decides which pods' logs to show.
type matcher struct {
	selector string
	match    func(*corev1.Pod, map[string]string) bool // jobOwners: Job -> its owner
	needJobs bool
}

func (c *Client) matcher(ctx context.Context, o LogOptions) (*matcher, error) {
	if o.Selector != "" {
		return &matcher{selector: o.Selector, match: func(*corev1.Pod, map[string]string) bool { return true }}, nil
	}
	if o.Name == "" {
		return &matcher{match: func(*corev1.Pod, map[string]string) bool { return true }}, nil
	}
	ksvc, err := c.get(ctx, gvrKService, o.Namespace, o.Name)
	if err != nil {
		return nil, err
	}
	if ksvc != nil {
		return &matcher{selector: "serving.knative.dev/service=" + o.Name, match: func(*corev1.Pod, map[string]string) bool { return true }}, nil
	}
	// Anything else: pods labelled with the name or owned by it, pods of a
	// Deployment's ReplicaSets, and pods of Jobs started by a CronJob or
	// ScaledJob.
	name := o.Name
	return &matcher{needJobs: true, match: func(p *corev1.Pod, jobOwners map[string]string) bool {
		if p.Labels["app.kubernetes.io/name"] == name {
			return true
		}
		for _, ref := range p.OwnerReferences {
			if ref.Name == name { // owned directly, e.g. a CloudNativePG Cluster
				return true
			}
			switch ref.Kind {
			case "ReplicaSet":
				if i := strings.LastIndex(ref.Name, "-"); i > 0 && ref.Name[:i] == name {
					return true
				}
			case "Job":
				if ref.Name == name || jobOwners[ref.Name] == name {
					return true
				}
			}
		}
		return false
	}}, nil
}

func (c *Client) pods(ctx context.Context, o LogOptions, m *matcher) ([]corev1.Pod, error) {
	list, err := c.Core.CoreV1().Pods(o.Namespace).List(ctx, metav1.ListOptions{LabelSelector: m.selector})
	if err != nil {
		return nil, err
	}
	jobOwners := map[string]string{}
	if m.needJobs {
		jobs, err := c.Core.BatchV1().Jobs(o.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, j := range jobs.Items {
			for _, ref := range j.OwnerReferences {
				if ref.Controller != nil && *ref.Controller {
					jobOwners[j.Name] = ref.Name
				}
			}
		}
	}
	var out []corev1.Pod
	for i := range list.Items {
		if m.match(&list.Items[i], jobOwners) {
			out = append(out, list.Items[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreationTimestamp.Before(&out[j].CreationTimestamp) })
	return out, nil
}

// containers lists the containers whose logs to show.
func containers(p *corev1.Pod, all bool) []string {
	var names []string
	for _, c := range p.Spec.Containers {
		if c.Name == "queue-proxy" && !all {
			continue
		}
		names = append(names, c.Name)
	}
	return names
}

func started(p *corev1.Pod, container string) (bool, int32) {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == container {
			return cs.State.Running != nil || cs.State.Terminated != nil, cs.RestartCount
		}
	}
	return false, 0
}

// Logs writes the selected pods' logs to w, each line prefixed with its pod
// (and container, for pods with several). With Follow it keeps going, and
// picks up pods that start later, until ctx is done. Notes about what it's
// doing go to errW.
func (c *Client) Logs(ctx context.Context, o LogOptions, w, errW io.Writer) error {
	m, err := c.matcher(ctx, o)
	if err != nil {
		return err
	}
	out := &lineWriter{w: w, errW: errW, color: o.Color}
	if o.PollInterval == 0 {
		o.PollInterval = 2 * time.Second
	}

	pods, err := c.pods(ctx, o, m)
	if err != nil {
		return err
	}
	if !o.Follow {
		if len(pods) == 0 {
			return fmt.Errorf("no pods%s in %s (a Knative service scaled to zero has none; `mcloud logs -f` waits for the next one)", forName(o.Name), o.Namespace)
		}
		for i := range pods {
			for _, ctr := range containers(&pods[i], o.AllContainers) {
				if ok, _ := started(&pods[i], ctr); !ok {
					continue
				}
				if err := c.stream(ctx, &pods[i], ctr, o, o.Since, o.Tail, false, out); err != nil {
					out.note("%s: %v", pods[i].Name, err)
				}
			}
		}
		return nil
	}

	// Follow: containers that existed at the start get --since/--tail;
	// anything that starts later is shown from its first line.
	type key struct {
		uid, container string
		restarts       int32
	}
	streaming := map[key]bool{}
	var wg sync.WaitGroup
	first := true
	if len(pods) == 0 {
		out.note("waiting for pods%s in %s ...", forName(o.Name), o.Namespace)
	}
	for {
		for i := range pods {
			p := pods[i]
			for _, ctr := range containers(&p, o.AllContainers) {
				ok, restarts := started(&p, ctr)
				k := key{string(p.UID), ctr, restarts}
				if !ok || streaming[k] {
					continue
				}
				streaming[k] = true
				since, tail := time.Duration(0), int64(0)
				if first {
					since, tail = o.Since, o.Tail
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := c.stream(ctx, &p, ctr, o, since, tail, true, out); err != nil && ctx.Err() == nil {
						out.note("%s: %v", p.Name, err)
					}
				}()
			}
		}
		first = false
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-time.After(o.PollInterval):
		}
		if pods, err = c.pods(ctx, o, m); err != nil && ctx.Err() == nil {
			out.note("listing pods: %v", err)
		}
	}
}

func forName(name string) string {
	if name == "" {
		return ""
	}
	return " of " + name
}

func (c *Client) stream(ctx context.Context, p *corev1.Pod, container string, o LogOptions, since time.Duration, tail int64, follow bool, out *lineWriter) error {
	opts := &corev1.PodLogOptions{Container: container, Follow: follow}
	if since > 0 {
		s := int64(since.Seconds())
		opts.SinceSeconds = &s
	}
	if tail > 0 {
		opts.TailLines = &tail
	}
	rc, err := c.Core.CoreV1().Pods(p.Namespace).GetLogs(p.Name, opts).Stream(ctx)
	if err != nil {
		return err
	}
	defer rc.Close()
	prefix := p.Name
	if len(containers(p, o.AllContainers)) > 1 {
		prefix += "/" + container
	}
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		out.line(prefix, sc.Text())
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// lineWriter interleaves whole lines from several streams.
type lineWriter struct {
	mu    sync.Mutex
	w     io.Writer
	errW  io.Writer
	color bool
}

var palette = []int{36, 33, 32, 35, 34, 96, 93, 92, 95, 94}

func (l *lineWriter) line(prefix, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.color {
		h := fnv.New32a()
		h.Write([]byte(prefix))
		fmt.Fprintf(l.w, "\x1b[%dm%s\x1b[0m %s\n", palette[h.Sum32()%uint32(len(palette))], prefix, text)
		return
	}
	fmt.Fprintf(l.w, "%s %s\n", prefix, text)
}

func (l *lineWriter) note(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf(format, a...)
	if l.color {
		msg = "\x1b[2m" + msg + "\x1b[0m"
	}
	fmt.Fprintln(l.errW, msg)
}
