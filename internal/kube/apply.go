package kube

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
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

// SecretData returns a Secret's values, empty if there's no such Secret.
func (c *Client) SecretData(ctx context.Context, ns, name string) (map[string]string, error) {
	s, err := c.Core.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out, nil
}

// Marks on the app's secrets Secret: mcloud owns it (the maxcloud
// controller backs up Secrets so marked), and Flux leaves it alone if it
// once applied it from a secrets.sops.yaml that's gone from git since.
var appSecretsLabels = map[string]string{"app.kubernetes.io/managed-by": FieldManager}
var appSecretsAnnotations = map[string]string{"kustomize.toolkit.fluxcd.io/prune": "disabled"}

// ApplySecret makes Secret name hold exactly data, marked as mcloud's.
// It's updated in place, never deleted: a Secret that's briefly missing
// would be put back from the controller's copy.
func (c *Client) ApplySecret(ctx context.Context, ns, name string, data map[string]string) error {
	bytes := make(map[string][]byte, len(data))
	for k, v := range data {
		bytes[k] = []byte(v)
	}
	secrets := c.Core.CoreV1().Secrets(ns)
	s, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		s = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: appSecretsLabels, Annotations: appSecretsAnnotations},
			Type:       corev1.SecretTypeOpaque,
			Data:       bytes,
		}
		_, err = secrets.Create(ctx, s, metav1.CreateOptions{FieldManager: FieldManager})
		return err
	}
	if err != nil {
		return err
	}
	markAppSecrets(s)
	s.Data, s.StringData = bytes, nil
	_, err = secrets.Update(ctx, s, metav1.UpdateOptions{FieldManager: FieldManager})
	return err
}

// AdoptSecret marks an existing Secret (one Flux applied from
// secrets.sops.yaml) as mcloud's. Returns false if there's none.
func (c *Client) AdoptSecret(ctx context.Context, ns, name string) (bool, error) {
	secrets := c.Core.CoreV1().Secrets(ns)
	s, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	markAppSecrets(s)
	_, err = secrets.Update(ctx, s, metav1.UpdateOptions{FieldManager: FieldManager})
	return err == nil, err
}

func markAppSecrets(s *corev1.Secret) {
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	for k, v := range appSecretsLabels {
		s.Labels[k] = v
	}
	for k, v := range appSecretsAnnotations {
		s.Annotations[k] = v
	}
}

// RestartedAtAnnotation on a function's pod template starts new pods (a
// new revision, for a Knative service), which read the secrets again.
const RestartedAtAnnotation = "maxcloud.gronblom.dev/restarted-at"

// RestartFunctions rolls the app's HTTP and queue functions, so they pick
// up changed secrets; scheduled ones read them on their next run anyway.
// Returns what it restarted. It applies as its own field manager, so
// neither Flux nor a direct deploy minds the annotation.
func (c *Client) RestartFunctions(ctx context.Context, ns string) ([]string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var done []string
	for _, k := range []struct {
		gvr        schema.GroupVersionResource
		apiVersion string
		kind       string
	}{
		{gvrKService, "serving.knative.dev/v1", "Service"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "apps/v1", "Deployment"},
	} {
		items, err := c.list(ctx, k.gvr, ns, metav1.ListOptions{LabelSelector: "app.kubernetes.io/part-of=maxcloud-functions"})
		if err != nil {
			return done, err
		}
		for _, it := range items {
			patch := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": k.apiVersion,
				"kind":       k.kind,
				"metadata":   map[string]any{"name": it.GetName()},
				"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
					"annotations": map[string]any{RestartedAtAnnotation: now},
				}}},
			}}
			if _, err := c.Dyn.Resource(k.gvr).Namespace(ns).Apply(ctx, it.GetName(), patch, metav1.ApplyOptions{FieldManager: FieldManager + "-restart", Force: true}); err != nil {
				return done, fmt.Errorf("restarting %s: %w", it.GetName(), err)
			}
			done = append(done, it.GetName())
		}
	}
	return done, nil
}
