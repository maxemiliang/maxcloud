package kube

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
)

// FieldManager is who direct deploys apply as.
const FieldManager = "mcloud"

// DirectLabel marks the objects a direct deploy applied, so the next one can
// prune what's gone.
const DirectLabel = "app.kubernetes.io/managed-by"

func (c *Client) mapper() meta.RESTMapper {
	if c.restMapper == nil {
		c.restMapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(c.Core.Discovery()))
	}
	return c.restMapper
}

func (c *Client) gvr(o *unstructured.Unstructured) (schema.GroupVersionResource, error) {
	gvk := o.GroupVersionKind()
	m, err := c.mapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("%s: %w", gvk.Kind, err)
	}
	return m.Resource, nil
}

// Apply server-side applies objects into ns as mcloud, taking over fields
// another manager set (it's the only one applying a direct app).
func (c *Client) Apply(ctx context.Context, ns string, objs []map[string]any) error {
	for _, m := range objs {
		o := &unstructured.Unstructured{Object: m}
		o.SetNamespace(ns)
		gvr, err := c.gvr(o)
		if err != nil {
			return err
		}
		opts := metav1.ApplyOptions{FieldManager: FieldManager, Force: true}
		if c.DryRun {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		if _, err := c.Dyn.Resource(gvr).Namespace(ns).Apply(ctx, o.GetName(), o, opts); err != nil {
			return fmt.Errorf("applying %s %s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	return nil
}

// The kinds a direct deploy generates, which it may prune. Databases and
// their TriggerAuthentication are never pruned.
var prunable = []schema.GroupVersionResource{
	gvrKService,
	{Group: "batch", Version: "v1", Resource: "cronjobs"},
	{Group: "apps", Version: "v1", Resource: "deployments"},
	gvrScaledObject,
}

// Prune deletes the functions a direct deploy applied earlier that aren't in
// keep (kind/name). Returns what it deleted.
func (c *Client) Prune(ctx context.Context, ns string, keep []map[string]any) ([]string, error) {
	want := map[string]bool{}
	for _, m := range keep {
		o := &unstructured.Unstructured{Object: m}
		want[o.GetKind()+"/"+o.GetName()] = true
	}
	var deleted []string
	selector := DirectLabel + "=" + FieldManager + ",app.kubernetes.io/part-of=maxcloud-functions"
	for _, gvr := range prunable {
		items, err := c.list(ctx, gvr, ns, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return deleted, err
		}
		for _, o := range items {
			key := o.GetKind() + "/" + o.GetName()
			if want[key] {
				continue
			}
			err := c.Dyn.Resource(gvr).Namespace(ns).Delete(ctx, o.GetName(), metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return deleted, err
			}
			deleted = append(deleted, key)
		}
	}
	return deleted, nil
}

// Exists reports whether the named object exists.
func (c *Client) Exists(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (bool, error) {
	o, err := c.get(ctx, gvr, ns, name)
	return o != nil, err
}

// GVRCNPGCluster and GVRKustomization for callers outside the package.
var (
	GVRCNPGCluster   = gvrCNPGCluster
	GVRKustomization = gvrKustomization
)

// NamespaceExists reports whether the namespace exists.
func (c *Client) NamespaceExists(ctx context.Context, name string) (bool, error) {
	_, err := c.Core.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
