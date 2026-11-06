package kube

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRunCronJob(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "shop", UID: "u1"},
		Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "nightly"}},
		}},
	}
	c := &Client{Core: fake.NewClientset(cj)}
	name, err := c.RunCronJob(context.Background(), "shop", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	j, err := c.Core.BatchV1().Jobs("shop").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "nightly-run-") || len(j.OwnerReferences) != 1 || j.OwnerReferences[0].UID != "u1" ||
		j.Labels["app.kubernetes.io/name"] != "nightly" || j.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" ||
		j.Spec.TTLSecondsAfterFinished == nil {
		t.Errorf("job: %+v", j)
	}
	if _, err := c.RunCronJob(context.Background(), "shop", "nope"); err == nil {
		t.Error("ran a CronJob that isn't there")
	}
}
