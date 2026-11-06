package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestApplySecretInPlace(t *testing.T) {
	ctx := context.Background()
	c := &Client{Core: fake.NewClientset()}
	if err := c.ApplySecret(ctx, "shop", "app-secrets", map[string]string{"A": "1", "B": "2"}); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Core.CoreV1().Secrets("shop").Get(ctx, "app-secrets", metav1.GetOptions{})
	uid := "kept"
	s.UID = "kept"
	c.Core.CoreV1().Secrets("shop").Update(ctx, s, metav1.UpdateOptions{})

	// Unsetting B: B goes, the Secret stays the same object (never deleted).
	if err := c.ApplySecret(ctx, "shop", "app-secrets", map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	s, _ = c.Core.CoreV1().Secrets("shop").Get(ctx, "app-secrets", metav1.GetOptions{})
	if string(s.UID) != uid || len(s.Data) != 1 || string(s.Data["A"]) != "1" {
		t.Errorf("secret: %+v", s)
	}
	if s.Labels["app.kubernetes.io/managed-by"] != "mcloud" || s.Annotations["kustomize.toolkit.fluxcd.io/prune"] != "disabled" {
		t.Errorf("marks: %v %v", s.Labels, s.Annotations)
	}
}

func TestAdoptSecret(t *testing.T) {
	ctx := context.Background()
	flux := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "app-secrets", Namespace: "shop",
		Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop"},
	}, Data: map[string][]byte{"K": []byte("v")}}
	c := &Client{Core: fake.NewClientset(flux)}
	if ok, err := c.AdoptSecret(ctx, "shop", "app-secrets"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s, _ := c.Core.CoreV1().Secrets("shop").Get(ctx, "app-secrets", metav1.GetOptions{})
	if s.Annotations["kustomize.toolkit.fluxcd.io/prune"] != "disabled" || s.Labels["app.kubernetes.io/managed-by"] != "mcloud" || string(s.Data["K"]) != "v" {
		t.Errorf("adopted: %+v", s)
	}
	if ok, _ := c.AdoptSecret(ctx, "other", "app-secrets"); ok {
		t.Error("adopted a Secret that isn't there")
	}
}
