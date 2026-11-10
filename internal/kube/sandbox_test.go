package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func sandboxClient(objs ...runtime.Object) *Client {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrProvider:     "ResourceSetInputProviderList",
		gvrSandboxClaim: "SandboxClaimList",
		gvrSandboxPool:  "SandboxWarmPoolList",
	}, objs...)
	return &Client{Core: fake.NewClientset(), Dyn: dyn}
}

func TestCheckSandboxUser(t *testing.T) {
	for user, ok := range map[string]bool{
		"alice": true, "a-b2": true,
		"": false, "Alice": false, "a.b": false, "a@b": false, "-a": false, "a--b": false, "1a": false,
		strings.Repeat("a", 41): false,
	} {
		if err := CheckSandboxUser(user); (err == nil) != ok {
			t.Errorf("%q: %v", user, err)
		}
	}
}

// The first use makes the user's entry, as the platform's admission policy
// wants it; after that there's nothing to make.
func TestEnsureSandbox(t *testing.T) {
	ctx := context.Background()
	c := sandboxClient()
	if made, err := c.EnsureSandbox(ctx, "alice"); err != nil || !made {
		t.Fatalf("first: %v, %v", made, err)
	}
	o, err := c.Dyn.Resource(gvrProvider).Namespace(FluxNamespace).Get(ctx, "sandbox-alice", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if o.GetLabels()[SandboxLabel] != "true" || str(o, "spec", "type") != "Static" || str(o, "spec", "defaultValues", "user") != "alice" {
		t.Errorf("entry: %v", o.Object)
	}
	if values, _, _ := unstructured.NestedMap(o.Object, "spec", "defaultValues"); len(values) != 1 {
		t.Errorf("only user belongs in a user's own entry: %v", values)
	}
	if made, err := c.EnsureSandbox(ctx, "alice"); err != nil || made {
		t.Errorf("second: %v, %v", made, err)
	}
	if _, err := c.EnsureSandbox(ctx, "Not.A.Label"); err == nil {
		t.Error("want an error for a name that can't be a namespace")
	}
}

// A provider of that name that isn't a sandbox entry (an app's, say) is
// not taken over.
func TestEnsureSandboxLeavesOtherEntriesAlone(t *testing.T) {
	other := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSetInputProvider",
		"metadata": map[string]any{"name": "sandbox-alice", "namespace": FluxNamespace},
	}}
	c := sandboxClient(other)
	if _, err := c.EnsureSandbox(context.Background(), "alice"); err == nil || !strings.Contains(err.Error(), "isn't a sandbox entry") {
		t.Errorf("err = %v", err)
	}
}

// The token Secret always holds exactly the one key: the platform refuses
// anything else in a sandbox namespace.
func TestSandboxToken(t *testing.T) {
	ctx := context.Background()
	c := sandboxClient()
	if has, _ := c.HasSandboxToken(ctx, "alice"); has {
		t.Error("no token yet")
	}
	for _, token := range []string{"first", "second"} {
		if err := c.SetSandboxToken(ctx, "alice", token); err != nil {
			t.Fatal(err)
		}
		s, err := c.Core.CoreV1().Secrets("sbx-alice").Get(ctx, SandboxTokenSecret, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if s.Type != corev1.SecretTypeOpaque || len(s.Data) != 1 || string(s.Data[SandboxTokenKey]) != token {
			t.Errorf("secret after %s: %v %v", token, s.Type, s.Data)
		}
	}
	if has, _ := c.HasSandboxToken(ctx, "alice"); !has {
		t.Error("token set")
	}
	if had, err := c.UnsetSandboxToken(ctx, "alice"); err != nil || !had {
		t.Errorf("unset: %v, %v", had, err)
	}
	if had, err := c.UnsetSandboxToken(ctx, "alice"); err != nil || had {
		t.Errorf("unset again: %v, %v", had, err)
	}
}

func TestStartSession(t *testing.T) {
	ctx := context.Background()
	c := sandboxClient()
	until := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	name, err := c.StartSession(ctx, "alice", "work", until)
	if err != nil || name != "work" {
		t.Fatalf("%q, %v", name, err)
	}
	o, err := c.Dyn.Resource(gvrSandboxClaim).Namespace("sbx-alice").Get(ctx, "work", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if str(o, "spec", "warmPoolRef", "name") != "claude-code" ||
		str(o, "spec", "lifecycle", "shutdownTime") != "2026-11-10T20:00:00Z" ||
		str(o, "spec", "lifecycle", "shutdownPolicy") != "Delete" {
		t.Errorf("claim: %v", o.Object["spec"])
	}
	if _, err := c.StartSession(ctx, "alice", "work", time.Time{}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("same name twice: %v", err)
	}
	if _, err := c.StartSession(ctx, "alice", "Not_A_Name", time.Time{}); err == nil {
		t.Error("want an error for a name that can't be a pod's")
	}

	s, err := c.GetSession(ctx, "alice", "work")
	if err != nil || s.Ready || !s.Until.Equal(until) {
		t.Errorf("session: %+v, %v", s, err)
	}
	if err := c.SetSessionEnd(ctx, "alice", "work", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.GetSession(ctx, "alice", "work"); !s.Until.IsZero() {
		t.Errorf("end time should be gone: %v", s.Until)
	}
	if err := c.StopSession(ctx, "alice", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSession(ctx, "alice", "work"); !errors.Is(err, ErrNoSession) {
		t.Errorf("after stop: %v", err)
	}
	if err := c.StopSession(ctx, "alice", "work"); !errors.Is(err, ErrNoSession) {
		t.Errorf("stop twice: %v", err)
	}
}

func claim(name string, created time.Time, conditions ...map[string]any) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1", "kind": "SandboxClaim",
		"metadata": map[string]any{"name": name, "namespace": "sbx-alice"},
	}}
	o.SetCreationTimestamp(metav1.NewTime(created))
	if len(conditions) > 0 {
		list := make([]any, len(conditions))
		for i, c := range conditions {
			list[i] = c
		}
		o.Object["status"] = map[string]any{"conditions": list}
	}
	return o
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	c := sandboxClient(
		claim("newer", t0.Add(time.Hour), map[string]any{"type": "Ready", "status": "False", "reason": "ReconcilerError",
			"message": `pods "newer" is forbidden: exceeded quota: sessions, requested: pods=1, used: pods=2, limited: pods=2`}),
		claim("older", t0, map[string]any{"type": "Ready", "status": "True", "reason": "DependenciesReady"}),
	)
	all, err := c.Sessions(ctx, "alice")
	if err != nil || len(all) != 2 || all[0].Name != "older" || !all[0].Ready || all[1].Ready {
		t.Fatalf("%+v, %v", all, err)
	}
	// Over the sandbox's limit: said in words, and not worth waiting for.
	why := c.sessionProblem(ctx, "alice", &all[1])
	if !strings.Contains(why, "as many sessions running as your sandbox allows") {
		t.Errorf("why = %q", why)
	}
	err = c.WaitSession(ctx, "alice", "newer", nil)
	if !errors.Is(err, ErrSessionCantStart) {
		t.Errorf("wait: %v", err)
	}
	if err := c.WaitSession(ctx, "alice", "older", nil); err != nil {
		t.Errorf("a session that's up: %v", err)
	}
}

// A session whose pod has no token to read says so, not "waiting".
func TestSessionProblemNoToken(t *testing.T) {
	ctx := context.Background()
	c := sandboxClient(claim("work", time.Now()))
	c.Core = fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "sbx-alice"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  SandboxContainer,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: `secret "claude-code" not found`}},
		}}},
	})
	s, _ := c.GetSession(ctx, "alice", "work")
	if why := c.sessionProblem(ctx, "alice", s); !strings.Contains(why, "mcloud sandbox token set") {
		t.Errorf("why = %q", why)
	}
}
