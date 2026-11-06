// Package functions turns an app's function description (what an SDK's
// runtime prints for MAXCLOUD_DESCRIBE=1 or --describe) into the manifests
// Flux deploys: a Knative service per HTTP function, a CronJob per schedule,
// a KEDA-scaled Deployment per queue, and the database queues need.
package functions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/maxemiliang/maxcloud/internal/kustomize"
)

// DescribeTag starts the description line an image prints when run with
// MAXCLOUD_DESCRIBE=1.
const DescribeTag = "maxcloud:describe:"

type Description struct {
	SDK       string     `json:"sdk"`
	Version   string     `json:"version"`
	Functions []Function `json:"functions"`
	// Migrations are the SQL migrations the image applies when it starts
	// (their file names). nil: an SDK from before migrations.
	Migrations []string `json:"migrations"`
}

type Function struct {
	Name           string  `json:"name"`
	Export         string  `json:"export"`
	Kind           string  `json:"kind"` // http, schedule, queue
	Memory         *string `json:"memory"`
	CPU            *string `json:"cpu"`
	TimeoutSeconds float64 `json:"timeoutSeconds"`

	// The container, every kind:
	// MemoryLimit above Memory (the request), for spikes.
	MemoryLimit *string `json:"memoryLimit"`
	// User the container runs as (and its group), for an image that
	// doesn't set a non-root USER.
	User *int64 `json:"user"`
	// ReadOnlyRootFilesystem: write only to Volumes.
	ReadOnlyRootFilesystem bool `json:"readOnlyRootFilesystem"`
	// Volumes are scratch directories (emptyDir), gone with the pod.
	Volumes []Volume `json:"volumes"`

	// http
	Public       bool `json:"public"`
	MinInstances int  `json:"minInstances"`
	MaxInstances int  `json:"maxInstances"`
	Concurrency  int  `json:"concurrency"`
	// Proxied: the public hostname's DNS record is proxied by Cloudflare.
	Proxied bool `json:"proxied"`
	// HealthPath answers 2xx when the instance can take requests: Knative's
	// readiness probe, and a liveness probe that restarts it when it stops.
	HealthPath string `json:"healthPath"`

	// schedule
	Schedule string `json:"schedule"`
	TimeZone string `json:"timeZone"`
	Retries  int    `json:"retries"`

	// queue (also MaxInstances, Concurrency)
	Queue          string  `json:"queue"`
	MaxAttempts    int     `json:"maxAttempts"`
	BackoffSeconds float64 `json:"backoffSeconds"`
	BacklogQuery   string  `json:"backlogQuery"`
}

// Volume is a scratch directory for an HTTP function.
type Volume struct {
	Path      string `json:"path"`
	SizeLimit string `json:"sizeLimit"`
	// Memory: in RAM (tmpfs), counted against the container's memory; for
	// /dev/shm.
	Memory bool `json:"memory"`
}

// Paths Knative won't mount a volume on.
var reservedPaths = map[string]bool{"/": true, "/dev": true, "/dev/log": true, "/tmp": true, "/var": true, "/var/log": true}

var quantityRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$`)

// Parse reads a description: plain JSON (--describe), or output with a
// tagged line in it (MAXCLOUD_DESCRIBE=1).
func Parse(data []byte) (*Description, error) {
	raw := bytes.TrimSpace(data)
	if !bytes.HasPrefix(raw, []byte("{")) {
		raw = nil
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, DescribeTag) {
				raw = []byte(strings.TrimPrefix(line, DescribeTag))
			}
		}
		if raw == nil {
			return nil, errors.New("no function description in the output (is the app's start command maxcloud-functions?)")
		}
	}
	var d Description
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("function description: %w", err)
	}
	return &d, d.validate()
}

var dnsLabel = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$`)

// Knative's default max-revision-timeout-seconds.
const maxHTTPTimeout = 600

func (d *Description) validate() error {
	seen := map[string]bool{}
	for _, f := range d.Functions {
		if !dnsLabel.MatchString(f.Name) {
			return fmt.Errorf("function %q: not a valid name", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("two functions named %s", f.Name)
		}
		seen[f.Name] = true
		switch f.Kind {
		case "http":
			if f.TimeoutSeconds > maxHTTPTimeout {
				return fmt.Errorf("function %s: timeoutSeconds %v is over Knative's limit of %d", f.Name, f.TimeoutSeconds, maxHTTPTimeout)
			}
			if err := f.validateHTTP(); err != nil {
				return fmt.Errorf("function %s: %w", f.Name, err)
			}
		case "schedule":
			if len(strings.Fields(f.Schedule)) != 5 {
				return fmt.Errorf("function %s: schedule %q isn't a five-field cron schedule", f.Name, f.Schedule)
			}
		case "queue":
			if f.Queue == "" || f.BacklogQuery == "" {
				return fmt.Errorf("function %s: queue function without a queue", f.Name)
			}
		default:
			return fmt.Errorf("function %s: unknown kind %q (a newer SDK than this mcloud?)", f.Name, f.Kind)
		}
		if err := f.validateContainer(); err != nil {
			return fmt.Errorf("function %s: %w", f.Name, err)
		}
	}
	return nil
}

func (f Function) validateHTTP() error {
	if f.Proxied && !f.Public {
		return errors.New("proxied needs public: a private function has no public hostname to proxy")
	}
	if f.HealthPath != "" && !strings.HasPrefix(f.HealthPath, "/") {
		return fmt.Errorf("healthPath %q: a path starting with /", f.HealthPath)
	}
	return nil
}

func (f Function) validateContainer() error {
	if f.MemoryLimit != nil && !quantityRe.MatchString(*f.MemoryLimit) {
		return fmt.Errorf("memoryLimit %q: a quantity such as 1Gi", *f.MemoryLimit)
	}
	if f.User != nil && *f.User <= 0 {
		return fmt.Errorf("user %d: a non-root user id", *f.User)
	}
	seen := map[string]bool{}
	for _, v := range f.Volumes {
		p := filepath.Clean(v.Path)
		if !filepath.IsAbs(v.Path) || p != v.Path {
			return fmt.Errorf("volume %q: an absolute, clean path such as /scratch", v.Path)
		}
		if reservedPaths[p] {
			return fmt.Errorf("volume %s: Knative doesn't allow a volume there (use another directory, and point TMPDIR at it for temporary files)", p)
		}
		if seen[p] {
			return fmt.Errorf("volume %s listed twice", p)
		}
		seen[p] = true
		if v.SizeLimit != "" && !quantityRe.MatchString(v.SizeLimit) {
			return fmt.Errorf("volume %s: sizeLimit %q: a quantity such as 512Mi", p, v.SizeLimit)
		}
	}
	if len(f.Volumes) > 8 {
		return errors.New("at most 8 volumes")
	}
	return nil
}

// NeedsDatabase reports whether any function uses a queue.
func (d *Description) NeedsDatabase() bool {
	for _, f := range d.Functions {
		if f.Kind == "queue" {
			return true
		}
	}
	return false
}

type Options struct {
	// Image is the name the manifests use for the app's image, which the
	// deploy workflow replaces with the built one. Default "app".
	Image string
	// LegacyDatabase: the app's deploy/k8s has a database.yaml, as mcloud
	// wrote before the maxcloud controller ran databases. That file defines
	// the Cluster db and KEDA's TriggerAuthentication db, and DATABASE_URL
	// is required. Otherwise the database is the registry's (mcloud apps
	// set --db): DATABASE_URL is optional, and queue functions get the
	// TriggerAuthentication here.
	LegacyDatabase bool
	// Labels are added to every object (direct deploys mark theirs).
	Labels map[string]string
	// App is the app's name: every function gets it as MAXCLOUD_APP (a
	// literal: Knative refuses the downward API unless a flag allows it).
	App string
	// Env is maxcloud.yaml's env: plain variables every function gets, in
	// the manifests themselves (a change is a new revision). They win over
	// the app's ConfigMap app-env and Secret app-secrets.
	Env map[string]string
}

const (
	// DatabaseName is the CloudNativePG cluster queues use; its Secret is db-app.
	DatabaseName = "db"
	// StorageSecret is where the maxcloud controller puts the app's S3
	// credentials once the app has a bucket: AWS_* variables every AWS SDK
	// reads, and MAXCLOUD_APP.
	StorageSecret = "maxcloud-s3"
	// AppSecrets holds the app's own secrets (mcloud secrets set).
	AppSecrets = "app-secrets"
	// AppEnv holds the app's plain variables set with mcloud env set (those
	// in maxcloud.yaml are in the manifests).
	AppEnv = "app-env"
	header = `# Generated by mcloud from the app's functions (maxcloud-functions
# --describe). Don't edit: the deploy workflow rewrites this file on every
# deploy. Change a function's options in its code instead.
`
)

type obj = map[string]any

// Objects returns the functions' objects.
func Objects(d *Description, o Options) []map[string]any {
	if o.Image == "" {
		o.Image = "app"
	}
	var docs []obj
	for _, f := range d.Functions {
		switch f.Kind {
		case "http":
			docs = append(docs, knativeService(f, o))
		case "schedule":
			docs = append(docs, cronJob(f, o))
		case "queue":
			docs = append(docs, worker(f, o), scaledObject(f))
		}
	}
	if d.NeedsDatabase() && !o.LegacyDatabase {
		docs = append(docs, triggerAuthentication())
	}
	for _, doc := range docs {
		if len(o.Labels) == 0 {
			break
		}
		l := doc["metadata"].(obj)["labels"].(obj)
		for k, v := range o.Labels {
			l[k] = v
		}
	}
	return docs
}

// Manifests returns functions.yaml.
func Manifests(d *Description, o Options) ([]byte, error) {
	docs := Objects(d, o)
	var b strings.Builder
	b.WriteString(header)
	if len(docs) == 0 {
		b.WriteString("# The app has no functions yet.\n")
	}
	for _, doc := range docs {
		out, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		b.WriteString("---\n")
		b.Write(out)
	}
	return []byte(b.String()), nil
}

func labels(f Function) obj {
	return obj{"app.kubernetes.io/name": f.Name, "app.kubernetes.io/part-of": "maxcloud-functions"}
}

func env(f Function, o Options) []any {
	e := []any{obj{"name": "FUNCTION_TARGET", "value": f.Name}}
	if o.App != "" {
		e = append(e, obj{"name": "MAXCLOUD_APP", "value": o.App})
	}
	ref := obj{"name": DatabaseName + "-app", "key": "uri"}
	if !o.LegacyDatabase {
		// No database.yaml: the app may still have a database from the
		// registry (the maxcloud controller's, also Cluster db), so take
		// its URL if it's there.
		ref["optional"] = true
	}
	e = append(e, obj{"name": "DATABASE_URL", "valueFrom": obj{"secretKeyRef": ref}})
	names := make([]string, 0, len(o.Env))
	for n := range o.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e = append(e, obj{"name": n, "value": o.Env[n]})
	}
	return e
}

func resources(f Function) obj {
	r := obj{}
	req := obj{}
	if f.CPU != nil {
		req["cpu"] = *f.CPU
	}
	if f.Memory != nil {
		req["memory"] = *f.Memory
		r["limits"] = obj{"memory": *f.Memory}
	}
	if f.MemoryLimit != nil {
		r["limits"] = obj{"memory": *f.MemoryLimit}
	}
	if len(req) > 0 {
		r["requests"] = req
	}
	return r
}

func container(f Function, o Options, name string) obj {
	c := obj{
		"image": o.Image,
		"env":   env(f, o),
		// Optional: the Secret maxcloud-s3 only exists once the app has a
		// bucket (registry field buckets), app-env and app-secrets once
		// something is set; a function deployed before gets them on its
		// next start.
		"envFrom": []any{
			obj{"secretRef": obj{"name": StorageSecret, "optional": true}},
			obj{"configMapRef": obj{"name": AppEnv, "optional": true}},
			obj{"secretRef": obj{"name": AppSecrets, "optional": true}},
		},
	}
	if name != "" {
		c["name"] = name
	}
	if r := resources(f); len(r) > 0 {
		c["resources"] = r
	}
	// Container-level: Knative refuses a pod securityContext with these.
	sc := obj{}
	if f.User != nil {
		sc["runAsUser"], sc["runAsGroup"] = *f.User, *f.User
	}
	if f.ReadOnlyRootFilesystem {
		sc["readOnlyRootFilesystem"] = true
	}
	if len(sc) > 0 {
		c["securityContext"] = sc
	}
	var mounts []any
	for i, v := range f.Volumes {
		mounts = append(mounts, obj{"name": volumeName(i), "mountPath": v.Path})
	}
	if len(mounts) > 0 {
		c["volumeMounts"] = mounts
	}
	return c
}

func volumeName(i int) string { return fmt.Sprintf("scratch-%d", i) }

// podVolumes are the emptyDirs behind the container's volumeMounts.
func podVolumes(f Function) []any {
	var out []any
	for i, v := range f.Volumes {
		ed := obj{}
		if v.Memory {
			ed["medium"] = "Memory"
		}
		if v.SizeLimit != "" {
			ed["sizeLimit"] = v.SizeLimit
		}
		out = append(out, obj{"name": volumeName(i), "emptyDir": ed})
	}
	return out
}

// hardened is the pod security a plain pod needs in a restricted namespace;
// Knative adds the same to its pods by itself.
func hardened(c obj) obj {
	sc, _ := c["securityContext"].(obj)
	if sc == nil {
		sc = obj{}
	}
	sc["allowPrivilegeEscalation"] = false
	sc["capabilities"] = obj{"drop": []any{"ALL"}}
	c["securityContext"] = sc
	return c
}

func podSecurity() obj {
	return obj{"runAsNonRoot": true, "seccompProfile": obj{"type": "RuntimeDefault"}}
}

func withVolumes(f Function, spec obj) obj {
	if v := podVolumes(f); len(v) > 0 {
		spec["volumes"] = v
	}
	return spec
}

func seconds(v float64, def int) int {
	if v <= 0 {
		return def
	}
	return int(math.Ceil(v))
}

func knativeService(f Function, o Options) obj {
	meta := obj{"name": f.Name, "labels": labels(f)}
	if f.Public {
		meta["labels"].(obj)["run.gronblom.dev/expose"] = "public"
	}
	if f.Proxied {
		// Knative copies the service's annotations to its HTTPRoute, where
		// external-dns reads it.
		meta["annotations"] = obj{"external-dns.kubernetes.io/cloudflare-proxied": "true"}
	}
	ann := obj{
		"autoscaling.knative.dev/min-scale": fmt.Sprint(f.MinInstances),
		"autoscaling.knative.dev/max-scale": fmt.Sprint(max(f.MaxInstances, 1)),
	}
	c := container(f, o, "")
	c["ports"] = []any{obj{"containerPort": 8080}}
	// Knative adds runAsNonRoot, seccomp and no capabilities itself.
	if f.HealthPath != "" {
		// Knative probes the container's port itself; no port here.
		c["readinessProbe"] = obj{"httpGet": obj{"path": f.HealthPath}, "periodSeconds": 10}
		c["livenessProbe"] = obj{
			"httpGet": obj{"path": f.HealthPath}, "initialDelaySeconds": 30,
			"periodSeconds": 30, "timeoutSeconds": 5, "failureThreshold": 3,
		}
	}
	spec := obj{
		"priorityClassName": "tenant",
		"timeoutSeconds":    seconds(f.TimeoutSeconds, 300),
		"containers":        []any{c},
	}
	if v := podVolumes(f); len(v) > 0 {
		spec["volumes"] = v
	}
	if f.Concurrency > 0 {
		spec["containerConcurrency"] = f.Concurrency
	}
	return obj{
		"apiVersion": "serving.knative.dev/v1",
		"kind":       "Service",
		"metadata":   meta,
		"spec": obj{"template": obj{
			"metadata": obj{"labels": labels(f), "annotations": ann},
			"spec":     spec,
		}},
	}
}

func cronJob(f Function, o Options) obj {
	timeout := seconds(f.TimeoutSeconds, 3600)
	spec := obj{
		"schedule":                   f.Schedule,
		"concurrencyPolicy":          "Forbid",
		"successfulJobsHistoryLimit": 1,
		"failedJobsHistoryLimit":     3,
		"jobTemplate": obj{"spec": obj{
			"backoffLimit": f.Retries,
			// The runtime stops the function at its timeout; this is the
			// backstop if the process doesn't exit.
			"activeDeadlineSeconds":   (timeout + 60) * (f.Retries + 1),
			"ttlSecondsAfterFinished": 86400,
			"template": obj{
				"metadata": obj{"labels": labels(f)},
				"spec": withVolumes(f, obj{
					"priorityClassName": "tenant",
					"restartPolicy":     "Never",
					"securityContext":   podSecurity(),
					"containers":        []any{hardened(container(f, o, "function"))},
				}),
			},
		}},
	}
	if f.TimeZone != "" && f.TimeZone != "UTC" {
		spec["timeZone"] = f.TimeZone
	}
	return obj{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": obj{"name": f.Name, "labels": labels(f)}, "spec": spec}
}

func worker(f Function, o Options) obj {
	return obj{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   obj{"name": f.Name, "labels": labels(f)},
		// No replicas: KEDA owns the count, and setting it here would make
		// every Flux sync fight KEDA.
		"spec": obj{
			"selector": obj{"matchLabels": obj{"app.kubernetes.io/name": f.Name}},
			"template": obj{
				"metadata": obj{"labels": labels(f)},
				"spec": withVolumes(f, obj{
					"priorityClassName": "tenant",
					"securityContext":   podSecurity(),
					// The worker finishes or puts back its jobs within 25 s.
					"terminationGracePeriodSeconds": 40,
					"containers":                    []any{hardened(container(f, o, "worker"))},
				}),
			},
		},
	}
}

func scaledObject(f Function) obj {
	perInstance := max(f.Concurrency, 1)
	return obj{
		"apiVersion": "keda.sh/v1alpha1",
		"kind":       "ScaledObject",
		"metadata":   obj{"name": f.Name, "labels": labels(f)},
		"spec": obj{
			"scaleTargetRef":  obj{"name": f.Name},
			"minReplicaCount": 0,
			"maxReplicaCount": max(f.MaxInstances, 1),
			"pollingInterval": 10,
			// Seconds the queue must stay empty before the worker stops.
			"cooldownPeriod": 120,
			"triggers": []any{obj{
				"type": "postgresql",
				"metadata": obj{
					"query":                      f.BacklogQuery,
					"targetQueryValue":           fmt.Sprint(perInstance),
					"activationTargetQueryValue": "0",
				},
				"authenticationRef": obj{"name": DatabaseName},
			}},
		},
	}
}

// triggerAuthentication is how KEDA logs in to count a queue's backlog:
// the app database's connection URI from the Secret db-app, which
// CloudNativePG writes for the Cluster db the maxcloud controller runs
// (registry field db).
func triggerAuthentication() obj {
	return obj{
		"apiVersion": "keda.sh/v1alpha1",
		"kind":       "TriggerAuthentication",
		"metadata": obj{
			"name":   DatabaseName,
			"labels": obj{"app.kubernetes.io/part-of": "maxcloud-functions"},
		},
		"spec": obj{
			"secretTargetRef": []any{obj{"parameter": "connection", "name": DatabaseName + "-app", "key": "uri"}},
		},
	}
}

const newKustomization = `# What Flux applies from this repo into the app's namespace. functions.yaml
# is generated from the app's functions on every deploy; list your own
# manifests here too.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
# The deploy workflow replaces app with the image it built, pinned by digest.
images:
  - name: app
`

// LegacyDatabaseFile is the database mcloud used to write for queue apps.
const LegacyDatabaseFile = "database.yaml"

// NeedsDatabaseHint is what to tell someone whose app has queue functions
// and no database.yaml: the queue lives in the app's database, which the
// registry provides.
func NeedsDatabaseHint(app string) string {
	if app == "" {
		app = "<app>"
	}
	return "queue functions keep their jobs in the app's database: if " + app +
		" has none yet, add db: true to its maxcloud.yaml (the workers wait until it's there)"
}

// WriteDir writes functions.yaml into dir (deploy/k8s) and lists it in
// kustomization.yaml, creating that if needed; an existing database.yaml
// stays listed too. Returns the files it changed.
func WriteDir(dir string, d *Description, o Options) ([]string, error) {
	if o.Image == "" {
		o.Image = "app"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var changed []string
	write := func(name string, data []byte) error {
		p := filepath.Join(dir, name)
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
			return nil
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		changed = append(changed, p)
		return nil
	}

	// An app set up before the maxcloud controller ran databases may have
	// a database.yaml: it keeps it. New ones get their database from the
	// registry (db: true in maxcloud.yaml) instead; NeedsDatabaseHint says so.
	_, err := os.Stat(filepath.Join(dir, LegacyDatabaseFile))
	hasDB := err == nil

	o.LegacyDatabase = hasDB
	m, err := Manifests(d, o)
	if err != nil {
		return nil, err
	}
	if err := write("functions.yaml", m); err != nil {
		return nil, err
	}

	kfile := filepath.Join(dir, "kustomization.yaml")
	k, err := os.ReadFile(kfile)
	if errors.Is(err, os.ErrNotExist) {
		k = []byte(newKustomization)
	} else if err != nil {
		return nil, err
	}
	ks := strings.Replace(string(k), "resources: []\n", "resources:\n", 1)
	for _, res := range []string{"functions.yaml", LegacyDatabaseFile} {
		if res == LegacyDatabaseFile && !hasDB {
			continue
		}
		if ks, _, err = kustomize.AddResource(ks, res); err != nil {
			return nil, fmt.Errorf("%s: %w", kfile, err)
		}
	}
	if !regexp.MustCompile(`(?m)^images:`).MatchString(ks) {
		ks += "images:\n  - name: " + o.Image + "\n"
	}
	if err := write("kustomization.yaml", []byte(ks)); err != nil {
		return nil, err
	}
	return changed, nil
}
