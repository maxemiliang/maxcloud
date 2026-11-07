package controller

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"

	"github.com/maxemiliang/maxcloud/controller/internal/naming"
)

// AppSync keeps a GitOps app's registry entry in line with the maxcloud.yaml
// in its repo: a push that changes the app's quota, buckets, database or domains
// reaches the cluster without anyone running mcloud, and without CI needing
// access to the cluster. Flux already fetches the app's repo (the registry's
// GitRepository for it, which includes maxcloud.yaml); this reads the file
// from Flux's artifact when its revision changes.
//
// It writes as Writer, a client impersonating the user maxcloud:app-sync, so
// the registry's admission ceiling applies as it would to a developer: an
// app's repo can't give itself more than that. Source settings (repo, path,
// branch, deploy) aren't taken from the file: those change with mcloud.
type AppSync struct {
	client.Client
	Writer   client.Client
	HTTP     *http.Client
	Recorder record.EventRecorder
}

var gitRepositoryGVK = schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"}

const (
	// SyncedRevisionAnnotation and SyncErrorAnnotation on a registry entry
	// say which maxcloud.yaml it was last brought in line with, and why the
	// last try didn't take.
	SyncedRevisionAnnotation = "maxcloud.gronblom.dev/synced-revision"
	SyncErrorAnnotation      = "maxcloud.gronblom.dev/sync-error"
	// SyncedFieldsAnnotation lists the registry keys the app's maxcloud.yaml
	// declares (comma-separated, sorted), so mcloud can refuse to set them by
	// hand: the next push would undo it.
	SyncedFieldsAnnotation = "maxcloud.gronblom.dev/synced-fields"
	projectFile            = "maxcloud.yaml"
	maxArtifactBytes       = 64 << 20
)

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=users,verbs=impersonate,resourceNames=maxcloud:app-sync

func (r *AppSync) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(gitRepositoryGVK)
	if err := r.Get(ctx, req.NamespacedName, repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	revision, _, _ := unstructured.NestedString(repo.Object, "status", "artifact", "revision")
	url, _, _ := unstructured.NestedString(repo.Object, "status", "artifact", "url")
	digest, _, _ := unstructured.NestedString(repo.Object, "status", "artifact", "digest")
	if revision == "" || url == "" {
		return ctrl.Result{}, nil // nothing fetched yet
	}

	entry := &unstructured.Unstructured{}
	entry.SetGroupVersionKind(providerGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: RegistryNamespace, Name: repo.GetName()}, entry); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Only entries mcloud manages: an app registered in the homelab repo
	// changes there.
	if !isMcloudEntry(entry) || entry.GetAnnotations()[SyncedRevisionAnnotation] == revision {
		return ctrl.Result{}, nil
	}
	logger := log.FromContext(ctx).WithValues("app", entry.GetName(), "revision", revision)

	data, found, err := r.fetchProjectFile(ctx, url, digest)
	if err != nil {
		return ctrl.Result{}, err // Flux's artifact is unreachable: try again
	}
	var changes, fields []string
	if found {
		p, err := parseProject(data)
		if err != nil {
			return ctrl.Result{}, r.fail(ctx, entry, revision, fmt.Sprintf("%s: %v", projectFile, err))
		}
		if p.App != "" && p.App != entry.GetName() {
			return ctrl.Result{}, r.fail(ctx, entry, revision, fmt.Sprintf("%s is for app %q, not %q", projectFile, p.App, entry.GetName()))
		}
		// Values the registry can't render would stop the one ResourceSet
		// that renders every app: keep the entry as it was instead.
		if err := p.validate(entry.GetName()); err != nil {
			return ctrl.Result{}, r.fail(ctx, entry, revision, fmt.Sprintf("%s: %v", projectFile, err))
		}
		values, _, _ := unstructured.NestedMap(entry.Object, "spec", "defaultValues")
		if values == nil {
			values = map[string]any{}
		}
		changes = p.apply(values)
		fields = p.declares()
		if len(changes) > 0 {
			want := entry.DeepCopy()
			if err := unstructured.SetNestedMap(want.Object, values, "spec", "defaultValues"); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Writer.Update(ctx, want); err != nil {
				if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
					// The ceiling (or a name rule) said no: report, don't retry
					// until the file changes.
					return ctrl.Result{}, r.fail(ctx, entry, revision, err.Error())
				}
				return ctrl.Result{}, err
			}
			logger.Info("registry entry synced from "+projectFile, "changes", changes)
			if err := r.Get(ctx, client.ObjectKeyFromObject(entry), entry); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Done with this revision.
	patch := client.MergeFrom(entry.DeepCopy())
	annotations := entry.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[SyncedRevisionAnnotation] = revision
	delete(annotations, SyncErrorAnnotation)
	if found {
		annotations[SyncedFieldsAnnotation] = strings.Join(fields, ",")
	} else {
		delete(annotations, SyncedFieldsAnnotation)
	}
	entry.SetAnnotations(annotations)
	if err := r.Patch(ctx, entry, patch); err != nil {
		return ctrl.Result{}, err
	}
	if len(changes) > 0 && r.Recorder != nil {
		r.Recorder.Eventf(entry, corev1.EventTypeNormal, "Synced", "%s at %s: %s", projectFile, revision, strings.Join(changes, ", "))
	}
	return ctrl.Result{}, nil
}

// fail records why the revision's maxcloud.yaml couldn't be applied, on the
// entry and as an event, and marks the revision done.
func (r *AppSync) fail(ctx context.Context, entry *unstructured.Unstructured, revision, why string) error {
	log.FromContext(ctx).Info("registry entry not synced", "app", entry.GetName(), "revision", revision, "reason", why)
	if r.Recorder != nil {
		r.Recorder.Eventf(entry, corev1.EventTypeWarning, "SyncFailed", "%s at %s: %s", projectFile, revision, why)
	}
	patch := client.MergeFrom(entry.DeepCopy())
	annotations := entry.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[SyncedRevisionAnnotation] = revision
	annotations[SyncErrorAnnotation] = why
	entry.SetAnnotations(annotations)
	return r.Patch(ctx, entry, patch)
}

// fetchProjectFile downloads Flux's artifact for the repo, checks its digest
// and returns maxcloud.yaml from it, if the repo has one.
func (r *AppSync) fetchProjectFile(ctx context.Context, url, digest string) ([]byte, bool, error) {
	httpc := r.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("fetching the artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetching the artifact: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes))
	if err != nil {
		return nil, false, err
	}
	if algo, want, ok := strings.Cut(digest, ":"); ok && algo == "sha256" {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want {
			return nil, false, errors.New("the artifact doesn't match its digest")
		}
	}
	return projectFileFrom(body)
}

func projectFileFrom(tgz []byte) ([]byte, bool, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(tgz)))
	if err != nil {
		return nil, false, fmt.Errorf("artifact: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("artifact: %w", err)
		}
		if strings.TrimPrefix(h.Name, "./") == projectFile && h.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			return data, true, err
		}
	}
}

// project is the part of maxcloud.yaml the sync takes (mcloud's
// internal/cli/root.go has the whole file).
type project struct {
	App string `json:"app"`
	// Deploy isn't applied (mcloud deploy switches it), but declaring it
	// makes it the file's, so mcloud apps set --deploy refuses it.
	Deploy string `json:"deploy"`
	Quota  *struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
		Pods   any    `json:"pods"`
	} `json:"quota"`
	Buckets []string        `json:"buckets"`
	DB      *projectDB      `json:"db"`
	Domains []projectDomain `json:"domains"`
}

// projectDomain is one of domains: {host, service, proxied}. Which
// hostnames the app may have is admission's call (the allowlist), checked
// when the entry is written.
type projectDomain struct {
	Host    string `json:"host"`
	Service string `json:"service"`
	Proxied bool   `json:"proxied"`
}

type projectDB struct {
	Enabled         bool
	Size, Instances string
}

func (d *projectDB) UnmarshalJSON(b []byte) error {
	var on bool
	if err := json.Unmarshal(b, &on); err == nil {
		d.Enabled = on
		return nil
	}
	var v struct {
		Size      string `json:"size"`
		Instances any    `json:"instances"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return errors.New("db: true, false, or {size: 2Gi, instances: 2}")
	}
	d.Enabled, d.Size = true, v.Size
	if v.Instances != nil {
		d.Instances = fmt.Sprint(v.Instances)
	}
	return nil
}

func parseProject(data []byte) (*project, error) {
	var p project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// validate checks what apply would write: each value must render into
// objects the API server accepts (the registry renders every app in one
// ResourceSet, so one bad entry stops them all). How much an app may ask
// for is the admission ceiling's call, not this.
func (p *project) validate(app string) error {
	if q := p.Quota; q != nil {
		for _, f := range []struct{ name, v string }{{"quota.cpu", q.CPU}, {"quota.memory", q.Memory}} {
			if f.v == "" {
				continue
			}
			if err := positiveQuantity(f.v); err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
		}
		if q.Pods != nil {
			if _, err := intInRange(q.Pods, 1, math.MaxInt32); err != nil {
				return fmt.Errorf("quota.pods: %w", err)
			}
		}
	}
	seen := map[string]bool{}
	for _, b := range p.Buckets {
		if strings.TrimSpace(b) == "" {
			continue // skipped, as mcloud and the registry's template do
		}
		name, opts, _ := strings.Cut(strings.TrimSpace(b), ":")
		if _, err := naming.Bucket(app, name); err != nil {
			return fmt.Errorf("buckets: %w", err)
		}
		if name == naming.DatabaseBucket {
			return fmt.Errorf("buckets: %q is reserved for the database's backups", name)
		}
		if opts != "" && opts != "public" {
			return fmt.Errorf("buckets: %q: the only option is :public", b)
		}
		if seen[name] {
			return fmt.Errorf("buckets: %q is listed twice", name)
		}
		seen[name] = true
	}
	if d := p.DB; d != nil && d.Enabled {
		if d.Size != "" {
			if err := positiveQuantity(d.Size); err != nil {
				return fmt.Errorf("db.size: %w", err)
			}
		}
		if d.Instances != "" {
			if _, err := intInRange(d.Instances, 1, 2); err != nil {
				return fmt.Errorf("db.instances: %w", err)
			}
		}
	}
	return nil
}

func positiveQuantity(s string) error {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return fmt.Errorf("%q isn't a quantity", s)
	}
	if q.Sign() <= 0 {
		return fmt.Errorf("%q: must be more than 0", s)
	}
	return nil
}

// intInRange reads a whole number, from YAML (a float64) or a string.
func intInRange(v any, lo, hi int) (int, error) {
	n, err := strconv.Atoi(fmt.Sprint(v))
	if err != nil {
		return 0, fmt.Errorf("%v isn't a whole number", v)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%d: must be from %d to %d", n, lo, hi)
	}
	return n, nil
}

// apply writes what the file says into an entry's values (what it leaves
// out stays, as with mcloud deploy) and returns what changed.
func (p *project) apply(values map[string]any) []string {
	var changes []string
	set := func(key, v string) {
		cur, _ := values[key].(string)
		if v == cur {
			return
		}
		if v == "" {
			delete(values, key)
		} else {
			values[key] = v
		}
		changes = append(changes, fmt.Sprintf("%s %q -> %q", key, cur, v))
	}
	if q := p.Quota; q != nil {
		if q.CPU != "" {
			set("cpu", q.CPU)
		}
		if q.Memory != "" {
			set("memory", q.Memory)
		}
		if q.Pods != nil {
			set("pods", fmt.Sprint(q.Pods))
		}
	}
	if p.Buckets != nil {
		parts := make([]string, 0, len(p.Buckets))
		for _, b := range p.Buckets {
			parts = append(parts, strings.TrimSpace(b))
		}
		set("buckets", strings.Join(parts, ","))
	}
	if p.Domains != nil {
		parts := make([]string, 0, len(p.Domains))
		for _, d := range p.Domains {
			v := strings.ToLower(strings.TrimSpace(d.Host)) + ":" + strings.TrimSpace(d.Service)
			if d.Proxied {
				v += ":proxied"
			}
			parts = append(parts, v)
		}
		set("domains", strings.Join(parts, ","))
	}
	if d := p.DB; d != nil {
		if d.Enabled {
			set("db", "true")
			if d.Size != "" {
				set("dbSize", d.Size)
			}
			if d.Instances != "" {
				set("dbInstances", d.Instances)
			}
		} else {
			set("db", "")
			set("dbSize", "")
			set("dbInstances", "")
		}
	}
	return changes
}

// declares is the registry keys the file sets, sorted. With db (true, false
// or a map) the file owns the whole database setting.
func (p *project) declares() []string {
	var keys []string
	if p.Deploy != "" {
		keys = append(keys, "deploy")
	}
	if q := p.Quota; q != nil {
		if q.CPU != "" {
			keys = append(keys, "cpu")
		}
		if q.Memory != "" {
			keys = append(keys, "memory")
		}
		if q.Pods != nil {
			keys = append(keys, "pods")
		}
	}
	if p.Buckets != nil {
		keys = append(keys, "buckets")
	}
	if p.DB != nil {
		keys = append(keys, "db", "dbSize", "dbInstances")
	}
	if p.Domains != nil {
		keys = append(keys, "domains")
	}
	slices.Sort(keys)
	return keys
}

func (r *AppSync) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gitRepositoryGVK)
	ours := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == RegistryNamespace && o.GetLabels()["app.kubernetes.io/part-of"] == "maxcloud"
	})
	newArtifact := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return artifactRevision(e.ObjectOld) != artifactRevision(e.ObjectNew)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(u, builder.WithPredicates(ours, newArtifact)).
		Named("appsync").
		Complete(r)
}

func artifactRevision(o client.Object) string {
	u, ok := o.(*unstructured.Unstructured)
	if !ok {
		return ""
	}
	rev, _, _ := unstructured.NestedString(u.Object, "status", "artifact", "revision")
	return rev
}
