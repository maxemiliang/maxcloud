package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PublicRoutes gives a public bucket a hostname of its own on the cluster's
// Gateway, <bucket>--<app>.<domain>, so it can sit behind the CDN in front
// of the cluster (Cloudflare) with the cluster's wildcard certificate.
//
// The Gateway API can't send traffic to a host outside the cluster
// (Traefik refuses ExternalName Services as HTTPRoute backends), so it
// goes through Traefik's own objects, all in the controller's namespace:
//
//	HTTPRoute (GET and HEAD for the hostname)
//	  -> TraefikService (passHostHeader: false, https)
//	    -> Service of type ExternalName: <bucket>.<S3 host>:443
//
// Traefik needs kubernetesCRD.allowExternalNameServices, and this namespace
// in kubernetesGateway.crossProviderNamespaces (so its HTTPRoutes may name
// a TraefikService). Apps can make neither object, and can't put routes in
// this namespace.
type PublicRoutes struct {
	Client client.Client
	// Reader reads straight from the API server: no cache of every
	// Service and HTTPRoute in the cluster.
	Reader client.Reader

	// Namespace for the objects: the controller's own.
	Namespace string
	// Domain of the hostnames ("" turns public hostnames off), and the
	// Gateway listener that serves it.
	Domain                              string
	GatewayNamespace, Gateway, Listener string
	// S3Host is the Object Storage endpoint's host (tl8dc.upcloudobjects.com).
	S3Host string
}

var (
	httpRouteGVK      = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
	traefikServiceGVK = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "TraefikService"}
)

// BucketLabel marks the route objects of a bucket, with its full name.
const BucketLabel = "maxcloud.gronblom.dev/bucket"

// +kubebuilder:rbac:groups="",resources=services,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=traefik.io,resources=traefikservices,verbs=get;create;update;delete

func (p *PublicRoutes) Enabled() bool { return p != nil && p.Domain != "" }

// Hostname of an app's public bucket. "--" can't occur in app or bucket
// names, so this can't collide with a Knative service's <svc>-<app>.
func (p *PublicRoutes) Hostname(app, bucket string) string {
	return bucket + "--" + app + "." + p.Domain
}

// Ensure creates or updates the route to bucket (its full Object Storage
// name) at hostname.
func (p *PublicRoutes) Ensure(ctx context.Context, bucket, hostname string) error {
	labels := map[string]string{ManagedByLabel: managedBy, BucketLabel: bucket}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: bucket, Labels: labels},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: bucket + "." + p.S3Host,
			Ports:        []corev1.ServicePort{{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := p.apply(ctx, svc, func(cur client.Object) {
		c := cur.(*corev1.Service)
		c.Labels, c.Spec.Type, c.Spec.ExternalName, c.Spec.Ports = svc.Labels, svc.Spec.Type, svc.Spec.ExternalName, svc.Spec.Ports
	}); err != nil {
		return err
	}

	ts := p.unstructured(traefikServiceGVK, bucket, labels)
	ts.Object["spec"] = map[string]any{
		"weighted": map[string]any{
			"services": []any{map[string]any{
				"name":   bucket,
				"port":   int64(443),
				"scheme": "https",
				// The S3 endpoint finds the bucket by Host; it must not
				// see the public hostname.
				"passHostHeader": false,
			}},
		},
	}
	if err := p.applyUnstructured(ctx, ts); err != nil {
		return err
	}

	route := p.unstructured(httpRouteGVK, bucket, labels)
	// Proxied by Cloudflare: its edge certificate covers one-level names,
	// and it caches the objects.
	route.SetAnnotations(map[string]string{"external-dns.kubernetes.io/cloudflare-proxied": "true"})
	match := func(method string) map[string]any {
		return map[string]any{"method": method, "path": map[string]any{"type": "PathPrefix", "value": "/"}}
	}
	route.Object["spec"] = map[string]any{
		"parentRefs": []any{map[string]any{
			"name": p.Gateway, "namespace": p.GatewayNamespace, "sectionName": p.Listener,
		}},
		"hostnames": []any{hostname},
		"rules": []any{map[string]any{
			// Reading only: anything else gets a 404 from the Gateway.
			"matches": []any{match("GET"), match("HEAD")},
			"backendRefs": []any{map[string]any{
				"group": "traefik.io",
				"kind":  "TraefikService",
				// Traefik's own name for it (namespace-name@provider).
				"name": p.Namespace + "-" + bucket + "@kubernetescrd",
			}},
		}},
	}
	return p.applyUnstructured(ctx, route)
}

// Remove deletes a bucket's route objects, if it has any.
func (p *PublicRoutes) Remove(ctx context.Context, bucket string) error {
	objs := []client.Object{
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: bucket}},
		p.unstructured(traefikServiceGVK, bucket, nil),
		p.unstructured(httpRouteGVK, bucket, nil),
	}
	for _, o := range objs {
		if err := p.Client.Delete(ctx, o); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete %T %s: %w", o, bucket, err)
		}
	}
	return nil
}

func (p *PublicRoutes) unstructured(gvk schema.GroupVersionKind, name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(p.Namespace)
	u.SetName(name)
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

// apply creates want, or updates the existing object with set.
func (p *PublicRoutes) apply(ctx context.Context, want client.Object, set func(cur client.Object)) error {
	cur := want.DeepCopyObject().(client.Object)
	err := p.Reader.Get(ctx, types.NamespacedName{Namespace: want.GetNamespace(), Name: want.GetName()}, cur)
	if apierrors.IsNotFound(err) {
		return p.Client.Create(ctx, want)
	}
	if err != nil {
		return err
	}
	set(cur)
	return p.Client.Update(ctx, cur)
}

func (p *PublicRoutes) applyUnstructured(ctx context.Context, want *unstructured.Unstructured) error {
	return p.apply(ctx, want, func(cur client.Object) {
		u := cur.(*unstructured.Unstructured)
		u.Object["spec"] = want.Object["spec"]
		u.SetLabels(want.GetLabels())
		if a := want.GetAnnotations(); a != nil {
			u.SetAnnotations(a)
		}
	})
}
