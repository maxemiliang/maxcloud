package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

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
	// SyncedFields are the registry keys (cpu, memory, pods, buckets, db,
	// dbSize, dbInstances) the app's maxcloud.yaml declares, which it owns.
	SyncedFields []string
	// Registration is the entry's registration ID, which the maxcloud
	// controller gives it: the app's buckets and database backups belong
	// to one registration, and a new entry under an old name doesn't get
	// them without an admin.
	Registration string
}

// RegistrationAnnotation holds an entry's registration ID (the maxcloud
// controller's; only it and Flux may set it).
const RegistrationAnnotation = "maxcloud.gronblom.dev/registration"

// AutheliaPrefix is how the API server names Authelia's users and groups
// (homelab infra/cluster-auth).
const AutheliaPrefix = "authelia:"

// Whoami returns the user the cluster sees this client as (what kubectl
// auth whoami shows), such as authelia:alice.
func (c *Client) Whoami(ctx context.Context) (string, error) {
	r, err := c.Core.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return r.Status.UserInfo.Username, nil
}

// SyncedFieldsAnnotation on a registry entry lists the keys the app's
// maxcloud.yaml declares (the maxcloud controller's AppSync and mcloud
// deploy write it).
const SyncedFieldsAnnotation = "maxcloud.gronblom.dev/synced-fields"

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
	r := &RegistryEntry{Entry: e, InGit: o.GetLabels()[registry.FluxOwnerLabel] != "", Registration: o.GetAnnotations()[RegistrationAnnotation]}
	if v := o.GetAnnotations()[SyncedFieldsAnnotation]; v != "" {
		r.SyncedFields = strings.Split(v, ",")
	}
	return r, nil
}

// PutRegistryEntry creates the app's entry, or replaces its values (owners
// included: e must carry them). An entry that lives in git is refused: Flux
// would put it back.
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

// SetSyncedFields records which keys the app's maxcloud.yaml owns.
func (c *Client) SetSyncedFields(ctx context.Context, name string, fields []string) error {
	var v any
	if len(fields) > 0 {
		v = strings.Join(fields, ",")
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{SyncedFieldsAnnotation: v}}})
	if err != nil {
		return err
	}
	_, err = c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Patch(ctx, name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: FieldManager, DryRun: c.dryRun()})
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
