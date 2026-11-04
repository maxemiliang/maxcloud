package kube

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func owned(kind, name string) []metav1.OwnerReference {
	t := true
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: &t}}
}

func pod(name string, labels map[string]string, owners []metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app", Labels: labels, OwnerReferences: owners}}
}

func testClient() *Client {
	core := fake.NewClientset(
		pod("web-00001-deployment-abc-1", map[string]string{"serving.knative.dev/service": "web"}, owned("ReplicaSet", "web-00001-deployment-abc")),
		pod("worker-5d8f-x", nil, owned("ReplicaSet", "worker-5d8f")),
		pod("worker-two-77-y", nil, owned("ReplicaSet", "worker-two-77")),
		pod("nightly-29012-z", nil, owned("Job", "nightly-29012")),
		pod("db-1", map[string]string{"cnpg.io/cluster": "db"}, owned("Cluster", "db")),
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "nightly-29012", Namespace: "app", OwnerReferences: owned("CronJob", "nightly")}},
	)
	ksvc := &unstructured.Unstructured{}
	ksvc.SetAPIVersion("serving.knative.dev/v1")
	ksvc.SetKind("Service")
	ksvc.SetName("web")
	ksvc.SetNamespace("app")
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvrKService: "ServiceList"}, ksvc)
	return &Client{Core: core, Dyn: dyn}
}

func TestPodMatching(t *testing.T) {
	c := testClient()
	for name, want := range map[string]string{
		"web":     "web-00001-deployment-abc-1",
		"worker":  "worker-5d8f-x", // not worker-two's pods
		"nightly": "nightly-29012-z",
		"db":      "db-1",
		"":        "db-1 nightly-29012-z web-00001-deployment-abc-1 worker-5d8f-x worker-two-77-y",
	} {
		o := LogOptions{Namespace: "app", Name: name}
		m, err := c.matcher(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		pods, err := c.pods(context.Background(), o, m)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range pods {
			got = append(got, p.Name)
		}
		sort.Strings(got)
		if strings.Join(got, " ") != want {
			t.Errorf("%q: got %v, want %s", name, got, want)
		}
	}
}

func TestPodProblem(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	crash := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m"},
		}}},
	}}
	if got := podProblem(crash); !strings.Contains(got, "CrashLoopBackOff") {
		t.Errorf("crash: %q", got)
	}
	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b", CreationTimestamp: old}, Status: corev1.PodStatus{
		Phase:      corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/2 nodes: Insufficient cpu"}},
	}}
	if got := podProblem(pending); !strings.Contains(got, "Insufficient cpu") {
		t.Errorf("pending: %q", got)
	}
	healthy := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "c"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if got := podProblem(healthy); got != "" {
		t.Errorf("healthy: %q", got)
	}
}
