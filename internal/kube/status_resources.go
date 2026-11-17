package kube

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The registry entry's annotations the controller writes when it brings
// the entry in line with a pushed maxcloud.yaml (controller/internal/
// controller/appsync.go).
const (
	SyncErrorAnnotation      = "maxcloud.gronblom.dev/sync-error"
	SyncedRevisionAnnotation = "maxcloud.gronblom.dev/synced-revision"
)

var (
	gvrBucket = schema.GroupVersionResource{Group: "maxcloud.gronblom.dev", Version: "v1alpha1", Resource: "buckets"}
	gvrDomain = schema.GroupVersionResource{Group: "maxcloud.gronblom.dev", Version: "v1alpha1", Resource: "domains"}
)

// Bucket is one of the app's buckets (the controller's kind).
type Bucket struct {
	Name     string // as in maxcloud.yaml
	Bucket   string // in Object Storage: mc-<app>--<name>
	Public   bool
	URL      string // a public bucket's
	Size     *int64 // bytes, refreshed every few minutes
	Objects  *int64
	Ready    Condition
	Observed bool // the controller has seen its current spec
}

// Domain is one of the app's custom hostnames.
type Domain struct {
	Host     string
	Service  string
	Proxied  bool
	URL      string
	Ready    Condition
	Observed bool
}

// storageAndDomains lists the app's Buckets and Domains. Either is nil when
// it can't be read (the kind isn't installed, or the caller may not list
// it), which leaves it out of the status.
func (c *Client) storageAndDomains(ctx context.Context, ns string) ([]Bucket, []Domain) {
	var buckets []Bucket
	bl, _ := c.list(ctx, gvrBucket, ns, metav1.ListOptions{})
	for i := range bl {
		o := &bl[i]
		public, _, _ := unstructured.NestedBool(o.Object, "spec", "public")
		b := Bucket{
			Name: o.GetName(), Bucket: str(o, "status", "bucketName"), Public: public, URL: str(o, "status", "url"),
			Ready: condition(o, "Ready"), Observed: observed(o),
		}
		if v, ok, _ := unstructured.NestedInt64(o.Object, "status", "sizeBytes"); ok {
			b.Size = &v
		}
		if v, ok, _ := unstructured.NestedInt64(o.Object, "status", "objects"); ok {
			b.Objects = &v
		}
		buckets = append(buckets, b)
	}
	var domains []Domain
	dl, _ := c.list(ctx, gvrDomain, ns, metav1.ListOptions{})
	for i := range dl {
		o := &dl[i]
		proxied, _, _ := unstructured.NestedBool(o.Object, "spec", "proxied")
		domains = append(domains, Domain{
			Host: o.GetName(), Service: str(o, "spec", "service"), Proxied: proxied, URL: str(o, "status", "url"),
			Ready: condition(o, "Ready"), Observed: observed(o),
		})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	sort.Slice(domains, func(i, j int) bool { return domains[i].Host < domains[j].Host })
	return buckets, domains
}

// observed: status.observedGeneration has caught up with the spec.
func observed(o *unstructured.Unstructured) bool {
	g, ok, _ := unstructured.NestedInt64(o.Object, "status", "observedGeneration")
	return ok && g >= o.GetGeneration()
}
