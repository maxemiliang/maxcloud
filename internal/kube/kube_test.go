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

func fn(kind, apiVersion, name string, direct bool) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	o.SetName(name)
	o.SetNamespace("app")
	l := map[string]string{"app.kubernetes.io/part-of": "maxcloud-functions"}
	if direct {
		l[DirectLabel] = FieldManager
	}
	o.SetLabels(l)
	return o
}

func TestPruneOnlyRemovedDirectFunctions(t *testing.T) {
	keep := fn("Service", "serving.knative.dev/v1", "api", true)
	gone := fn("Service", "serving.knative.dev/v1", "old", true)
	goneJob := fn("CronJob", "batch/v1", "nightly", true)
	fromGit := fn("Service", "serving.knative.dev/v1", "flux-owned", false)
	db := fn("Cluster", "postgresql.cnpg.io/v1", "db", true)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrKService: "ServiceList", gvrScaledObject: "ScaledObjectList", gvrCNPGCluster: "ClusterList",
		{Group: "batch", Version: "v1", Resource: "cronjobs"}:   "CronJobList",
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
	}, keep, gone, goneJob, fromGit, db)
	c := &Client{Core: fake.NewClientset(), Dyn: dyn}
	deleted, err := c.Prune(context.Background(), "app", []map[string]any{keep.Object})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(deleted)
	if strings.Join(deleted, " ") != "CronJob/nightly Service/old" {
		t.Errorf("deleted %v", deleted)
	}
	for _, name := range []string{"api", "flux-owned"} {
		if ok, _ := c.Exists(context.Background(), gvrKService, "app", name); !ok {
			t.Errorf("%s was deleted", name)
		}
	}
	if ok, _ := c.Exists(context.Background(), gvrCNPGCluster, "app", "db"); !ok {
		t.Error("the database was deleted")
	}
}

func TestPsqlError(t *testing.T) {
	in := "ERROR:  no table maxcloud_jobs in this database\nCONTEXT:  PL/pgSQL function inline_code_block line 3 at RAISE\n"
	if got := psqlError(in); got != "no table maxcloud_jobs in this database" {
		t.Errorf("psqlError = %q", got)
	}
	in = "ERROR:  syntax error at or near \"x\"\nLINE 1: select x y\n                 ^\n"
	if got := psqlError(in); got != `syntax error at or near "x"` {
		t.Errorf("psqlError = %q", got)
	}
}
