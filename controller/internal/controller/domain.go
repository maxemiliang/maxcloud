package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
)

// Domains puts an app's Knative service on a custom hostname (a Domain).
//
// The hostname gets an HTTPRoute of its own on the public Gateway, in the
// app's namespace, whose rules are a copy of Knative's route for the
// service's cluster-local name: the same revisions, weights and headers
// (Knative-Serving-Revision, which the activator needs while the service
// scales from zero). When Knative changes its route (a new revision), the
// copy follows. So the request reaches the revision in one hop with its
// Host header untouched, and the service may stay private.
//
// Not a Knative DomainMapping: it needs a cluster-scoped ClusterDomainClaim
// per hostname, and its route rewrites the Host to the service's
// cluster-local name and goes through the Gateway a second time (the app
// sees Host: web.shop.svc.cluster.local, the original only in
// K-Original-Host).
//
// Which hostnames an app may have is an admin's call: the allowlist
// ConfigMap (one key per app, comma-separated hostnames or *.zone
// wildcards). Admission checks the registry entries and every route in an
// app namespace against the same ConfigMap; this checks it again, so taking
// a hostname off the list takes the route down too.
type Domains struct {
	client.Client
	// Reader reads the allowlist and the Gateway straight from the API
	// server, so the controller doesn't cache every ConfigMap and Gateway.
	Reader client.Reader

	// Allowlist is the ConfigMap namespace/name.
	AllowlistNamespace, Allowlist string
	// Gateway is the public Gateway the routes attach to; the listener is
	// the HTTPS one whose hostname covers the domain.
	GatewayNamespace, Gateway string
}

var gatewayGVK = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}

// Knative's labels on the HTTPRoutes net-gateway-api makes for a service.
const (
	knativeRouteLabel      = "serving.knative.dev/route"
	knativeVisibilityLabel = "networking.knative.dev/visibility"
	knativeProbeHeader     = "K-Network-Hash"
	proxiedAnnotation      = "external-dns.kubernetes.io/cloudflare-proxied"
	// DomainServiceLabel on a Domain's HTTPRoute names its Knative service.
	DomainServiceLabel = "maxcloud.gronblom.dev/domain-service"
	// DomainRoutePrefix starts the name of a Domain's HTTPRoute. Knative
	// names its public routes after their hostname, so a bare hostname
	// could collide with one.
	DomainRoutePrefix = "domain-"
)

// Reasons a Domain isn't Ready.
const (
	ReasonNotAllowed     = "NotAllowed"
	ReasonNoListener     = "NoListener"
	ReasonNoService      = "ServiceNotFound"
	ReasonHostInUse      = "HostInUse"
	domainResync         = 5 * time.Minute
	domainRetryHostInUse = time.Minute
)

// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=domains,verbs=get;list;watch
// +kubebuilder:rbac:groups=maxcloud.gronblom.dev,resources=domains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get

func (r *Domains) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var d v1.Domain
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !d.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // the route is owned by it: garbage collection removes it
	}
	host, app := d.Name, d.Namespace
	// What the status was, so it's only written when it changes.
	before := d.Status.DeepCopy()

	tenant, err := isTenant(ctx, r.Client, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !tenant {
		return r.notReady(ctx, &d, before, ReasonNotTenant, fmt.Sprintf("namespace %s isn't an app on the run platform (no %s label)", app, TenantLabel))
	}

	allowed, err := r.allowed(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !HostAllowed(host, allowed) {
		return r.notReady(ctx, &d, before, ReasonNotAllowed, fmt.Sprintf("%s isn't among the hostnames an admin allowed app %s (ConfigMap %s/%s)", host, app, r.AllowlistNamespace, r.Allowlist))
	}

	listener, err := r.listener(ctx, host)
	if err != nil {
		return ctrl.Result{}, err
	}
	if listener == "" {
		return r.notReady(ctx, &d, before, ReasonNoListener, fmt.Sprintf("no HTTPS listener on Gateway %s/%s has a certificate for %s", r.GatewayNamespace, r.Gateway, host))
	}

	if other, err := r.routedElsewhere(ctx, &d); err != nil {
		return ctrl.Result{}, err
	} else if other != "" {
		res, err := r.notReady(ctx, &d, before, ReasonHostInUse, fmt.Sprintf("%s is already routed by HTTPRoute %s; remove that one first", host, other))
		if err == nil {
			res.RequeueAfter = domainRetryHostInUse
		}
		return res, err
	}

	rules, revisions, err := r.serviceRules(ctx, app, d.Spec.Service)
	if err != nil {
		return ctrl.Result{}, err
	}
	if rules == nil {
		return r.notReady(ctx, &d, before, ReasonNoService, fmt.Sprintf("Knative service %s has no route yet (is it deployed and ready?)", d.Spec.Service))
	}

	if err := r.ensureRoute(ctx, &d, listener, rules); err != nil {
		return ctrl.Result{}, err
	}
	d.Status.URL = "https://" + host + "/"
	d.Status.Listener = listener
	d.Status.Revisions = revisions
	return ctrl.Result{RequeueAfter: domainResync}, r.setStatus(ctx, &d, before, metav1.ConditionTrue, ReasonReady, fmt.Sprintf("%s routes to Knative service %s", host, d.Spec.Service))
}

// allowed is the app's line of the allowlist.
func (r *Domains) allowed(ctx context.Context, app string) ([]string, error) {
	var cm corev1.ConfigMap
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: r.AllowlistNamespace, Name: r.Allowlist}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return splitList(cm.Data[app]), nil
}

// splitList reads one app's allowlist value: comma-separated, spaces
// around the items ignored (as admission's CEL reads it).
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// HostAllowed reports whether host is on the list: listed as it is, or
// one level below a "*.zone" entry (as a wildcard certificate covers it).
// Admission (the homelab's registry-admission.yaml) applies the same rule.
func HostAllowed(host string, patterns []string) bool {
	for _, p := range patterns {
		if wildcardCovers(p, host) {
			return true
		}
	}
	return false
}

// wildcardCovers: pattern is host itself, or *.zone with host exactly one
// label below zone.
func wildcardCovers(pattern, host string) bool {
	if pattern == host {
		return true
	}
	zone, ok := strings.CutPrefix(pattern, "*.")
	if !ok {
		return false
	}
	label, ok := strings.CutSuffix(host, "."+zone)
	return ok && label != "" && !strings.Contains(label, ".")
}

// listener is the Gateway's HTTPS listener for host: one for exactly that
// hostname, else the one with the longest wildcard that covers it (one
// level, like its certificate). "" if there's none.
func (r *Domains) listener(ctx context.Context, host string) (string, error) {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: r.GatewayNamespace, Name: r.Gateway}, gw); err != nil {
		return "", fmt.Errorf("gateway %s/%s: %w", r.GatewayNamespace, r.Gateway, err)
	}
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	best, bestLen := "", -1
	for _, l := range listeners {
		m, _ := l.(map[string]any)
		name, _ := m["name"].(string)
		proto, _ := m["protocol"].(string)
		hostname, _ := m["hostname"].(string)
		if proto != "HTTPS" || hostname == "" || !wildcardCovers(hostname, host) {
			continue
		}
		n := len(hostname)
		if hostname == host {
			n = 1 << 20 // exact beats any wildcard
		}
		if n > bestLen {
			best, bestLen = name, n
		}
	}
	return best, nil
}

// routedElsewhere names another HTTPRoute on the Gateway that already has
// the hostname ("namespace/name"), or "". The Gateway API gives a host to
// the oldest route, so a new one would silently get nothing.
func (r *Domains) routedElsewhere(ctx context.Context, d *v1.Domain) (string, error) {
	var routes unstructured.UnstructuredList
	routes.SetGroupVersionKind(httpRouteGVK.GroupVersion().WithKind("HTTPRouteList"))
	if err := r.List(ctx, &routes); err != nil {
		return "", err
	}
	for _, rt := range routes.Items {
		if rt.GetNamespace() == d.Namespace && rt.GetName() == DomainRoutePrefix+d.Name {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
		if slices.Contains(hosts, d.Name) && r.onGateway(&rt) {
			return rt.GetNamespace() + "/" + rt.GetName(), nil
		}
	}
	return "", nil
}

func (r *Domains) onGateway(rt *unstructured.Unstructured) bool {
	parents, _, _ := unstructured.NestedSlice(rt.Object, "spec", "parentRefs")
	for _, p := range parents {
		m, _ := p.(map[string]any)
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = rt.GetNamespace()
		}
		if m["name"] == r.Gateway && ns == r.GatewayNamespace {
			return true
		}
	}
	return false
}

// serviceRules copies the rules of Knative's route for the service's
// cluster-local hostname, without the rule net-gateway-api's prober uses.
// nil if Knative hasn't made the route (yet).
func (r *Domains) serviceRules(ctx context.Context, app, service string) ([]any, []string, error) {
	var routes unstructured.UnstructuredList
	routes.SetGroupVersionKind(httpRouteGVK.GroupVersion().WithKind("HTTPRouteList"))
	if err := r.List(ctx, &routes, client.InNamespace(app), client.MatchingLabels{
		knativeRouteLabel: service, knativeVisibilityLabel: "cluster-local",
	}); err != nil {
		return nil, nil, err
	}
	local := service + "." + app + ".svc.cluster.local"
	for _, rt := range routes.Items {
		hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
		if !slices.Contains(hosts, local) {
			continue // a traffic tag's route
		}
		src, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
		rules := []any{}
		var revisions []string
		for _, rule := range src {
			m, _ := rule.(map[string]any)
			if isProbeRule(m) {
				continue
			}
			rules = append(rules, m)
			refs, _ := m["backendRefs"].([]any)
			for _, ref := range refs {
				if name, _ := ref.(map[string]any)["name"].(string); name != "" && !slices.Contains(revisions, name) {
					revisions = append(revisions, name)
				}
			}
		}
		if len(rules) == 0 {
			return nil, nil, nil
		}
		slices.Sort(revisions)
		return rules, revisions, nil
	}
	return nil, nil, nil
}

// isProbeRule: net-gateway-api's rule for its readiness probe, which
// matches the header K-Network-Hash: override.
func isProbeRule(rule map[string]any) bool {
	matches, _ := rule["matches"].([]any)
	for _, m := range matches {
		headers, _ := m.(map[string]any)["headers"].([]any)
		for _, h := range headers {
			if name, _ := h.(map[string]any)["name"].(string); strings.EqualFold(name, knativeProbeHeader) {
				return true
			}
		}
	}
	return false
}

func (r *Domains) ensureRoute(ctx context.Context, d *v1.Domain, listener string, rules []any) error {
	want := &unstructured.Unstructured{}
	want.SetGroupVersionKind(httpRouteGVK)
	want.SetNamespace(d.Namespace)
	want.SetName(DomainRoutePrefix + d.Name)
	want.SetLabels(map[string]string{ManagedByLabel: managedBy, DomainServiceLabel: d.Spec.Service})
	if d.Spec.Proxied {
		want.SetAnnotations(map[string]string{proxiedAnnotation: "true"})
	}
	want.Object["spec"] = map[string]any{
		"parentRefs": []any{map[string]any{
			"group": "gateway.networking.k8s.io", "kind": "Gateway",
			"name": r.Gateway, "namespace": r.GatewayNamespace, "sectionName": listener,
		}},
		"hostnames": []any{d.Name},
		"rules":     rules,
	}
	if err := controllerutil.SetControllerReference(d, want, r.Scheme()); err != nil {
		return err
	}

	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(httpRouteGVK)
	err := r.Get(ctx, client.ObjectKeyFromObject(want), cur)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, want)
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(cur, d) {
		return fmt.Errorf("HTTPRoute %s/%s exists and isn't this Domain's", cur.GetNamespace(), cur.GetName())
	}
	ann := cur.GetAnnotations()
	if (ann[proxiedAnnotation] == "true") == d.Spec.Proxied &&
		equality.Semantic.DeepEqual(cur.Object["spec"], want.Object["spec"]) &&
		equality.Semantic.DeepEqual(cur.GetLabels(), want.GetLabels()) {
		return nil
	}
	cur.Object["spec"] = want.Object["spec"]
	cur.SetLabels(want.GetLabels())
	if ann == nil {
		ann = map[string]string{}
	}
	if d.Spec.Proxied {
		ann[proxiedAnnotation] = "true"
	} else {
		delete(ann, proxiedAnnotation)
	}
	cur.SetAnnotations(ann)
	return r.Update(ctx, cur)
}

func (r *Domains) removeRoute(ctx context.Context, d *v1.Domain) error {
	rt := &unstructured.Unstructured{}
	rt.SetGroupVersionKind(httpRouteGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: DomainRoutePrefix + d.Name}, rt); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(rt, d) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, rt))
}

// notReady takes the hostname's route down and says why.
func (r *Domains) notReady(ctx context.Context, d *v1.Domain, before *v1.DomainStatus, reason, msg string) (ctrl.Result, error) {
	if err := r.removeRoute(ctx, d); err != nil {
		return ctrl.Result{}, err
	}
	d.Status.URL, d.Status.Listener, d.Status.Revisions = "", "", nil
	return ctrl.Result{RequeueAfter: domainResync}, r.setStatus(ctx, d, before, metav1.ConditionFalse, reason, msg)
}

// setStatus writes d's status, unless it's still what it was (before).
func (r *Domains) setStatus(ctx context.Context, d *v1.Domain, before *v1.DomainStatus, status metav1.ConditionStatus, reason, msg string) error {
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: msg, ObservedGeneration: d.Generation,
	})
	d.Status.ObservedGeneration = d.Generation
	if equality.Semantic.DeepEqual(before, &d.Status) {
		return nil
	}
	return r.Status().Update(ctx, d)
}

func (r *Domains) SetupWithManager(mgr ctrl.Manager) error {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK)
	knativeRoute := &unstructured.Unstructured{}
	knativeRoute.SetGroupVersionKind(httpRouteGVK)
	isKnativeLocal := predicate.NewPredicateFuncs(func(o client.Object) bool {
		l := o.GetLabels()
		return l[knativeRouteLabel] != "" && l[knativeVisibilityLabel] == "cluster-local"
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Domain{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Its own route, changed or deleted by hand: put it back.
		Owns(route).
		// Knative's route for a service changed (a new revision): copy it.
		Watches(knativeRoute, handler.EnqueueRequestsFromMapFunc(r.domainsFor), builder.WithPredicates(isKnativeLocal)).
		Named("domain").
		Complete(r)
}

// domainsFor is the Domains of the service a Knative route belongs to.
func (r *Domains) domainsFor(ctx context.Context, o client.Object) []reconcile.Request {
	var list v1.DomainList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace())); err != nil {
		return nil
	}
	service := o.GetLabels()[knativeRouteLabel]
	var out []reconcile.Request
	for _, d := range list.Items {
		if d.Spec.Service == service {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
		}
	}
	return out
}
