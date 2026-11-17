package kube

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var gvrMaxcloudBucket = schema.GroupVersionResource{Group: "maxcloud.gronblom.dev", Version: "v1alpha1", Resource: "buckets"}

// AppBucket is one of an app's buckets, as the maxcloud controller last
// saw it.
type AppBucket struct {
	// Name is the bucket's name in maxcloud.yaml; FullName the one in
	// Object Storage (mc-<app>--<name>).
	Name, FullName string
	Public         bool
	// URL is where a public bucket's objects are served.
	URL string
	// Size and Objects are -1 until the controller has measured them
	// (every few minutes).
	Size, Objects int64
	Ready         Condition
}

// AppBuckets lists the app's buckets (its Bucket objects), by name. The
// database's backup bucket isn't one of them.
func (c *Client) AppBuckets(ctx context.Context, app string) ([]AppBucket, error) {
	items, err := c.list(ctx, gvrMaxcloudBucket, app, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]AppBucket, 0, len(items))
	for i := range items {
		o := &items[i]
		b := AppBucket{
			Name:     o.GetName(),
			FullName: str(o, "status", "bucketName"),
			URL:      str(o, "status", "url"),
			Size:     -1,
			Objects:  -1,
			Ready:    condition(o, "Ready"),
		}
		b.Public, _, _ = unstructured.NestedBool(o.Object, "spec", "public")
		if n, ok, _ := unstructured.NestedInt64(o.Object, "status", "sizeBytes"); ok {
			b.Size = n
		}
		if n, ok, _ := unstructured.NestedInt64(o.Object, "status", "objects"); ok {
			b.Objects = n
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
