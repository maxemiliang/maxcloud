package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
)

func TestHostAllowed(t *testing.T) {
	list := []string{"paja.lankkupelit.fi", "*.shop.example.com"}
	for host, want := range map[string]bool{
		"paja.lankkupelit.fi":        true,
		"play.lankkupelit.fi":        false,
		"a.shop.example.com":         true,
		"a.b.shop.example.com":       false, // one level, like the certificate
		"shop.example.com":           false,
		"evilshop.example.com":       false,
		"x.paja.lankkupelit.fi":      false,
		"paja.lankkupelit.fi.evil.x": false,
	} {
		if got := HostAllowed(host, list); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
	if HostAllowed("paja.lankkupelit.fi", nil) {
		t.Error("an empty list allows nothing")
	}
	if got := splitList(" paja.lankkupelit.fi,\n *.Shop.example.com "); len(got) != 2 || got[1] != "*.shop.example.com" {
		t.Errorf("splitList = %q", got)
	}
}

type domainEnv struct {
	t      *testing.T
	client client.Client
	r      *Domains
}

func gatewayObj() *unstructured.Unstructured {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetNamespace("traefik")
	gw.SetName("public")
	listener := func(name, proto, host string) map[string]any {
		l := map[string]any{"name": name, "protocol": proto, "port": int64(8443)}
		if host != "" {
			l["hostname"] = host
		}
		return l
	}
	gw.Object["spec"] = map[string]any{"listeners": []any{
		listener("web", "HTTP", ""),
		listener("https-lab", "HTTPS", "*.lab.gronblom.dev"),
		listener("https-public", "HTTPS", "*.gronblom.dev"),
		listener("https-lankkupelit", "HTTPS", "*.lankkupelit.fi"),
		listener("in-cluster", "HTTP", ""),
	}}
	return gw
}

func allowlist(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "maxcloud-system", Name: "maxcloud-domains"}, Data: data}
}

// knativeLocalRoute is what net-gateway-api makes for a service's
// cluster-local name (as seen on the cluster): a rule for its prober, and
// the one that carries traffic.
func knativeLocalRoute(ns, svc string, revisions ...string) *unstructured.Unstructured {
	rt := &unstructured.Unstructured{}
	rt.SetGroupVersionKind(httpRouteGVK)
	rt.SetNamespace(ns)
	rt.SetName(svc + "." + ns + ".svc.cluster.local")
	rt.SetLabels(map[string]string{
		knativeRouteLabel: svc, knativeVisibilityLabel: "cluster-local", "serving.knative.dev/service": svc,
	})
	var refs []any
	for _, rev := range revisions {
		refs = append(refs, map[string]any{
			"group": "", "kind": "Service", "name": rev, "port": int64(80), "weight": int64(100 / len(revisions)),
			"filters": []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{"set": []any{
				map[string]any{"name": "Knative-Serving-Namespace", "value": ns},
				map[string]any{"name": "Knative-Serving-Revision", "value": rev},
			}}}},
		})
	}
	prefix := map[string]any{"type": "PathPrefix", "value": "/"}
	rt.Object["spec"] = map[string]any{
		"hostnames":  []any{svc + "." + ns, svc + "." + ns + ".svc", svc + "." + ns + ".svc.cluster.local"},
		"parentRefs": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "knative-local", "namespace": "traefik"}},
		"rules": []any{
			map[string]any{
				"matches":     []any{map[string]any{"headers": []any{map[string]any{"name": "K-Network-Hash", "type": "Exact", "value": "override"}}, "path": prefix}},
				"filters":     []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{"set": []any{map[string]any{"name": "K-Network-Hash", "value": "abc"}}}}},
				"backendRefs": refs,
			},
			map[string]any{"matches": []any{map[string]any{"path": prefix}}, "backendRefs": refs},
		},
	}
	return rt
}

func domain(ns, host, svc string, proxied bool) *v1.Domain {
	return &v1.Domain{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: host, Generation: 1, UID: types.UID("uid-" + host)},
		Spec: v1.DomainSpec{Service: svc, Proxied: proxied}}
}

func newDomainEnv(t *testing.T, objs ...client.Object) *domainEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1.Domain{}).Build()
	return &domainEnv{t: t, client: c, r: &Domains{
		Client: c, Reader: c, AllowlistNamespace: "maxcloud-system", Allowlist: "maxcloud-domains",
		GatewayNamespace: "traefik", Gateway: "public",
	}}
}

func (e *domainEnv) reconcile(ns, host string) ctrl.Result {
	e.t.Helper()
	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: host}})
	if err != nil {
		e.t.Fatalf("reconcile %s/%s: %v", ns, host, err)
	}
	return res
}

func (e *domainEnv) domain(ns, host string) *v1.Domain {
	e.t.Helper()
	var d v1.Domain
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: host}, &d); err != nil {
		e.t.Fatal(err)
	}
	return &d
}

func (e *domainEnv) route(ns, host string) *unstructured.Unstructured {
	e.t.Helper()
	rt := &unstructured.Unstructured{}
	rt.SetGroupVersionKind(httpRouteGVK)
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: DomainRoutePrefix + host}, rt)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return rt
}

func domainReady(d *v1.Domain) *metav1.Condition {
	return meta.FindStatusCondition(d.Status.Conditions, "Ready")
}

func TestDomainCopiesKnativeRoute(t *testing.T) {
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), allowlist(map[string]string{"paja": "paja.lankkupelit.fi"}),
		knativeLocalRoute("paja", "web", "web-00003"), domain("paja", "paja.lankkupelit.fi", "web", true))
	e.reconcile("paja", "paja.lankkupelit.fi")

	d := e.domain("paja", "paja.lankkupelit.fi")
	if c := domainReady(d); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %+v", c)
	}
	if d.Status.URL != "https://paja.lankkupelit.fi/" || d.Status.Listener != "https-lankkupelit" || strings.Join(d.Status.Revisions, ",") != "web-00003" {
		t.Errorf("status = %+v", d.Status)
	}

	rt := e.route("paja", "paja.lankkupelit.fi")
	if rt == nil {
		t.Fatal("no HTTPRoute")
	}
	if rt.GetAnnotations()[proxiedAnnotation] != "true" {
		t.Errorf("annotations = %v", rt.GetAnnotations())
	}
	if !metav1.IsControlledBy(rt, d) {
		t.Error("the route isn't owned by the Domain (garbage collection wouldn't remove it)")
	}
	hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
	if strings.Join(hosts, ",") != "paja.lankkupelit.fi" {
		t.Errorf("hostnames = %v", hosts)
	}
	parents, _, _ := unstructured.NestedSlice(rt.Object, "spec", "parentRefs")
	p := parents[0].(map[string]any)
	if p["name"] != "public" || p["namespace"] != "traefik" || p["sectionName"] != "https-lankkupelit" {
		t.Errorf("parentRef = %v", p)
	}
	rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("rules = %v (the prober's rule should be left out)", rules)
	}
	rule := rules[0].(map[string]any)
	if _, ok := rule["filters"]; ok {
		t.Errorf("rule filters = %v: no host rewrite, the app sees the original Host", rule["filters"])
	}
	ref := rule["backendRefs"].([]any)[0].(map[string]any)
	if ref["name"] != "web-00003" {
		t.Errorf("backendRef = %v", ref)
	}
	set := ref["filters"].([]any)[0].(map[string]any)["requestHeaderModifier"].(map[string]any)["set"].([]any)
	if set[1].(map[string]any)["value"] != "web-00003" {
		t.Errorf("revision header = %v (the activator needs it)", set)
	}
}

func TestDomainFollowsNewRevision(t *testing.T) {
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), allowlist(map[string]string{"paja": "*.lankkupelit.fi"}),
		knativeLocalRoute("paja", "web", "web-00003"), domain("paja", "paja.lankkupelit.fi", "web", false))
	e.reconcile("paja", "paja.lankkupelit.fi")

	// Knative rolls out web-00004 and updates its route.
	ctx := context.Background()
	src := knativeLocalRoute("paja", "web", "web-00003", "web-00004")
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(httpRouteGVK)
	if err := e.client.Get(ctx, client.ObjectKeyFromObject(src), cur); err != nil {
		t.Fatal(err)
	}
	cur.Object["spec"] = src.Object["spec"]
	if err := e.client.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	reqs := e.r.domainsFor(ctx, cur)
	if len(reqs) != 1 || reqs[0].Name != "paja.lankkupelit.fi" {
		t.Fatalf("domainsFor = %v", reqs)
	}
	e.reconcile("paja", "paja.lankkupelit.fi")

	if d := e.domain("paja", "paja.lankkupelit.fi"); strings.Join(d.Status.Revisions, ",") != "web-00003,web-00004" {
		t.Errorf("revisions = %v", d.Status.Revisions)
	}
	rt := e.route("paja", "paja.lankkupelit.fi")
	refs, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
	if n := len(refs[0].(map[string]any)["backendRefs"].([]any)); n != 2 {
		t.Errorf("%d backendRefs, want the split over both revisions", n)
	}
	if _, ok := rt.GetAnnotations()[proxiedAnnotation]; ok {
		t.Error("not proxied, but the route has the annotation")
	}
}

func TestDomainNotAllowed(t *testing.T) {
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), allowlist(map[string]string{"other": "paja.lankkupelit.fi"}),
		knativeLocalRoute("paja", "web", "web-00001"), domain("paja", "paja.lankkupelit.fi", "web", false))
	e.reconcile("paja", "paja.lankkupelit.fi")
	if c := domainReady(e.domain("paja", "paja.lankkupelit.fi")); c == nil || c.Reason != ReasonNotAllowed {
		t.Fatalf("Ready = %+v", c)
	}
	if e.route("paja", "paja.lankkupelit.fi") != nil {
		t.Error("a route for a hostname the app isn't allowed")
	}
}

func TestDomainRemovedFromAllowlistTakesTheRouteDown(t *testing.T) {
	cm := allowlist(map[string]string{"paja": "paja.lankkupelit.fi"})
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), cm,
		knativeLocalRoute("paja", "web", "web-00001"), domain("paja", "paja.lankkupelit.fi", "web", false))
	e.reconcile("paja", "paja.lankkupelit.fi")
	if e.route("paja", "paja.lankkupelit.fi") == nil {
		t.Fatal("no route")
	}
	cm.Data = map[string]string{}
	if err := e.client.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	e.reconcile("paja", "paja.lankkupelit.fi")
	if e.route("paja", "paja.lankkupelit.fi") != nil {
		t.Error("the route stayed after the hostname left the allowlist")
	}
}

func TestDomainNeedsAListener(t *testing.T) {
	// Two levels below *.lankkupelit.fi: the Gateway's wildcard certificate
	// doesn't cover it.
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), allowlist(map[string]string{"paja": "x.paja.lankkupelit.fi, paja.example.com"}),
		knativeLocalRoute("paja", "web", "web-00001"),
		domain("paja", "x.paja.lankkupelit.fi", "web", false), domain("paja", "paja.example.com", "web", false))
	for _, host := range []string{"x.paja.lankkupelit.fi", "paja.example.com"} {
		e.reconcile("paja", host)
		if c := domainReady(e.domain("paja", host)); c == nil || c.Reason != ReasonNoListener {
			t.Errorf("%s: Ready = %+v", host, c)
		}
	}
}

func TestDomainMostSpecificListener(t *testing.T) {
	e := newDomainEnv(t, tenantNS("shop"), gatewayObj(), allowlist(map[string]string{"shop": "shop.lab.gronblom.dev,shop.gronblom.dev"}),
		knativeLocalRoute("shop", "web", "web-00001"),
		domain("shop", "shop.lab.gronblom.dev", "web", false), domain("shop", "shop.gronblom.dev", "web", false))
	e.reconcile("shop", "shop.lab.gronblom.dev")
	e.reconcile("shop", "shop.gronblom.dev")
	if l := e.domain("shop", "shop.lab.gronblom.dev").Status.Listener; l != "https-lab" {
		t.Errorf("shop.lab.gronblom.dev on %q", l)
	}
	if l := e.domain("shop", "shop.gronblom.dev").Status.Listener; l != "https-public" {
		t.Errorf("shop.gronblom.dev on %q", l)
	}
}

func TestDomainWaitsForTheService(t *testing.T) {
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), allowlist(map[string]string{"paja": "paja.lankkupelit.fi"}),
		knativeLocalRoute("paja", "other", "other-00001"), domain("paja", "paja.lankkupelit.fi", "web", false))
	e.reconcile("paja", "paja.lankkupelit.fi")
	if c := domainReady(e.domain("paja", "paja.lankkupelit.fi")); c == nil || c.Reason != ReasonNoService {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestDomainHostAlreadyRouted(t *testing.T) {
	// The cutover case: the old deployment's route still has the hostname.
	old := &unstructured.Unstructured{}
	old.SetGroupVersionKind(httpRouteGVK)
	old.SetNamespace("lankkupaja")
	old.SetName("lankkupaja")
	old.Object["spec"] = map[string]any{
		"parentRefs": []any{map[string]any{"name": "public", "namespace": "traefik", "sectionName": "https-lankkupelit"}},
		"hostnames":  []any{"paja.lankkupelit.fi"},
	}
	e := newDomainEnv(t, tenantNS("paja"), gatewayObj(), allowlist(map[string]string{"paja": "paja.lankkupelit.fi"}), old,
		knativeLocalRoute("paja", "web", "web-00001"), domain("paja", "paja.lankkupelit.fi", "web", false))
	res := e.reconcile("paja", "paja.lankkupelit.fi")
	c := domainReady(e.domain("paja", "paja.lankkupelit.fi"))
	if c == nil || c.Reason != ReasonHostInUse || !strings.Contains(c.Message, "lankkupaja/lankkupaja") {
		t.Fatalf("Ready = %+v", c)
	}
	if res.RequeueAfter != domainRetryHostInUse {
		t.Errorf("RequeueAfter = %v: should look again soon", res.RequeueAfter)
	}

	// Once it's gone, the Domain takes over.
	if err := e.client.Delete(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	e.reconcile("paja", "paja.lankkupelit.fi")
	if c := domainReady(e.domain("paja", "paja.lankkupelit.fi")); c.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v", c)
	}
}

func TestDomainOutsideTenant(t *testing.T) {
	e := newDomainEnv(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}, gatewayObj(),
		allowlist(map[string]string{"kube-system": "x.gronblom.dev"}), domain("kube-system", "x.gronblom.dev", "web", false))
	e.reconcile("kube-system", "x.gronblom.dev")
	if c := domainReady(e.domain("kube-system", "x.gronblom.dev")); c == nil || c.Reason != ReasonNotTenant {
		t.Fatalf("Ready = %+v", c)
	}
}
