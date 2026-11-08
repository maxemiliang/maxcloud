package kube

import (
	"context"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

func TestWhoami(t *testing.T) {
	core := fake.NewClientset()
	core.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		r := &authv1.SelfSubjectReview{}
		r.Status.UserInfo.Username = "authelia:alice"
		return true, r, nil
	})
	c := &Client{Core: core}
	got, err := c.Whoami(context.Background())
	if err != nil || got != "authelia:alice" {
		t.Fatalf("Whoami = %q, %v", got, err)
	}
}

// Changing an app keeps its owners and leaves the controller's registration
// ID alone.
func TestPutRegistryEntryKeepsOwnersAndRegistration(t *testing.T) {
	e := &registry.Entry{Name: "shop", Repo: "a/shop", Owners: "alice,bob"}
	cur := &unstructured.Unstructured{Object: e.Object()}
	cur.SetAnnotations(map[string]string{RegistrationAnnotation: "aaa"})
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvrProvider: "ResourceSetInputProviderList"}, cur)
	c := &Client{Core: fake.NewClientset(), Dyn: dyn}

	got, err := c.GetRegistryEntry(context.Background(), "shop")
	if err != nil || got.Registration != "aaa" || got.Entry.Owners != "alice,bob" {
		t.Fatalf("got %+v, %v", got, err)
	}
	next := *got.Entry
	next.CPU = "500m"
	if err := c.PutRegistryEntry(context.Background(), &next); err != nil {
		t.Fatal(err)
	}
	after, _ := c.GetRegistryEntry(context.Background(), "shop")
	if after.Entry.Owners != "alice,bob" || after.Entry.CPU != "500m" || after.Registration != "aaa" {
		t.Errorf("after = %+v (%+v)", after, after.Entry)
	}
}
