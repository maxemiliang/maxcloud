package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/naming"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// An app's environments (staging, ...) and previews are apps of their own:
// a registry entry <app>--<env> each, which the registry turns into a
// namespace, quota, buckets, database and deploy like any app's. The
// difference is who makes them: only this controller, from what the app's
// own entry says (its environments, written from maxcloud.yaml by AppSync
// or mcloud deploy) and from the app's Previews. Developers can't make an
// entry with "--" in its name (admission), so an environment always
// belongs to the app before the "--", with the app's owners.
//
// The controller writes those entries as the user maxcloud:environments
// (impersonated), so admission's ceiling on what a developer may ask for
// applies to each one, as it does to the app's own entry.
const (
	// EnvironmentOfLabel, on an environment's entry: the app it belongs to.
	EnvironmentOfLabel = "run.gronblom.dev/environment-of"
	// EnvironmentLabel: the environment's own name (staging, pr-42).
	EnvironmentLabel = "run.gronblom.dev/environment"
	// EnvironmentKindLabel: environment (declared in maxcloud.yaml, kept
	// through a rebuild) or preview (short-lived, not kept).
	EnvironmentKindLabel = "run.gronblom.dev/environment-kind"
	KindEnvironment      = "environment"
	KindPreview          = "preview"
	// PullRequestsOfLabel marks the Flux Operator input provider that lists
	// an app's open pull requests labelled preview (the registry makes one
	// per app with previews on): <app>--pull-requests in flux-system.
	PullRequestsOfLabel = "run.gronblom.dev/pull-requests-of"
	pullRequestsSuffix  = "--pull-requests"
	// EnvironmentsErrorAnnotation, on an app's entry: why an environment it
	// declares can't be made.
	EnvironmentsErrorAnnotation = "maxcloud.gronblom.dev/environments-error"
	// EnvironmentsUser is who the controller writes environments' entries
	// as.
	EnvironmentsUser  = "maxcloud:environments"
	controllerManaged = "maxcloud-controller"

	// PreviewBranchPrefix: a pull request's preview deploys the branch
	// maxcloud/preview/<number>, which the app's deploy workflow writes.
	PreviewBranchPrefix = "maxcloud/preview/"
	// EnvironmentPathPrefix: an environment's manifests are
	// deploy/env/<env> on its own branch, so merging between branches never
	// conflicts on the deploy commits.
	EnvironmentPathPrefix = "deploy/env/"

	// MaxEnvironmentName keeps <service>--<app>--<env>.<domain> within DNS's
	// 63-character label; MaxEnvironmentFullName is <app>--<env>.
	MaxEnvironmentName     = 12
	MaxEnvironmentFullName = 40
	// PullRequestPrefix starts every pull request preview's name (pr-42);
	// declared environments can't use it.
	PullRequestPrefix = "pr-"

	defaultPreviewTTL = 72 * time.Hour
	defaultPreviewMax = 2
)

var envNameRe = regexp.MustCompile(`^[a-z]([a-z0-9]|-[a-z0-9])*$`)

// ValidEnvironmentName checks an environment's or preview's name for app.
func ValidEnvironmentName(app, env string) error {
	if !envNameRe.MatchString(env) || len(env) > MaxEnvironmentName {
		return fmt.Errorf("environment %q: lowercase letters, digits and single dashes, starting with a letter, at most %d characters", env, MaxEnvironmentName)
	}
	if n := len(naming.Environment(app, env)); n > MaxEnvironmentFullName {
		return fmt.Errorf("environment %q: %s is %d characters, at most %d (it's in every hostname)", env, naming.Environment(app, env), n, MaxEnvironmentFullName)
	}
	return nil
}

// isChildEntry: an environment's or preview's registry entry.
func isChildEntry(o client.Object) bool {
	return isAppEntry(o) && o.GetLabels()[EnvironmentOfLabel] != ""
}

// isEnvironmentEntry: a declared environment's entry (kept through a
// rebuild), not a preview's.
func isEnvironmentEntry(o client.Object) bool {
	return isChildEntry(o) && o.GetLabels()[EnvironmentKindLabel] == KindEnvironment
}

// PreviewQuota is what every preview gets: small, since previews come and
// go without anyone sizing them.
type PreviewQuota struct{ CPU, Memory, Pods, DBSize string }

// DefaultPreviewQuota: 100m CPU, 512Mi, 10 pods, a 1Gi database.
var DefaultPreviewQuota = PreviewQuota{CPU: "100m", Memory: "512Mi", Pods: "10", DBSize: "1Gi"}

// Environments keeps every app's environments' and previews' registry
// entries in line with the app's entry and its Previews, keeps pull
// requests' Previews in line with the app's open pull requests, and
// removes previews when they expire.
type Environments struct {
	client.Client
	// Writer writes the entries, impersonating EnvironmentsUser.
	Writer client.Client
	// Objects and Bucket: the registry's copies, so an environment made
	// again after a rebuild keeps its registration ID (and so its data).
	Objects  objectstorage.Objects
	Bucket   string
	Recorder record.EventRecorder
	// MaxEnvironments per app (declared ones; default 3) and MaxPreviews
	// on the whole platform (default 6).
	MaxEnvironments, MaxPreviews int
	Quota                        PreviewQuota
	Now                          func() time.Time
}

// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=previews,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=previews/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=users,verbs=impersonate,resourceNames=maxcloud:environments

func (r *Environments) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// envSpec is one environment as the app's entry declares it: entry keys
// (branch, cpu, memory, pods, buckets, db, dbSize, dbInstances, domains)
// that differ from the app's.
type envSpec map[string]string

var envSpecKeys = []string{"branch", "cpu", "memory", "pods", "buckets", "db", "dbSize", "dbInstances", "domains"}

// parseEnvironments reads an entry's environments value: JSON,
// {"staging": {"branch": "staging", "cpu": "100m"}}.
func parseEnvironments(app, raw string) (map[string]envSpec, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var envs map[string]envSpec
	if err := json.Unmarshal([]byte(raw), &envs); err != nil {
		return nil, fmt.Errorf("environments: not a JSON object of objects: %w", err)
	}
	for name, spec := range envs {
		if err := ValidEnvironmentName(app, name); err != nil {
			return nil, err
		}
		if strings.HasPrefix(name, PullRequestPrefix) {
			return nil, fmt.Errorf("environment %q: names starting with %s are pull requests' previews", name, PullRequestPrefix)
		}
		for k := range spec {
			if !slices.Contains(envSpecKeys, k) {
				return nil, fmt.Errorf("environment %s: unknown setting %q", name, k)
			}
		}
	}
	return envs, nil
}

// previewSettings are the app entry's preview keys.
type previewSettings struct {
	On  bool
	TTL time.Duration
	Max int
	DB  bool
}

func parsePreviewSettings(values map[string]any) (previewSettings, error) {
	s := previewSettings{TTL: defaultPreviewTTL, Max: defaultPreviewMax}
	str := func(k string) string { v, _ := values[k].(string); return v }
	s.On = str("previews") == "true"
	s.DB = str("previewDB") == "true"
	if v := str("previewTTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("previewTTL %q: a duration such as 72h", v)
		}
		s.TTL = d
	}
	if v := str("previewMax"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return s, fmt.Errorf("previewMax %q: a whole number", v)
		}
		s.Max = n
	}
	return s, nil
}

// child is an environment's entry as it should be.
type child struct {
	Env, Kind string
	Values    map[string]any
}

func (c child) labels(app string) map[string]string {
	return map[string]string{
		appLabel:             "true",
		EnvironmentOfLabel:   app,
		EnvironmentLabel:     c.Env,
		EnvironmentKindLabel: c.Kind,
		ManagedByLabel:       controllerManaged,
	}
}

// inherited are the app's keys an environment shares unless it says
// otherwise; never domains (hostnames are an admin's grant per entry).
var inherited = []string{"cpu", "memory", "pods", "buckets"}

// environmentValues is an environment's entry values: the app's source and
// owners, its own branch and manifests, the app's quota, buckets and
// database unless it declares its own.
func environmentValues(app string, parent map[string]any, env string, spec envSpec) map[string]any {
	str := func(k string) string { v, _ := parent[k].(string); return v }
	v := map[string]any{"name": naming.Environment(app, env)}
	for _, k := range []string{"owners", "deploy"} {
		if s := str(k); s != "" {
			v[k] = s
		}
	}
	if str("deploy") != "direct" {
		if s := str("repo"); s != "" {
			v["repo"] = s
		}
		branch := spec["branch"]
		if branch == "" {
			branch = env
		}
		v["branch"] = branch
		v["path"] = EnvironmentPathPrefix + env
	}
	for _, k := range inherited {
		if s, ok := spec[k]; ok {
			if s != "" {
				v[k] = s
			}
		} else if s := str(k); s != "" {
			v[k] = s
		}
	}
	// The database as a whole: the environment's own setting, else the app's.
	src := map[string]string{"db": str("db"), "dbSize": str("dbSize"), "dbInstances": str("dbInstances")}
	if _, ok := spec["db"]; ok {
		src = map[string]string{"db": spec["db"], "dbSize": spec["dbSize"], "dbInstances": spec["dbInstances"]}
	}
	if src["db"] == "true" {
		for k, s := range src {
			if s != "" {
				v[k] = s
			}
		}
	}
	if s := spec["domains"]; s != "" {
		v["domains"] = s
	}
	return v
}

// previewValues is a preview's entry values: the app's owners and bucket
// names (empty buckets of its own), the preview quota, an empty database
// if the app asks for one, and everything deleted with it.
func previewValues(app string, parent map[string]any, p *v1.Preview, s previewSettings, q PreviewQuota) map[string]any {
	str := func(k string) string { v, _ := parent[k].(string); return v }
	v := map[string]any{
		"name":    naming.Environment(app, p.Name),
		"cpu":     q.CPU,
		"memory":  q.Memory,
		"pods":    q.Pods,
		"reclaim": "Delete",
	}
	if s := str("owners"); s != "" {
		v["owners"] = s
	}
	if p.Spec.PullRequest > 0 {
		v["repo"] = str("repo")
		v["branch"] = PreviewBranchPrefix + strconv.FormatInt(p.Spec.PullRequest, 10)
		v["path"] = "deploy/k8s"
	} else {
		v["deploy"] = "direct"
	}
	if b := str("buckets"); b != "" {
		v["buckets"] = b
	}
	if s.DB {
		v["db"], v["dbSize"], v["dbInstances"] = "true", q.DBSize, "1"
	}
	return v
}

func (r *Environments) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	app := req.Name
	logger := log.FromContext(ctx).WithValues("app", app)
	now := r.now()

	parent := &unstructured.Unstructured{}
	parent.SetGroupVersionKind(providerGVK)
	getErr := r.Get(ctx, types.NamespacedName{Namespace: RegistryNamespace, Name: app}, parent)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return ctrl.Result{}, getErr
	}
	existing, err := r.children(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if apierrors.IsNotFound(getErr) || !parent.GetDeletionTimestamp().IsZero() || !isAppEntry(parent) {
		// The app is gone: so are its environments.
		return ctrl.Result{}, r.prune(ctx, existing, nil)
	}
	if isChildEntry(parent) {
		return ctrl.Result{}, nil
	}
	values, _, _ := unstructured.NestedMap(parent.Object, "spec", "defaultValues")
	str := func(k string) string { v, _ := values[k].(string); return v }

	var problems []string
	envs, err := parseEnvironments(app, str("environments"))
	if err != nil {
		problems = append(problems, err.Error())
	}
	if max := r.maxEnvironments(); len(envs) > max {
		problems = append(problems, fmt.Sprintf("environments: at most %d (ask an admin for more)", max))
		names := slices.Sorted(maps.Keys(envs))
		for _, n := range names[max:] {
			delete(envs, n)
		}
	}
	settings, err := parsePreviewSettings(values)
	if err != nil {
		problems = append(problems, err.Error())
	}

	want := map[string]child{}
	for name, spec := range envs {
		want[naming.Environment(app, name)] = child{Env: name, Kind: KindEnvironment, Values: environmentValues(app, values, name, spec)}
	}
	if _, declared := values["environments"]; !declared {
		// No environments key at all (an mcloud older than environments
		// rewrote the entry without it, or the app never had any): the
		// environments stay as they are. Removing them takes
		// environments: {} in maxcloud.yaml, or deleting the app.
		for i := range existing {
			if e := &existing[i]; isEnvironmentEntry(e) {
				v, _, _ := unstructured.NestedMap(e.Object, "spec", "defaultValues")
				want[e.GetName()] = child{Env: e.GetLabels()[EnvironmentLabel], Kind: KindEnvironment, Values: v}
			}
		}
	}

	if err := r.syncPullRequests(ctx, app, str("repo"), settings, now); err != nil {
		return ctrl.Result{}, err
	}
	previews, next, err := r.previews(ctx, app, values, envs, settings, existing, now, want)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.apply(ctx, parent, want, existing); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.prune(ctx, existing, want); err != nil {
		return ctrl.Result{}, err
	}
	for i := range previews {
		if err := r.Status().Update(ctx, &previews[i]); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}
	if err := r.report(ctx, parent, strings.Join(problems, "; ")); err != nil {
		return ctrl.Result{}, err
	}
	if len(problems) > 0 {
		logger.Info("environments not as declared", "problems", problems)
	}
	// Look again at the next expiry, and every few minutes anyway.
	after := 5 * time.Minute
	if !next.IsZero() && next.Sub(now) < after {
		after = max(next.Sub(now), time.Second)
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *Environments) maxEnvironments() int {
	if r.MaxEnvironments > 0 {
		return r.MaxEnvironments
	}
	return 3
}

func (r *Environments) maxPreviews() int {
	if r.MaxPreviews > 0 {
		return r.MaxPreviews
	}
	return 6
}

func (r *Environments) quota() PreviewQuota {
	if r.Quota.CPU == "" {
		return DefaultPreviewQuota
	}
	return r.Quota
}

// children are app's environments' and previews' entries.
func (r *Environments) children(ctx context.Context, app string) ([]unstructured.Unstructured, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(providerGVK.GroupVersion().WithKind(providerGVK.Kind + "List"))
	if err := r.List(ctx, &list, client.InNamespace(RegistryNamespace), client.MatchingLabels{EnvironmentOfLabel: app}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// syncPullRequests keeps the app's pull request Previews in line with its
// open pull requests labelled preview, as the Flux Operator's input
// provider last listed them: one Preview pr-<n> each, extended on every
// new head commit, deleted when the pull request is no longer listed. With
// previews off, there are none. A provider that isn't Ready (GitHub
// unreachable) changes nothing.
func (r *Environments) syncPullRequests(ctx context.Context, app, repo string, s previewSettings, now time.Time) error {
	var list v1.PreviewList
	if err := r.List(ctx, &list, client.InNamespace(app)); err != nil {
		return err
	}
	have := map[int64]*v1.Preview{}
	for i := range list.Items {
		if n := list.Items[i].Spec.PullRequest; n > 0 {
			have[n] = &list.Items[i]
		}
	}
	if !s.On {
		for _, p := range have {
			if err := r.Delete(ctx, p); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
		return nil
	}
	provider := &unstructured.Unstructured{}
	provider.SetGroupVersionKind(providerGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: RegistryNamespace, Name: app + pullRequestsSuffix}, provider)
	if apierrors.IsNotFound(err) {
		return nil // a direct app, or not made yet
	}
	if err != nil {
		return err
	}
	// Only the provider the registry makes for this app, for its repo
	// (admission lets nobody else write one; this is the second check).
	typ, _, _ := unstructured.NestedString(provider.Object, "spec", "type")
	url, _, _ := unstructured.NestedString(provider.Object, "spec", "url")
	if provider.GetLabels()[PullRequestsOfLabel] != app || typ != "GitHubPullRequest" || repo == "" ||
		strings.TrimSuffix(url, ".git") != "https://github.com/"+repo || !providerReady(provider) {
		return nil
	}
	open := openPullRequests(provider)
	for n, sha := range open {
		name := PullRequestPrefix + strconv.FormatInt(n, 10)
		if len(name) > MaxEnvironmentName {
			continue
		}
		p, ok := have[n]
		expires := metav1.NewTime(now.Add(s.TTL))
		if !ok {
			p = &v1.Preview{
				ObjectMeta: metav1.ObjectMeta{Namespace: app, Name: name},
				Spec:       v1.PreviewSpec{PullRequest: n, Revision: sha, ExpiresAt: &expires},
			}
			if err := r.Create(ctx, p); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			log.FromContext(ctx).Info("preview for a pull request", "app", app, "pullRequest", n, "revision", sha)
			continue
		}
		if p.Spec.Revision != sha {
			p.Spec.Revision, p.Spec.ExpiresAt = sha, &expires
			if err := r.Update(ctx, p); err != nil {
				return err
			}
		}
	}
	for n, p := range have {
		if _, ok := open[n]; !ok {
			if err := r.Delete(ctx, p); client.IgnoreNotFound(err) != nil {
				return err
			}
			log.FromContext(ctx).Info("pull request closed or unlabelled: preview removed", "app", app, "pullRequest", n)
		}
	}
	return nil
}

func providerReady(u *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" {
			return m["status"] == "True"
		}
	}
	return false
}

// openPullRequests reads the provider's exported inputs: pull request
// number to head commit.
func openPullRequests(u *unstructured.Unstructured) map[int64]string {
	out := map[int64]string{}
	inputs, _, _ := unstructured.NestedSlice(u.Object, "status", "exportedInputs")
	for _, in := range inputs {
		m, _ := in.(map[string]any)
		n, err := strconv.ParseInt(fmt.Sprint(m["id"]), 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		sha, _ := m["sha"].(string)
		out[n] = sha
	}
	return out
}

// previews decides each of the app's Previews' fate, adds the ones that get
// an environment to want, and returns them with their new status, and the
// next expiry. Expired working-tree previews are deleted.
func (r *Environments) previews(ctx context.Context, app string, values map[string]any, envs map[string]envSpec,
	s previewSettings, existing []unstructured.Unstructured, now time.Time, want map[string]child) ([]v1.Preview, time.Time, error) {
	var list v1.PreviewList
	if err := r.List(ctx, &list, client.InNamespace(app)); err != nil {
		return nil, time.Time{}, err
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].CreationTimestamp, items[j].CreationTimestamp
		if !a.Equal(&b) {
			return a.Before(&b)
		}
		return items[i].Name < items[j].Name
	})
	// What the platform has room for: the other apps' previews count.
	var all unstructured.UnstructuredList
	all.SetGroupVersionKind(providerGVK.GroupVersion().WithKind(providerGVK.Kind + "List"))
	if err := r.List(ctx, &all, client.InNamespace(RegistryNamespace), client.MatchingLabels{EnvironmentKindLabel: KindPreview}); err != nil {
		return nil, time.Time{}, err
	}
	others := 0
	for _, e := range all.Items {
		if e.GetLabels()[EnvironmentOfLabel] != app {
			others++
		}
	}
	room := min(s.Max, r.maxPreviews()-others)

	var out []v1.Preview
	var next time.Time
	active := 0
	for i := range items {
		p := items[i]
		if !p.DeletionTimestamp.IsZero() {
			continue
		}
		before := p.Status.DeepCopy()
		expired := p.Spec.ExpiresAt != nil && !p.Spec.ExpiresAt.After(now)
		switch {
		case expired && p.Spec.PullRequest == 0:
			if err := r.Delete(ctx, &p); client.IgnoreNotFound(err) != nil {
				return nil, time.Time{}, err
			}
			log.FromContext(ctx).Info("preview expired: removed", "app", app, "preview", p.Name)
			continue
		case expired:
			setPreview(&p, v1.PreviewExpired, "Expired", "expired at "+p.Spec.ExpiresAt.UTC().Format(time.RFC3339)+"; the pull request's next push brings it back")
		case envs[p.Name] != nil:
			setPreview(&p, v1.PreviewWaiting, "NameTaken", "the app declares an environment "+p.Name+"; give the preview another name")
		case !s.On:
			setPreview(&p, v1.PreviewWaiting, "PreviewsOff", "previews are off for "+app+": previews: in its maxcloud.yaml turns them on")
		case p.Spec.PullRequest > 0 && str(values, "deploy") == "direct":
			setPreview(&p, v1.PreviewWaiting, "PreviewsOff", app+" deploys directly: pull requests get no previews (mcloud preview deploy does)")
		case active >= room:
			setPreview(&p, v1.PreviewWaiting, "OverLimit", fmt.Sprintf("%s has %d previews already (previews.max), or the platform has as many as it allows; delete one, or wait for one to expire", app, active))
		case ValidEnvironmentName(app, p.Name) != nil:
			setPreview(&p, v1.PreviewWaiting, "NameTaken", ValidEnvironmentName(app, p.Name).Error())
		default:
			active++
			name := naming.Environment(app, p.Name)
			want[name] = child{Env: p.Name, Kind: KindPreview, Values: previewValues(app, values, &p, s, r.quota())}
			p.Status.Environment = name
			setPreview(&p, v1.PreviewActive, "Active", "deployed as "+name)
		}
		if p.Spec.ExpiresAt != nil && p.Spec.ExpiresAt.After(now) && (next.IsZero() || p.Spec.ExpiresAt.Time.Before(next)) {
			next = p.Spec.ExpiresAt.Time
		}
		if !equality.Semantic.DeepEqual(before, &p.Status) {
			out = append(out, p)
		}
	}
	return out, next, nil
}

func str(values map[string]any, k string) string { v, _ := values[k].(string); return v }

func setPreview(p *v1.Preview, phase v1.PreviewPhase, reason, msg string) {
	if phase != v1.PreviewActive {
		p.Status.Environment = ""
	}
	p.Status.Phase = phase
	p.Status.ObservedGeneration = p.Generation
	status := metav1.ConditionFalse
	if phase == v1.PreviewActive {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
}

// apply creates and updates the entries in want. A new environment (not a
// preview) whose entry has a copy in the registry's bucket gets that
// copy's registration ID, so after a rebuild it's the same registration
// and gets its buckets and database back.
func (r *Environments) apply(ctx context.Context, parent *unstructured.Unstructured, want map[string]child, existing []unstructured.Unstructured) error {
	app := parent.GetName()
	have := map[string]*unstructured.Unstructured{}
	for i := range existing {
		have[existing[i].GetName()] = &existing[i]
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		c := want[name]
		cur := have[name]
		if cur == nil {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(providerGVK)
			u.SetNamespace(RegistryNamespace)
			u.SetName(name)
			// No owner reference to the app's entry: the garbage collector
			// couldn't delete it through admission (only the controller
			// may); Reconcile removes environments when the app goes.
			u.SetLabels(c.labels(app))
			if c.Kind == KindEnvironment {
				if id, err := r.copiedRegistration(ctx, name); err != nil {
					return err
				} else if id != "" {
					u.SetAnnotations(map[string]string{RegistrationAnnotation: id})
				}
			}
			u.Object["spec"] = map[string]any{"type": "Static", "defaultValues": c.Values}
			if err := r.Writer.Create(ctx, u); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue // a developer's entry by that name can't exist; the restore loop's can
				}
				r.event(parent, corev1.EventTypeWarning, "EnvironmentRefused", "%s: %v", name, err)
				if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
					continue // admission said no; reported, retried with the next change
				}
				return err
			}
			r.event(parent, corev1.EventTypeNormal, "EnvironmentCreated", "%s (%s)", name, c.Kind)
			continue
		}
		values, _, _ := unstructured.NestedMap(cur.Object, "spec", "defaultValues")
		labels := cur.GetLabels()
		sameLabels := true
		for k, v := range c.labels(app) {
			if labels[k] != v {
				sameLabels = false
			}
		}
		if equalValues(values, c.Values) && sameLabels {
			continue
		}
		u := cur.DeepCopy()
		for k, v := range c.labels(app) {
			labels[k] = v
		}
		u.SetLabels(labels)
		if err := unstructured.SetNestedMap(u.Object, c.Values, "spec", "defaultValues"); err != nil {
			return err
		}
		if err := r.Writer.Update(ctx, u); err != nil {
			r.event(parent, corev1.EventTypeWarning, "EnvironmentRefused", "%s: %v", name, err)
			if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsConflict(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func equalValues(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if fmt.Sprint(v) != fmt.Sprint(b[k]) {
			return false
		}
	}
	return true
}

// copiedRegistration is the registration ID of the entry's copy in the
// registry's bucket, if it has one.
func (r *Environments) copiedRegistration(ctx context.Context, name string) (string, error) {
	if r.Objects == nil {
		return "", nil
	}
	data, found, err := r.Objects.GetObject(ctx, r.Bucket, backupKey(name))
	if err != nil || !found {
		return "", err
	}
	var u unstructured.Unstructured
	if err := yaml.Unmarshal(data, &u.Object); err != nil {
		return "", nil // unreadable: the entry gets a new ID
	}
	return u.GetAnnotations()[RegistrationAnnotation], nil
}

// prune deletes the entries not in want. Deleting an entry deletes its
// namespace; a preview's buckets and database go with it (reclaim Delete),
// an environment's stay in Object Storage for its registration.
func (r *Environments) prune(ctx context.Context, existing []unstructured.Unstructured, want map[string]child) error {
	for i := range existing {
		e := &existing[i]
		if _, ok := want[e.GetName()]; ok || !e.GetDeletionTimestamp().IsZero() {
			continue
		}
		if err := r.Writer.Delete(ctx, e); client.IgnoreNotFound(err) != nil {
			return err
		}
		log.FromContext(ctx).Info("environment removed", "entry", e.GetName())
	}
	return nil
}

// report puts why declared environments can't be made on the app's entry
// (and as an event), or takes it off.
func (r *Environments) report(ctx context.Context, parent *unstructured.Unstructured, problem string) error {
	if parent.GetAnnotations()[EnvironmentsErrorAnnotation] == problem {
		return nil
	}
	patch := client.MergeFrom(parent.DeepCopy())
	a := parent.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	if problem == "" {
		delete(a, EnvironmentsErrorAnnotation)
	} else {
		a[EnvironmentsErrorAnnotation] = problem
		r.event(parent, corev1.EventTypeWarning, "EnvironmentsInvalid", "%s", problem)
	}
	parent.SetAnnotations(a)
	return client.IgnoreNotFound(r.Patch(ctx, parent, patch))
}

func (r *Environments) event(o *unstructured.Unstructured, kind, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(o, kind, reason, format, args...)
	}
}

// appOf is the app whose environments a change to o concerns.
func appOf(o client.Object) string {
	switch {
	case o.GetNamespace() == RegistryNamespace && o.GetLabels()[PullRequestsOfLabel] != "":
		return o.GetLabels()[PullRequestsOfLabel]
	case isChildEntry(o):
		return o.GetLabels()[EnvironmentOfLabel]
	case isAppEntry(o):
		return o.GetName()
	}
	if _, isPreview := o.(*v1.Preview); isPreview {
		if _, _, isEnv := naming.SplitEnvironment(o.GetNamespace()); !isEnv {
			return o.GetNamespace()
		}
	}
	return ""
}

func (r *Environments) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	toApp := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if app := appOf(o); app != "" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: app}}}
		}
		return nil
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("environments").
		Watches(u, toApp).
		Watches(&v1.Preview{}, toApp).
		Complete(r)
}
