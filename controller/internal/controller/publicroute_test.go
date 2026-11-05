package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
)

func withRoutes(e *env) {
	e.r.Routes = &PublicRoutes{
		Client: e.client, Reader: e.client, Namespace: "maxcloud-system", Domain: "example.dev",
		GatewayNamespace: "traefik", Gateway: "public", Listener: "https-public", S3Host: "s3.example.com",
	}
}

func (e *env) routeObjects(name string) (svc *corev1.Service, ts, route *unstructured.Unstructured) {
	e.t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Namespace: "maxcloud-system", Name: name}
	var s corev1.Service
	if err := e.client.Get(ctx, key, &s); err == nil {
		svc = &s
	} else if !apierrors.IsNotFound(err) {
		e.t.Fatal(err)
	}
	get := func(u *unstructured.Unstructured) *unstructured.Unstructured {
		if err := e.client.Get(ctx, key, u); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			e.t.Fatal(err)
		}
		return u
	}
	t := &unstructured.Unstructured{}
	t.SetGroupVersionKind(traefikServiceGVK)
	r := &unstructured.Unstructured{}
	r.SetGroupVersionKind(httpRouteGVK)
	return svc, get(t), get(r)
}

func TestPublicBucketGetsAHostname(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "assets", func(b *v1.Bucket) { b.Spec.Public = true }))
	withRoutes(e)
	e.mustReconcile("shop", "assets")

	svc, ts, route := e.routeObjects("mc-shop--assets")
	if svc == nil || svc.Spec.Type != corev1.ServiceTypeExternalName || svc.Spec.ExternalName != "mc-shop--assets.s3.example.com" {
		t.Fatalf("Service = %+v", svc)
	}
	if ts == nil {
		t.Fatal("no TraefikService")
	}
	services, _, _ := unstructured.NestedSlice(ts.Object, "spec", "weighted", "services")
	if len(services) != 1 || services[0].(map[string]any)["passHostHeader"] != false || services[0].(map[string]any)["scheme"] != "https" {
		t.Errorf("TraefikService services = %v", services)
	}
	if route == nil {
		t.Fatal("no HTTPRoute")
	}
	hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if len(hosts) != 1 || hosts[0] != "assets--shop.example.dev" {
		t.Errorf("hostnames = %v", hosts)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	ref := rules[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)
	if ref["kind"] != "TraefikService" || ref["name"] != "maxcloud-system-mc-shop--assets@kubernetescrd" {
		t.Errorf("backendRef = %v", ref)
	}
	matches := rules[0].(map[string]any)["matches"].([]any)
	if len(matches) != 2 || matches[0].(map[string]any)["method"] != "GET" || matches[1].(map[string]any)["method"] != "HEAD" {
		t.Errorf("matches = %v (reading only)", matches)
	}
	if route.GetAnnotations()["external-dns.kubernetes.io/cloudflare-proxied"] != "true" {
		t.Error("not proxied")
	}
	if got := e.bucket("shop", "assets").Status.URL; got != "https://assets--shop.example.dev/" {
		t.Errorf("status.url = %q", got)
	}

	// Private again: the route goes.
	b := e.bucket("shop", "assets")
	b.Spec.Public = false
	b.Generation = 2
	if err := e.client.Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	e.mustReconcile("shop", "assets")
	if svc, ts, route := e.routeObjects("mc-shop--assets"); svc != nil || ts != nil || route != nil {
		t.Error("route objects left after making the bucket private")
	}
	if got := e.bucket("shop", "assets").Status.URL; got != "" {
		t.Errorf("status.url = %q", got)
	}
}

func TestPublicRouteGoesWithTheBucket(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "assets", func(b *v1.Bucket) { b.Spec.Public = true }))
	withRoutes(e)
	e.mustReconcile("shop", "assets")
	e.delete("shop", "assets")
	e.mustReconcile("shop", "assets")
	if svc, ts, route := e.routeObjects("mc-shop--assets"); svc != nil || ts != nil || route != nil {
		t.Error("route objects left after deleting the bucket")
	}
}

func TestNoHostnamesWhenOff(t *testing.T) {
	e := newEnv(t, tenantNS("shop"), bucket("shop", "assets", func(b *v1.Bucket) { b.Spec.Public = true }))
	e.mustReconcile("shop", "assets") // Routes nil
	if svc, _, _ := e.routeObjects("mc-shop--assets"); svc != nil {
		t.Error("made a route with hostnames off")
	}
	if _, ok := e.policies.policies["mc-shop--assets"]; !ok {
		t.Error("still public through the S3 endpoint")
	}
}
