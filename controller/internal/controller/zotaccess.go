package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// PushServiceAccount: mcloud pushes an app's images (direct deploys) with
	// a token of this ServiceAccount in the app's namespace, which only
	// someone who may manage that namespace can get.
	PushServiceAccount = "maxcloud-image-push"

	zotBaseConfigMap      = "zot-base"
	zotConfigMap          = "zot-config"
	zotDeployment         = "zot"
	zotConfigKey          = "config.json"
	zotBaseHashAnnotation = "maxcloud.gronblom.dev/zot-base"
)

// ZotAccess writes the registry's (zot's) configuration: the base from the
// ConfigMap zot-base, which the cluster's repo owns, plus one access rule
// per app on the platform, from the app registry:
//
//	<app>/**  read:          <app>:pull  (the app's pull ServiceAccount)
//	          read and push: <app>:push  (its push ServiceAccount, for mcloud)
//	                         github:<repo> (the app's repo's GitHub Actions)
//
// into the ConfigMap zot-config, which zot runs with. zot reloads access
// rules by itself; a change to the base (logins, storage) it reads only at
// start, so then the Deployment zot is restarted, by a hash of the base on
// its pod template.
type ZotAccess struct {
	client.Client
	// APIReader reads the ConfigMaps uncached.
	APIReader client.Reader
	Namespace string
}

// The one thing this reconciles.
var zotRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "zot"}}

// +kubebuilder:rbac:groups=fluxcd.controlplane.io,resources=resourcesetinputproviders,verbs=get;list;watch
// The ConfigMaps and the Deployment in the registry's namespace: a Role
// there (the cluster provides it), not this ClusterRole.

func (r *ZotAccess) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var base corev1.ConfigMap
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: zotBaseConfigMap}, &base); err != nil {
		return ctrl.Result{}, fmt.Errorf("base configuration %s/%s: %w", r.Namespace, zotBaseConfigMap, err)
	}
	baseJSON := base.Data[zotConfigKey]

	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(providerGVK.GroupVersion().WithKind(providerGVK.Kind + "List"))
	if err := r.List(ctx, &list, client.InNamespace(RegistryNamespace), client.MatchingLabels{appLabel: "true"}); err != nil {
		return ctrl.Result{}, err
	}
	var apps []zotApp
	for _, e := range list.Items {
		if !e.GetDeletionTimestamp().IsZero() {
			continue
		}
		v, _, _ := unstructured.NestedMap(e.Object, "spec", "defaultValues")
		name, _ := v["name"].(string)
		if name == "" {
			name = e.GetName()
		}
		repo, _ := v["repo"].(string)
		if deploy, _ := v["deploy"].(string); deploy == "direct" {
			repo = "" // nothing pushes from its repo
		}
		apps = append(apps, zotApp{Name: name, Repo: repo})
	}

	rendered, err := renderZotConfig(baseJSON, apps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("%s/%s: %w", r.Namespace, zotBaseConfigMap, err)
	}
	if err := r.writeConfig(ctx, rendered); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.restartOnBaseChange(ctx, hash(baseJSON)); err != nil {
		return ctrl.Result{}, err
	}
	// The base isn't watched (that would cache every ConfigMap in the
	// cluster): look again every few minutes.
	return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
}

type zotApp struct{ Name, Repo string }

// renderZotConfig adds the apps' rules to the base configuration. Rules in
// the base for the same pattern are kept as they are.
func renderZotConfig(base string, apps []zotApp) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(base), &cfg); err != nil {
		return nil, fmt.Errorf("%s isn't JSON: %w", zotConfigKey, err)
	}
	httpCfg, _ := cfg["http"].(map[string]any)
	if httpCfg == nil {
		return nil, fmt.Errorf("%s has no http section", zotConfigKey)
	}
	ac, _ := httpCfg["accessControl"].(map[string]any)
	if ac == nil {
		ac = map[string]any{}
		httpCfg["accessControl"] = ac
	}
	repos, _ := ac["repositories"].(map[string]any)
	if repos == nil {
		repos = map[string]any{}
		ac["repositories"] = repos
	}
	slices.SortFunc(apps, func(a, b zotApp) int { return strings.Compare(a.Name, b.Name) })
	for _, a := range apps {
		pattern := a.Name + "/**"
		if _, ok := repos[pattern]; ok {
			continue
		}
		pushers := []string{a.Name + ":push"}
		if a.Repo != "" {
			pushers = append(pushers, "github:"+a.Repo)
		}
		repos[pattern] = map[string]any{"policies": []any{
			map[string]any{"users": []string{a.Name + ":pull"}, "actions": []string{"read"}},
			map[string]any{"users": pushers, "actions": []string{"read", "create", "update"}},
		}}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func (r *ZotAccess) writeConfig(ctx context.Context, rendered []byte) error {
	var cm corev1.ConfigMap
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: zotConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: zotConfigMap, Namespace: r.Namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "maxcloud-controller"},
				Annotations: map[string]string{
					"maxcloud.gronblom.dev/about": "written by the maxcloud controller from zot-base and the app registry; edit zot-base",
				},
			},
			Data: map[string]string{zotConfigKey: string(rendered)},
		}
		return r.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	if cm.Data[zotConfigKey] == string(rendered) {
		return nil
	}
	cm.Data = map[string]string{zotConfigKey: string(rendered)}
	if err := r.Update(ctx, &cm); err != nil {
		return err
	}
	log.FromContext(ctx).Info("registry configuration updated")
	return nil
}

// restartOnBaseChange stamps the base's hash on zot's pod template: a new
// base rolls out a new pod, which reads the new configuration at start.
func (r *ZotAccess) restartOnBaseChange(ctx context.Context, h string) error {
	var d appsv1.Deployment
	// Uncached: a cached Get would watch every Deployment in the cluster.
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: zotDeployment}, &d); err != nil {
		return client.IgnoreNotFound(err)
	}
	if d.Spec.Template.Annotations[zotBaseHashAnnotation] == h {
		return nil
	}
	patch := client.MergeFrom(d.DeepCopy())
	if d.Spec.Template.Annotations == nil {
		d.Spec.Template.Annotations = map[string]string{}
	}
	d.Spec.Template.Annotations[zotBaseHashAnnotation] = h
	if err := r.Patch(ctx, &d, patch); err != nil {
		return err
	}
	log.FromContext(ctx).Info("registry base configuration changed: restarting zot", "hash", h)
	return nil
}

func (r *ZotAccess) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	entries := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == RegistryNamespace && o.GetLabels()[appLabel] == "true"
	})
	toZot := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{zotRequest}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("zotaccess").
		Watches(u, toZot, builder.WithPredicates(entries)).
		Complete(r)
}
