package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// The Postgres database every maxcloud app database has (the Cluster's
// initdb database, owned by the app's user).
const appDatabase = "app"

// PrimaryPod is the pod of a CloudNativePG cluster's primary.
func (c *Client) PrimaryPod(ctx context.Context, ns, cluster string) (string, error) {
	pods, err := c.Core.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + cluster + ",cnpg.io/instanceRole=primary",
	})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	clusters, _ := c.list(ctx, gvrCNPGCluster, ns, metav1.ListOptions{})
	var names []string
	for _, cl := range clusters {
		names = append(names, cl.GetName())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", fmt.Errorf("app %s has no database (mcloud apps set %s --db)", ns, ns)
	}
	return "", fmt.Errorf("no running primary for database %s in %s (its databases: %s)", cluster, ns, strings.Join(names, ", "))
}

// Psql runs a script with psql in the primary of a CloudNativePG cluster,
// as the postgres superuser over the local socket, in the app database.
// vars become psql variables: refer to them as :'name' in the script, so
// values are quoted by psql and never pasted into the SQL. Output is
// unaligned, tab-separated rows without headers.
func (c *Client) Psql(ctx context.Context, ns, cluster, script string, vars map[string]string) (string, error) {
	if c.config == nil {
		return "", errors.New("no cluster connection")
	}
	pod, err := c.PrimaryPod(ctx, ns, cluster)
	if err != nil {
		return "", err
	}
	cmd := []string{"psql", "-X", "-q", "-A", "-t", "-F", "\t", "-v", "ON_ERROR_STOP=1", "-d", appDatabase}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd = append(cmd, "-v", k+"="+vars[k])
	}
	req := c.Core.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "postgres",
			Command:   cmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(c.config, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  strings.NewReader(script),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		if msg := psqlError(stderr.String()); msg != "" {
			return "", fmt.Errorf("psql in %s/%s: %s", ns, pod, msg)
		}
		return "", fmt.Errorf("psql in %s/%s: %w", ns, pod, err)
	}
	return stdout.String(), nil
}

// psqlError keeps psql's error lines, without the CONTEXT and LINE noise.
func psqlError(stderr string) string {
	var keep []string
	for _, l := range strings.Split(stderr, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "CONTEXT:") || strings.HasPrefix(t, "LINE ") || strings.HasPrefix(t, "^") {
			continue
		}
		keep = append(keep, strings.TrimPrefix(t, "ERROR:  "))
	}
	return strings.Join(keep, "; ")
}
