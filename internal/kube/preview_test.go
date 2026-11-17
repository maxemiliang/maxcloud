package kube

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

func TestPreviews(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GVRPreview: "PreviewList"})
	c := &Client{Core: fake.NewClientset(), Dyn: dyn}
	ctx := context.Background()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := c.PutPreview(ctx, "shop", "ann", at); err != nil {
		t.Fatal(err)
	}
	// Again: moves the expiry on.
	if err := c.PutPreview(ctx, "shop", "ann", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ps, err := c.Previews(ctx, "shop")
	if err != nil || len(ps) != 1 || ps[0].Name != "ann" || !ps[0].ExpiresAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("previews = %+v, %v", ps, err)
	}
	if err := c.DeletePreview(ctx, "shop", "ann"); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.GetPreview(ctx, "shop", "ann"); p != nil {
		t.Errorf("still there: %+v", p)
	}
}

func TestEnvironments(t *testing.T) {
	app := &unstructured.Unstructured{Object: (&registry.Entry{Name: "shop", Repo: "a/shop"}).Object()}
	app.SetAnnotations(map[string]string{EnvironmentsErrorAnnotation: "environments: at most 3"})
	staging := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSetInputProvider",
		"metadata": map[string]any{"name": "shop--staging", "namespace": FluxNamespace, "labels": map[string]any{
			registry.AppLabel: "true", EnvironmentOfLabel: "shop", EnvironmentLabel: "staging", EnvironmentKindLabel: "environment"}},
		"spec": map[string]any{"type": "Static", "defaultValues": map[string]any{"name": "shop--staging", "branch": "staging"}},
	}}
	ks := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "shop--staging", "namespace": FluxNamespace},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": "Applied"}}},
	}}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvrProvider: "ResourceSetInputProviderList", gvrKustomization: "KustomizationList"},
		app, staging, ks)
	c := &Client{Core: fake.NewClientset(), Dyn: dyn}
	envs, problem, err := c.Environments(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0].Env != "staging" || envs[0].Branch != "staging" || !envs[0].Applied || !envs[0].Deploy.Ready() {
		t.Errorf("environments = %+v", envs)
	}
	if problem != "environments: at most 3" {
		t.Errorf("problem = %q", problem)
	}
}
