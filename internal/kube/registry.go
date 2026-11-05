package kube

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

// RegistryEntry is an app's entry in the registry: a ResourceSetInputProvider
// in flux-system, which the ResourceSet turns into the app's namespace,
// guardrails, buckets and database.
type RegistryEntry struct {
	Entry *registry.Entry
	// InGit: Flux applied it from the homelab repo (the platform's own apps,
	// like run-demo). Those change in git, not with mcloud.
	InGit bool
}

// GetRegistryEntry returns the app's entry, or nil if it has none.
func (c *Client) GetRegistryEntry(ctx context.Context, name string) (*RegistryEntry, error) {
	o, err := c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if o.GetLabels()[registry.AppLabel] != "true" {
		return nil, fmt.Errorf("%s/%s isn't an app's registry entry", FluxNamespace, name)
	}
	e, err := registry.FromObject(o.Object)
	if err != nil {
		return nil, fmt.Errorf("registry entry %s: %w", name, err)
	}
	return &RegistryEntry{Entry: e, InGit: o.GetLabels()[registry.FluxOwnerLabel] != ""}, nil
}

// PutRegistryEntry creates the app's entry, or replaces its values. An entry
// that lives in git is refused: Flux would put it back.
func (c *Client) PutRegistryEntry(ctx context.Context, e *registry.Entry) error {
	want := &unstructured.Unstructured{Object: e.Object()}
	res := c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace)
	cur, err := res.Get(ctx, e.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = res.Create(ctx, want, metav1.CreateOptions{FieldManager: FieldManager, DryRun: c.dryRun()})
		return err
	}
	if err != nil {
		return err
	}
	if cur.GetLabels()[registry.FluxOwnerLabel] != "" {
		return fmt.Errorf("app %s is registered in the homelab repo (apps/maxcloud/apps/%s.yaml): change it there", e.Name, e.Name)
	}
	cur.Object["spec"] = want.Object["spec"]
	labels := cur.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range want.GetLabels() {
		labels[k] = v
	}
	cur.SetLabels(labels)
	_, err = res.Update(ctx, cur, metav1.UpdateOptions{FieldManager: FieldManager, DryRun: c.dryRun()})
	return err
}

// DeleteRegistryEntry removes the app's entry: the registry then deletes its
// namespace and everything in it.
func (c *Client) DeleteRegistryEntry(ctx context.Context, name string) error {
	err := c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Delete(ctx, name, metav1.DeleteOptions{DryRun: c.dryRun()})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s isn't registered", name)
	}
	return err
}

func (c *Client) dryRun() []string {
	if c.DryRun {
		return []string{metav1.DryRunAll}
	}
	return nil
}
