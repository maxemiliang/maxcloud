// Package registry reads and writes entries in the maxcloud app registry: one
// ResourceSetInputProvider per app in the homelab repo's apps/maxcloud/apps/,
// which the Flux Operator turns into the app's namespace, guardrails and
// reconciler (see apps/maxcloud/resourceset.yaml there).
package registry

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// AppLabel selects an app's ResourceSetInputProvider; the ResourceSet only
// reads providers that carry it.
const AppLabel = "run.gronblom.dev/app"

// Entry is one app. Empty fields take the registry's defaults.
type Entry struct {
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Pods   string `json:"pods,omitempty"`
	// Deploy is "direct" for an app mcloud deploy applies itself; empty
	// (gitops) for one Flux applies from git.
	Deploy string `json:"deploy,omitempty"`
	// Buckets are the app's S3 buckets, comma-separated, each optionally
	// ":public": "uploads,assets:public". The maxcloud controller names
	// them mc-<app>--<bucket>.
	Buckets string `json:"buckets,omitempty"`
	// DB is "true" for an app with a Postgres database run by the maxcloud
	// controller (Cluster db, backed up to mc-<app>--db), with DBInstances
	// (1 or 2) and DBSize (disk per instance).
	DB          string `json:"db,omitempty"`
	DBInstances string `json:"dbInstances,omitempty"`
	DBSize      string `json:"dbSize,omitempty"`
}

// Bucket is one entry of Entry.Buckets.
type Bucket struct {
	Name   string
	Public bool
}

// bucketRe: lowercase letters and digits with single hyphens between, so
// "--" only ever separates the app from the bucket (mc-<app>--<bucket>).
var bucketRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9]|-[a-z0-9])*$`)

// ParseBuckets reads an Entry.Buckets value.
func ParseBuckets(s string) ([]Bucket, error) {
	var out []Bucket
	seen := map[string]bool{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, opt, _ := strings.Cut(item, ":")
		b := Bucket{Name: name}
		switch opt {
		case "":
		case "public":
			b.Public = true
		default:
			return nil, fmt.Errorf("bucket %q: the only option is :public", item)
		}
		if !bucketRe.MatchString(name) {
			return nil, fmt.Errorf("bucket %q: lowercase letters, digits and single dashes", name)
		}
		if name == "db" {
			return nil, fmt.Errorf("bucket %q: reserved for the app's database backups", name)
		}
		if strings.HasSuffix(name, "-s3alias") {
			return nil, fmt.Errorf("bucket %q: names ending in -s3alias are reserved by S3", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("bucket %q is listed twice", name)
		}
		seen[name] = true
		out = append(out, b)
	}
	return out, nil
}

// FormatBuckets is the canonical Entry.Buckets value.
func FormatBuckets(bs []Bucket) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = b.Name
		if b.Public {
			parts[i] += ":public"
		}
	}
	return strings.Join(parts, ",")
}

// S3 bucket names are at most 63 characters: mc-<app>--<bucket>.
const maxBucketName = 63

// DeployDirect marks an app that mcloud deploy applies itself.
const DeployDirect = "direct"

// Direct reports whether the app is deployed directly, not from git.
func (e *Entry) Direct() bool { return e.Deploy == DeployDirect }

// Defaults the ResourceSet applies to empty fields (keep in sync with
// resourceset.yaml in the homelab repo).
const (
	DefaultPath   = "deploy/k8s"
	DefaultBranch = "main"
	DefaultCPU    = "200m"
	DefaultMemory = "1Gi"
	DefaultPods   = "20"
)

// MaxNameLength keeps public hostnames, <service>-<name>.gronblom.dev, within
// DNS's 63-character label limit with room for the service name.
const MaxNameLength = 30

var (
	dnsLabel = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// reserved are namespaces the platform itself uses. Registering one would
// hand it to the ResourceSet, and removing the entry would delete it.
var reserved = map[string]bool{
	"default": true, "flux-system": true, "traefik": true,
	"knative-serving": true, "knative-operator": true, "keda": true,
	"monitoring": true, "cnpg-system": true, "cert-manager": true,
	"external-dns": true, "authelia": true, "kured": true,
}

// Validate checks e and normalises its path.
func (e *Entry) Validate() error {
	if !dnsLabel.MatchString(e.Name) {
		return fmt.Errorf("app name %q: use lowercase letters, digits and dashes, starting with a letter", e.Name)
	}
	if len(e.Name) > MaxNameLength {
		return fmt.Errorf("app name %q: at most %d characters", e.Name, MaxNameLength)
	}
	if reserved[e.Name] || strings.HasPrefix(e.Name, "kube-") {
		return fmt.Errorf("app name %q: reserved for the platform", e.Name)
	}
	// "--" separates app and bucket in bucket names (mc-<app>--<bucket>);
	// in an app name it would let one app's S3 policy match another's
	// buckets.
	if strings.Contains(e.Name, "--") {
		return fmt.Errorf("app name %q: no double dashes", e.Name)
	}
	switch e.DB {
	case "", "true":
	case "false":
		e.DB = ""
	default:
		return fmt.Errorf("db %q: true or false", e.DB)
	}
	if e.DB == "" && (e.DBInstances != "" || e.DBSize != "") {
		return fmt.Errorf("db-instances and db-size need --db")
	}
	if e.DBInstances != "" && e.DBInstances != "1" && e.DBInstances != "2" {
		return fmt.Errorf("db-instances %q: 1, or 2 for a replica on the other node", e.DBInstances)
	}
	if e.DBSize != "" {
		v, err := resource.ParseQuantity(e.DBSize)
		if err != nil || v.Sign() <= 0 {
			return fmt.Errorf("db-size %q: expected a positive quantity such as 1Gi", e.DBSize)
		}
	}
	if e.Buckets != "" {
		bs, err := ParseBuckets(e.Buckets)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if n := len("mc-" + e.Name + "--" + b.Name); n > maxBucketName {
				return fmt.Errorf("bucket %q: mc-%s--%s would be %d characters, S3 allows %d", b.Name, e.Name, b.Name, n, maxBucketName)
			}
		}
		e.Buckets = FormatBuckets(bs)
	}
	if e.Repo != "" && !repoRe.MatchString(e.Repo) {
		return fmt.Errorf("repo %q: expected <owner>/<repo>", e.Repo)
	}
	if e.Path != "" {
		p := strings.TrimPrefix(e.Path, "./")
		p = strings.TrimSuffix(p, "/")
		if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.HasPrefix(p, "..") {
			return fmt.Errorf("path %q: expected a relative directory such as deploy/k8s", e.Path)
		}
		e.Path = p
	}
	switch e.Deploy {
	case "", DeployDirect:
	case "gitops":
		e.Deploy = ""
	default:
		return fmt.Errorf("deploy %q: gitops (the default) or direct", e.Deploy)
	}
	if e.Repo == "" && e.Path == "" && !e.Direct() {
		return fmt.Errorf("an app kept in the homelab repo needs --path (e.g. apps/%s)", e.Name)
	}
	if e.Branch != "" && (!branchRe.MatchString(e.Branch) || strings.Contains(e.Branch, "..")) {
		return fmt.Errorf("branch %q: not a valid branch name", e.Branch)
	}
	for _, q := range []struct{ field, v string }{{"cpu", e.CPU}, {"memory", e.Memory}} {
		if q.v == "" {
			continue
		}
		v, err := resource.ParseQuantity(q.v)
		if err != nil || v.Sign() <= 0 {
			return fmt.Errorf("%s %q: expected a positive quantity such as 500m or 2Gi", q.field, q.v)
		}
	}
	if e.Pods != "" {
		if n, err := strconv.Atoi(e.Pods); err != nil || n <= 0 {
			return fmt.Errorf("pods %q: expected a positive whole number", e.Pods)
		}
	}
	return nil
}

// FilePath is where e's entry lives, relative to the registry directory.
func FilePath(name string) string { return "apps/" + name + ".yaml" }

// Render returns the entry as the file committed to the registry. Values are
// quoted so YAML never reads them as numbers (cpu: 1, branch: 1.0).
func (e *Entry) Render() []byte {
	var b strings.Builder
	b.WriteString("# An app on the run platform, registered with mcloud. The fields are\n")
	b.WriteString("# described in ../resourceset.yaml; removing this file deletes the app\n")
	b.WriteString("# and its data.\n")
	b.WriteString("apiVersion: fluxcd.controlplane.io/v1\n")
	b.WriteString("kind: ResourceSetInputProvider\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(&b, "  name: %s\n", e.Name)
	b.WriteString("  namespace: flux-system\n")
	b.WriteString("  labels:\n")
	fmt.Fprintf(&b, "    %s: \"true\"\n", AppLabel)
	b.WriteString("spec:\n")
	b.WriteString("  type: Static\n")
	b.WriteString("  defaultValues:\n")
	for _, f := range e.fields() {
		fmt.Fprintf(&b, "    %s: %s\n", f.key, strconv.Quote(f.value))
	}
	return []byte(b.String())
}

type field struct{ key, value string }

func (e *Entry) fields() []field {
	var fs []field
	for _, f := range []field{
		{"name", e.Name}, {"repo", e.Repo}, {"path", e.Path}, {"branch", e.Branch},
		{"cpu", e.CPU}, {"memory", e.Memory}, {"pods", e.Pods}, {"deploy", e.Deploy},
		{"buckets", e.Buckets},
		{"db", e.DB}, {"dbInstances", e.DBInstances}, {"dbSize", e.DBSize},
	} {
		if f.value != "" {
			fs = append(fs, f)
		}
	}
	return fs
}

// Parse reads an entry file back.
func Parse(data []byte) (*Entry, error) {
	var doc struct {
		Kind string `json:"kind"`
		Spec struct {
			DefaultValues map[string]any `json:"defaultValues"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != "ResourceSetInputProvider" {
		return nil, fmt.Errorf("not a ResourceSetInputProvider (kind %q)", doc.Kind)
	}
	s := func(k string) string {
		v, ok := doc.Spec.DefaultValues[k]
		if !ok || v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	e := &Entry{
		Name: s("name"), Repo: s("repo"), Path: s("path"), Branch: s("branch"),
		CPU: s("cpu"), Memory: s("memory"), Pods: s("pods"), Deploy: s("deploy"),
		Buckets: s("buckets"),
		DB:      s("db"), DBInstances: s("dbInstances"), DBSize: s("dbSize"),
	}
	if e.Name == "" {
		return nil, fmt.Errorf("entry has no name")
	}
	return e, nil
}
