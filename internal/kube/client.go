// Package kube reads an app's state from the cluster: what Flux applied,
// its services and workloads, and its logs.
package kube

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// FluxNamespace holds the registry's providers and each app's Flux objects.
const FluxNamespace = "flux-system"

var (
	gvrProvider      = schema.GroupVersionResource{Group: "fluxcd.controlplane.io", Version: "v1", Resource: "resourcesetinputproviders"}
	gvrKustomization = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	gvrGitRepository = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	gvrKService      = schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	gvrScaledJob     = schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledjobs"}
	gvrScaledObject  = schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	gvrCNPGCluster   = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
)

type Client struct {
	Core kubernetes.Interface
	Dyn  dynamic.Interface
}

// New connects using kubeconfig if set, else $MCLOUD_KUBECONFIG, else the
// usual $KUBECONFIG / ~/.kube/config.
func New(kubeconfig, kubeContext string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig == "" {
		kubeconfig = os.Getenv("MCLOUD_KUBECONFIG")
	}
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w (set MCLOUD_KUBECONFIG to the homelab's kubeconfig)", err)
	}
	cfg.Timeout = 30 * time.Second
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Core: core, Dyn: dyn}, nil
}

// list returns nothing, not an error, when the kind isn't installed.
func (c *Client) list(ctx context.Context, gvr schema.GroupVersionResource, ns string, opts metav1.ListOptions) ([]unstructured.Unstructured, error) {
	l, err := c.Dyn.Resource(gvr).Namespace(ns).List(ctx, opts)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

func (c *Client) get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	o, err := c.Dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return o, err
}

// Condition is one status condition of an object.
type Condition struct {
	Status  string // True, False, Unknown; "" if the object has none yet
	Reason  string
	Message string
}

func (c Condition) Ready() bool { return c.Status == "True" }

func condition(o *unstructured.Unstructured, typ string) Condition {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, x := range conds {
		m, ok := x.(map[string]any)
		if !ok || m["type"] != typ {
			continue
		}
		s := func(k string) string { v, _ := m[k].(string); return v }
		return Condition{Status: s("status"), Reason: s("reason"), Message: s("message")}
	}
	return Condition{}
}

func str(o *unstructured.Unstructured, fields ...string) string {
	v, _, _ := unstructured.NestedFieldNoCopy(o.Object, fields...)
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
