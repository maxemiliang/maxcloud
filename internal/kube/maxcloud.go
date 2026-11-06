package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// The maxcloud controller's kinds (controller/ in this repo).
var (
	gvrDatabase = schema.GroupVersionResource{Group: "maxcloud.gronblom.dev", Version: "v1alpha1", Resource: "databases"}
	gvrBranch   = schema.GroupVersionResource{Group: "maxcloud.gronblom.dev", Version: "v1alpha1", Resource: "databasebranches"}
)

// The annotations the controller acts on (api/v1alpha1 in controller/).
const (
	RestoreToAnnotation   = "maxcloud.gronblom.dev/restore-to"
	RecoverFromAnnotation = "maxcloud.gronblom.dev/recover-from"
)

// MaxcloudDB is what mcloud shows of an app's Database (the controller's kind).
type MaxcloudDB struct {
	Phase, Ready, Blocked, Restore string
	Generation                     int64
}

// GetDatabase reads the app's Database db; nil if it has none.
func (c *Client) GetDatabase(ctx context.Context, app string) (*MaxcloudDB, error) {
	o, err := c.Dyn.Resource(gvrDatabase).Namespace(app).Get(ctx, "db", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gen, _, _ := unstructured.NestedInt64(o.Object, "status", "generation")
	return &MaxcloudDB{
		Phase:      str(o, "status", "phase"),
		Generation: gen,
		Ready:      condition(o, "Ready").Message,
		Blocked:    blockedMessage(o),
		Restore:    condition(o, "Restore").Message,
	}, nil
}

func blockedMessage(o *unstructured.Unstructured) string {
	if c := condition(o, "Blocked"); c.Ready() {
		return c.Message
	}
	return ""
}

// AnnotateDatabase sets an annotation on the app's Database db.
func (c *Client) AnnotateDatabase(ctx context.Context, app, key, value string) error {
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{key: value}}})
	_, err := c.Dyn.Resource(gvrDatabase).Namespace(app).Patch(ctx, "db", types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("app %s has no database run by maxcloud (db: true in its maxcloud.yaml)", app)
	}
	return err
}

// Branch is what mcloud shows of a DatabaseBranch.
type Branch struct {
	Name, Phase, Cluster, Message string
	At                            string
	FromGeneration                int64
	Created                       time.Time
}

func (c *Client) CreateBranch(ctx context.Context, app, name string, at *time.Time) error {
	spec := map[string]any{"database": "db"}
	if at != nil {
		spec["at"] = at.UTC().Format(time.RFC3339)
	}
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "maxcloud.gronblom.dev/v1alpha1",
		"kind":       "DatabaseBranch",
		"metadata":   map[string]any{"name": name, "namespace": app},
		"spec":       spec,
	}}
	_, err := c.Dyn.Resource(gvrBranch).Namespace(app).Create(ctx, o, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("app %s already has a branch %s", app, name)
	}
	return err
}

func (c *Client) GetBranch(ctx context.Context, app, name string) (*Branch, error) {
	o, err := c.Dyn.Resource(gvrBranch).Namespace(app).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	b := branch(o)
	return &b, nil
}

func (c *Client) Branches(ctx context.Context, app string) ([]Branch, error) {
	items, err := c.list(ctx, gvrBranch, app, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Branch, 0, len(items))
	for i := range items {
		out = append(out, branch(&items[i]))
	}
	return out, nil
}

func (c *Client) DeleteBranch(ctx context.Context, app, name string) error {
	err := c.Dyn.Resource(gvrBranch).Namespace(app).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("app %s has no branch %s", app, name)
	}
	return err
}

func branch(o *unstructured.Unstructured) Branch {
	gen, _, _ := unstructured.NestedInt64(o.Object, "status", "fromGeneration")
	return Branch{
		Name:           o.GetName(),
		Phase:          str(o, "status", "phase"),
		Cluster:        str(o, "status", "cluster"),
		Message:        condition(o, "Ready").Message,
		At:             str(o, "spec", "at"),
		FromGeneration: gen,
		Created:        o.GetCreationTimestamp().Time,
	}
}
