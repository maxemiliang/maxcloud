package kube

import (
	"context"
	"regexp"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRecordDeployKeepsTheLastTen(t *testing.T) {
	ctx := context.Background()
	c := &Client{Core: fake.NewClientset()}
	objs := []map[string]any{{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": "nightly"}}}
	for i := 1; i <= 12; i++ {
		n, err := c.RecordDeploy(ctx, "shop", DirectDeploy{Image: "img@sha256:" + string(rune('a'+i)), Functions: []string{"cron/nightly"}, Migrations: []string{"0001_init.sql"}}, objs)
		if err != nil || n != i {
			t.Fatalf("deploy %d: %d, %v", i, n, err)
		}
	}
	ds, err := c.DirectDeploys(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != HistoryKeep || ds[0].Number != 12 || ds[len(ds)-1].Number != 3 {
		t.Fatalf("kept %d deploys, %d to %d", len(ds), ds[0].Number, ds[len(ds)-1].Number)
	}
	if ds[0].Migrations[0] != "0001_init.sql" || ds[0].Functions[0] != "cron/nightly" {
		t.Errorf("record: %+v", ds[0])
	}
	got, err := c.DeployObjects(ctx, "shop", 5)
	if err != nil || len(got) != 1 || got[0]["kind"] != "CronJob" {
		t.Errorf("objects of 5: %v, %v", got, err)
	}
	if _, err := c.DeployObjects(ctx, "shop", 1); err == nil {
		t.Error("deploy 1 should be gone")
	}
}

func obj(apiVersion, kind, ns, name string, fields map[string]any) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{}}
	for k, v := range fields {
		o.Object[k] = v
	}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	o.SetNamespace(ns)
	o.SetName(name)
	return o
}

func TestStatusBucketsDomainsAndSyncError(t *testing.T) {
	provider := obj("fluxcd.controlplane.io/v1", "ResourceSetInputProvider", FluxNamespace, "shop", map[string]any{
		"spec": map[string]any{"defaultValues": map[string]any{"name": "shop", "repo": "o/shop"}},
	})
	provider.SetAnnotations(map[string]string{SyncErrorAnnotation: "quota.cpu 4 is above 1", SyncedRevisionAnnotation: "main@sha1:0123456789abcdef"})
	ready := func(status, reason, msg string) map[string]any {
		return map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg}}}
	}
	bucket := obj("maxcloud.gronblom.dev/v1alpha1", "Bucket", "shop", "uploads", map[string]any{
		"spec":   map[string]any{"public": true},
		"status": map[string]any{"bucketName": "mc-shop--uploads", "sizeBytes": int64(2048), "observedGeneration": int64(1), "conditions": ready("True", "Ready", "")["conditions"]},
	})
	domain := obj("maxcloud.gronblom.dev/v1alpha1", "Domain", "shop", "shop.example.com", map[string]any{
		"spec": map[string]any{"service": "web"}, "status": ready("False", "NotAllowed", "not on the allowlist"),
	})
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrProvider: "ResourceSetInputProviderList", gvrKustomization: "KustomizationList", gvrGitRepository: "GitRepositoryList",
		gvrKService: "ServiceList", gvrScaledJob: "ScaledJobList", gvrScaledObject: "ScaledObjectList", gvrCNPGCluster: "ClusterList",
		gvrBucket: "BucketList", gvrDomain: "DomainList",
	}, provider, bucket, domain)
	core := fake.NewClientset()
	core.CoreV1().Namespaces().Create(context.Background(), nsObj("shop"), metav1.CreateOptions{})
	c := &Client{Core: core, Dyn: dyn}
	st, err := c.AppStatus(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if st.SyncError != "quota.cpu 4 is above 1" || st.SyncedRevision == "" {
		t.Errorf("sync: %q %q", st.SyncError, st.SyncedRevision)
	}
	if len(st.Buckets) != 1 || st.Buckets[0].Bucket != "mc-shop--uploads" || !st.Buckets[0].Public || *st.Buckets[0].Size != 2048 || !st.Buckets[0].Ready.Ready() || !st.Buckets[0].Observed {
		t.Errorf("buckets: %+v", st.Buckets)
	}
	if len(st.Domains) != 1 || st.Domains[0].Service != "web" || st.Domains[0].Ready.Reason != "NotAllowed" {
		t.Errorf("domains: %+v", st.Domains)
	}
}

func TestLogPodPattern(t *testing.T) {
	c := testClient()
	for name, want := range map[string]string{
		"web":    `web-[0-9]+-deployment-.+`,
		"worker": `worker-([0-9]+(-[a-z0-9-]+)?|[a-z0-9]{5,10}-[a-z0-9]{5})`,
		"":       "",
	} {
		got, err := c.LogPodPattern(context.Background(), "app", name)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", name, got, err)
		}
	}
}

func nsObj(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// The patterns pick a service's pods and not a longer-named one's.
func TestLogPodPatternMatches(t *testing.T) {
	c := testClient()
	for name, pods := range map[string]map[string]bool{
		"worker": {"worker-5d8f6c7b9-x7k2p": true, "worker-two-77f8d9c6b-abcde": false, "worker-29012345-abcde": true, "worker-1": true},
		"web":    {"web-00001-deployment-5d8f6c7b9-x7k2p": true, "web-api-00001-deployment-5d8f6c7b9-x7k2p": false},
	} {
		p, _ := c.LogPodPattern(context.Background(), "app", name)
		re := regexp.MustCompile("^(?:" + p + ")$")
		for pod, want := range pods {
			if re.MatchString(pod) != want {
				t.Errorf("%s: %s matches = %v", name, pod, !want)
			}
		}
	}
}
