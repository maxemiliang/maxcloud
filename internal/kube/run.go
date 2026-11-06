package kube

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// The image mcloud call runs curl in, inside the app's namespace.
// renovate: datasource=docker depName=curlimages/curl
const curlImage = "curlimages/curl:8.11.1"

// RunCronJob starts a Job from a CronJob now (like kubectl create job
// --from), owned by the CronJob and removed an hour after it finishes.
func (c *Client) RunCronJob(ctx context.Context, ns, name string) (string, error) {
	cj, err := c.Core.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	ann := map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	for k, v := range cj.Spec.JobTemplate.Annotations {
		ann[k] = v
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-run-%d", name, time.Now().Unix()%1000000),
			Namespace:   ns,
			Labels:      cj.Spec.JobTemplate.Labels,
			Annotations: ann,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: ptr.To(true),
			}},
		},
		Spec: cj.Spec.JobTemplate.Spec,
	}
	job.Spec.TTLSecondsAfterFinished = ptr.To[int32](3600)
	j, err := c.Core.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return j.Name, nil
}

// FollowJob streams the logs of a Job's pods as they run, and returns nil
// if it succeeds, or an error saying how it failed.
func (c *Client) FollowJob(ctx context.Context, ns, name string, w io.Writer) error {
	seen := map[string]bool{}
	for {
		job, err := c.Core.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		pods, err := c.Core.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name})
		if err != nil {
			return err
		}
		sort.Slice(pods.Items, func(i, j int) bool {
			return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
		})
		for _, p := range pods.Items {
			if seen[p.Name] || p.Status.Phase == corev1.PodPending {
				continue
			}
			seen[p.Name] = true
			if err := c.copyLogs(ctx, ns, p.Name, "", w); err != nil {
				fmt.Fprintf(w, "(no logs from %s: %v)\n", p.Name, err)
			}
		}
		for _, cond := range job.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}
			switch cond.Type {
			case batchv1.JobComplete:
				return nil
			case batchv1.JobFailed:
				return fmt.Errorf("job %s failed: %s", name, cond.Message)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) copyLogs(ctx context.Context, ns, pod, container string, w io.Writer) error {
	r, err := c.Core.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, Follow: true}).Stream(ctx)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	return err
}

// Request is what mcloud call sends.
type Request struct {
	Method  string
	URL     string
	Headers []string // "Name: value"
	Body    string
}

// Call sends req from a short-lived pod in the app's namespace (so private,
// cluster-local services answer it, and its network policies allow it),
// writes curl's output (status, headers, body) to w, and removes the pod.
func (c *Client) Call(ctx context.Context, ns string, req Request, w io.Writer) error {
	args := []string{"-sS", "--include", "--max-time", "100", "-X", req.Method}
	for _, h := range req.Headers {
		args = append(args, "-H", h)
	}
	if req.Body != "" {
		args = append(args, "--data-binary", req.Body)
	}
	args = append(args, req.URL)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "mcloud-call-",
			Labels:       map[string]string{"app.kubernetes.io/managed-by": FieldManager},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			PriorityClassName:            "tenant",
			ActiveDeadlineSeconds:        ptr.To[int64](150),
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To[int64](65534),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:  "curl",
				Image: curlImage,
				Args:  args,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					ReadOnlyRootFilesystem:   ptr.To(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	p, err := c.Core.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		_ = c.Core.CoreV1().Pods(ns).Delete(context.Background(), p.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
	}()
	for {
		cur, err := c.Core.CoreV1().Pods(ns).Get(ctx, p.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch cur.Status.Phase {
		case corev1.PodSucceeded:
			return c.copyLogs(ctx, ns, p.Name, "curl", w)
		case corev1.PodFailed:
			_ = c.copyLogs(ctx, ns, p.Name, "curl", w)
			return fmt.Errorf("the request failed (%s)", firstNonEmpty(cur.Status.Message, cur.Status.Reason, "curl exited with an error"))
		case corev1.PodPending:
			if why := podProblem(cur); why != "" {
				return fmt.Errorf("the request's pod can't start: %s", why)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
