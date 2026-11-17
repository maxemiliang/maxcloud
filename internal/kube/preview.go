package kube

import (
	"context"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GVRPreview is the maxcloud controller's Preview: a short-lived deploy of an
// app in an environment of its own (<app>--<name>), made in the app's
// namespace.
var GVRPreview = schema.GroupVersionResource{Group: "maxcloud.gronblom.dev", Version: "v1alpha1", Resource: "previews"}

// Preview is what mcloud shows of one.
type Preview struct {
	Name        string
	PullRequest int64
	ExpiresAt   time.Time
	// Phase: Active, Waiting or Expired ("" before the controller looked).
	Phase       string
	Environment string
	// Reason and Message: its Ready condition.
	Reason, Message string
}

func previewFrom(o *unstructured.Unstructured) Preview {
	p := Preview{Name: o.GetName(), Phase: str(o, "status", "phase"), Environment: str(o, "status", "environment")}
	p.PullRequest, _, _ = unstructured.NestedInt64(o.Object, "spec", "pullRequest")
	if s := str(o, "spec", "expiresAt"); s != "" {
		p.ExpiresAt, _ = time.Parse(time.RFC3339, s)
	}
	c := condition(o, "Ready")
	p.Reason, p.Message = c.Reason, c.Message
	return p
}

// Previews lists app's previews, by name.
func (c *Client) Previews(ctx context.Context, app string) ([]Preview, error) {
	items, err := c.list(ctx, GVRPreview, app, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Preview, 0, len(items))
	for i := range items {
		out = append(out, previewFrom(&items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetPreview returns app's preview name, or nil.
func (c *Client) GetPreview(ctx context.Context, app, name string) (*Preview, error) {
	o, err := c.get(ctx, GVRPreview, app, name)
	if err != nil || o == nil {
		return nil, err
	}
	p := previewFrom(o)
	return &p, nil
}

// PutPreview creates app's working-tree preview name, or moves its expiry
// to expires.
func (c *Client) PutPreview(ctx context.Context, app, name string, expires time.Time) error {
	res := c.Dyn.Resource(GVRPreview).Namespace(app)
	at := expires.UTC().Format(time.RFC3339)
	cur, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		o := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": GVRPreview.GroupVersion().String(),
			"kind":       "Preview",
			"metadata":   map[string]any{"name": name, "namespace": app},
			"spec":       map[string]any{"expiresAt": at},
		}}
		_, err = res.Create(ctx, o, metav1.CreateOptions{FieldManager: FieldManager})
		return err
	}
	if err != nil {
		return err
	}
	if err := unstructured.SetNestedField(cur.Object, at, "spec", "expiresAt"); err != nil {
		return err
	}
	_, err = res.Update(ctx, cur, metav1.UpdateOptions{FieldManager: FieldManager})
	return err
}

// DeletePreview removes app's preview name: the controller then removes
// its environment, data included.
func (c *Client) DeletePreview(ctx context.Context, app, name string) error {
	return c.Dyn.Resource(GVRPreview).Namespace(app).Delete(ctx, name, metav1.DeleteOptions{})
}

// Labels and annotations the maxcloud controller puts on an app's
// environments' registry entries and on the app's own.
const (
	EnvironmentOfLabel          = "run.gronblom.dev/environment-of"
	EnvironmentLabel            = "run.gronblom.dev/environment"
	EnvironmentKindLabel        = "run.gronblom.dev/environment-kind"
	EnvironmentsErrorAnnotation = "maxcloud.gronblom.dev/environments-error"
)

// Environment is one of an app's environments or previews, as its registry
// entry and its deploy show it.
type Environment struct {
	Name, Env, Kind string // shop--staging, staging, environment or preview
	Branch          string
	// Applied: Flux has its Kustomization (GitOps); Deploy its Ready.
	Applied bool
	Deploy  Condition
}

// Environments lists app's environments and previews, and why the
// controller couldn't make ones the app declares ("" if it could).
func (c *Client) Environments(ctx context.Context, app string) ([]Environment, string, error) {
	items, err := c.list(ctx, gvrProvider, FluxNamespace, metav1.ListOptions{LabelSelector: EnvironmentOfLabel + "=" + app})
	if err != nil {
		return nil, "", err
	}
	var out []Environment
	for i := range items {
		o := &items[i]
		e := Environment{
			Name: o.GetName(), Env: o.GetLabels()[EnvironmentLabel], Kind: o.GetLabels()[EnvironmentKindLabel],
			Branch: str(o, "spec", "defaultValues", "branch"),
		}
		k, err := c.get(ctx, gvrKustomization, FluxNamespace, e.Name)
		if err != nil {
			return nil, "", err
		}
		if k != nil {
			e.Applied, e.Deploy = true, condition(k, "Ready")
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	parent, err := c.get(ctx, gvrProvider, FluxNamespace, app)
	if err != nil || parent == nil {
		return out, "", err
	}
	return out, parent.GetAnnotations()[EnvironmentsErrorAnnotation], nil
}
