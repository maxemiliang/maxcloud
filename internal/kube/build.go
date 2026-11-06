package kube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// ClusterBuild is a buildpacks build in the app's namespace: the Paketo
// builder's lifecycle (creator) in a pod, with the source streamed in by
// exec, pushing straight to the registry. No docker on your machine.
type ClusterBuild struct {
	Builder  string            // builder image
	Image    string            // repo:tag to push
	Cache    string            // repo:tag of the build cache
	Registry string            // the registry's host
	Token    string            // push token for it
	Env      map[string]string // build-time env (project.toml's io.buildpacks.build.env)
}

const (
	buildReady  = "/workspace/.mcloud-ready"
	reportBegin = "--- mcloud report ---"
	// The CNB user of Paketo's builders.
	cnbUID, cnbGID = 1001, 1000
)

// buildScript waits for the source, writes the platform env, builds and
// pushes, then prints the lifecycle's report (with the digest).
func buildScript(b ClusterBuild) string {
	var s strings.Builder
	s.WriteString("set -e\nwhile [ ! -f " + buildReady + " ]; do sleep 1; done\nmkdir -p /platform/env\n")
	for k, v := range b.Env {
		fmt.Fprintf(&s, "printf '%%s' '%s' > /platform/env/%s\n", strings.ReplaceAll(v, "'", `'\''`), k)
	}
	fmt.Fprintf(&s, "/cnb/lifecycle/creator -app=/workspace/app -layers=/layers -platform=/platform -cache-image=%s -report=/workspace/report.toml %s\n", b.Cache, b.Image)
	s.WriteString("echo '" + reportBegin + "'\ncat /workspace/report.toml\n")
	return s.String()
}

// buildPod is the pod (and the Secret with the registry login) a build
// runs in.
func buildPod(name string, b ClusterBuild) (*corev1.Pod, *corev1.Secret) {
	auth, _ := json.Marshal(map[string]string{b.Registry: "Basic " + base64.StdEncoding.EncodeToString([]byte("maxcloud:"+b.Token))})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": FieldManager}},
		StringData: map[string]string{"CNB_REGISTRY_AUTH": string(auth)},
	}
	empty := func(n string) corev1.Volume {
		return corev1.Volume{Name: n, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": FieldManager, "maxcloud.gronblom.dev/build": "true"}},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			PriorityClassName:            "tenant",
			ActiveDeadlineSeconds:        ptr.To[int64](1800),
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To[int64](cnbUID),
				RunAsGroup:     ptr.To[int64](cnbGID),
				FSGroup:        ptr.To[int64](cnbGID),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:    "build",
				Image:   b.Builder,
				Command: []string{"/bin/sh", "-c", buildScript(b)},
				Env:     []corev1.EnvVar{{Name: "CNB_PLATFORM_API", Value: "0.13"}},
				EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "layers", MountPath: "/layers"}, {Name: "platform", MountPath: "/platform"}},
			}},
			Volumes: []corev1.Volume{empty("workspace"), empty("layers"), empty("platform")},
		},
	}
	return pod, secret
}

// Build runs b in namespace ns: source is a tar stream of the app's files.
// Build output goes to w. Returns the pushed image's digest.
func (c *Client) Build(ctx context.Context, ns string, b ClusterBuild, source io.Reader, w io.Writer) (string, error) {
	name := fmt.Sprintf("mcloud-build-%d", time.Now().Unix())
	pod, secret := buildPod(name, b)
	pods, secrets := c.Core.CoreV1().Pods(ns), c.Core.CoreV1().Secrets(ns)
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("the build's registry login: %w", err)
	}
	defer secrets.Delete(context.Background(), name, metav1.DeleteOptions{})
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("the build pod: %w", err)
	}
	defer pods.Delete(context.Background(), name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})

	fmt.Fprintf(w, "Build pod %s/%s: waiting for it to start (the first pull of the builder takes a while) ...\n", ns, name)
	if err := c.waitRunning(ctx, ns, name); err != nil {
		return "", err
	}
	fmt.Fprintln(w, "Uploading the source ...")
	var stderr bytes.Buffer
	if err := c.Exec(ctx, ns, name, "build", []string{"sh", "-c", "mkdir -p /workspace/app && tar -x -C /workspace/app -f -"}, source, io.Discard, &stderr); err != nil {
		return "", fmt.Errorf("uploading the source: %w: %s", err, stderr.String())
	}
	if err := c.Exec(ctx, ns, name, "build", []string{"touch", buildReady}, nil, io.Discard, io.Discard); err != nil {
		return "", err
	}

	// The build's output, then the report.
	var logs bytes.Buffer
	if err := c.copyLogs(ctx, ns, name, "build", io.MultiWriter(&logs, &reportFilter{w: w})); err != nil {
		return "", err
	}
	for {
		p, err := pods.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if p.Status.Phase == corev1.PodFailed {
			return "", errors.New("the build failed (its output is above)")
		}
		if p.Status.Phase == corev1.PodSucceeded {
			break
		}
		if err := sleepCtx(ctx, time.Second); err != nil {
			return "", err
		}
	}
	digest := reportDigest(logs.String())
	if digest == "" {
		return "", errors.New("the build's report has no image digest")
	}
	return digest, nil
}

// reportDigest finds the image digest in the lifecycle's report.toml
// ([image] digest = "sha256:...") printed after reportBegin.
func reportDigest(logs string) string {
	_, report, ok := strings.Cut(logs, reportBegin)
	if !ok {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(report))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && strings.TrimSpace(k) == "digest" {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// reportFilter passes the build's output through, up to the report.
type reportFilter struct {
	w    io.Writer
	done bool
}

func (f *reportFilter) Write(p []byte) (int, error) {
	if f.done {
		return len(p), nil
	}
	if i := bytes.Index(p, []byte(reportBegin)); i >= 0 {
		f.done = true
		_, err := f.w.Write(p[:i])
		return len(p), err
	}
	_, err := f.w.Write(p)
	return len(p), err
}

// Describe runs the app's image with MAXCLOUD_DESCRIBE=1 in its namespace
// (pulling with the namespace's own credentials) and returns what it
// prints: the description of its functions.
func (c *Client) Describe(ctx context.Context, ns, image string) ([]byte, error) {
	name := fmt.Sprintf("mcloud-describe-%d", time.Now().Unix())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": FieldManager}},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			PriorityClassName:            "tenant",
			ActiveDeadlineSeconds:        ptr.To[int64](300),
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:  "describe",
				Image: image,
				Env:   []corev1.EnvVar{{Name: "MAXCLOUD_DESCRIBE", Value: "1"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	pods := c.Core.CoreV1().Pods(ns)
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, err
	}
	defer pods.Delete(context.Background(), name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
	for {
		p, err := pods.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		switch p.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			var out bytes.Buffer
			if err := c.copyLogs(ctx, ns, name, "describe", &out); err != nil {
				return nil, err
			}
			if p.Status.Phase == corev1.PodFailed {
				return nil, fmt.Errorf("describing the image failed: %s", strings.TrimSpace(out.String()))
			}
			return out.Bytes(), nil
		case corev1.PodPending:
			if why := podProblem(p); why != "" {
				return nil, fmt.Errorf("describing the image: %s", why)
			}
		}
		if err := sleepCtx(ctx, time.Second); err != nil {
			return nil, err
		}
	}
}

func (c *Client) waitRunning(ctx context.Context, ns, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		p, err := c.Core.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch p.Status.Phase {
		case corev1.PodRunning:
			return nil
		case corev1.PodFailed, corev1.PodSucceeded:
			return fmt.Errorf("the build pod ended before it started: %s", firstNonEmpty(p.Status.Message, p.Status.Reason))
		}
		if why := podProblem(p); why != "" && !strings.Contains(why, "ContainerCreating") && !strings.Contains(why, "Pulling") {
			return fmt.Errorf("the build pod can't start: %s", why)
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("the build pod didn't start: %w", err)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
