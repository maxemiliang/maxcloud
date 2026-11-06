package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const zotBase = `{
  "storage": {"rootDirectory": "/var/lib/zot"},
  "http": {
    "port": "5000",
    "accessControl": {
      "repositories": {
        "**": {"policies": [], "anonymousPolicy": []},
        "special/**": {"policies": [{"users": ["someone"], "actions": ["read"]}]}
      },
      "adminPolicy": {"groups": ["authelia:admins"], "actions": ["read", "create", "update", "delete"]}
    }
  }
}`

func registryEntry(name string, values map[string]any) *unstructured.Unstructured {
	u := entry(name, map[string]string{appLabel: "true"})
	u.Object["spec"] = map[string]any{"defaultValues": values}
	return u
}

func newZotEnv(t *testing.T, objs ...client.Object) (client.Client, *ZotAccess) {
	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	objs = append(objs,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "registry", Name: zotBaseConfigMap}, Data: map[string]string{zotConfigKey: zotBase}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "registry", Name: zotDeployment}},
	)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return c, &ZotAccess{Client: c, APIReader: c, Namespace: "registry"}
}

type zotRule struct {
	Policies []struct {
		Users   []string `json:"users"`
		Actions []string `json:"actions"`
	} `json:"policies"`
}

func renderedRules(t *testing.T, c client.Client) map[string]zotRule {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "registry", Name: zotConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		HTTP struct {
			AccessControl struct {
				Repositories map[string]zotRule `json:"repositories"`
				AdminPolicy  map[string]any     `json:"adminPolicy"`
			} `json:"accessControl"`
		} `json:"http"`
	}
	if err := json.Unmarshal([]byte(cm.Data[zotConfigKey]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.AccessControl.AdminPolicy == nil {
		t.Error("the base's adminPolicy is gone")
	}
	return cfg.HTTP.AccessControl.Repositories
}

func baseHash(t *testing.T, c client.Client) string {
	var d appsv1.Deployment
	c.Get(context.Background(), types.NamespacedName{Namespace: "registry", Name: zotDeployment}, &d)
	return d.Spec.Template.Annotations[zotBaseHashAnnotation]
}

func TestZotAccessRendersOneRulePerApp(t *testing.T) {
	c, r := newZotEnv(t,
		registryEntry("shop", map[string]any{"name": "shop", "repo": "ann/shop"}),
		registryEntry("quick", map[string]any{"name": "quick", "deploy": "direct", "repo": "ann/old"}),
		registryEntry("special", map[string]any{"name": "special", "repo": "ann/special"}),
		entry("not-an-app", map[string]string{}),
	)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	rules := renderedRules(t, c)
	if len(rules) != 4 {
		t.Errorf("rules for %v", rules)
	}
	shop := rules["shop/**"].Policies
	if len(shop) != 2 || shop[0].Users[0] != "shop:pull" || len(shop[0].Actions) != 1 || shop[0].Actions[0] != "read" {
		t.Errorf("shop's pull rule: %+v", shop)
	}
	if len(shop[1].Users) != 2 || shop[1].Users[0] != "shop:push" || shop[1].Users[1] != "github:ann/shop" {
		t.Errorf("shop's push rule: %+v", shop[1])
	}
	if q := rules["quick/**"].Policies; len(q) != 2 || len(q[1].Users) != 1 {
		t.Errorf("a direct app's repo may push: %+v", q)
	}
	if s := rules["special/**"].Policies; len(s) != 1 || s[0].Users[0] != "someone" {
		t.Errorf("the base's own rule was replaced: %+v", s)
	}
	if _, ok := rules["**"]; !ok {
		t.Error("the base's default rule is gone")
	}

	first := baseHash(t, c)
	if first == "" {
		t.Fatal("zot's pod template has no base hash")
	}
	// Same base: no restart.
	r.Reconcile(context.Background(), ctrl.Request{})
	if baseHash(t, c) != first {
		t.Error("restarted zot without a base change")
	}
	// A new base: restart.
	var base corev1.ConfigMap
	c.Get(context.Background(), types.NamespacedName{Namespace: "registry", Name: zotBaseConfigMap}, &base)
	base.Data[zotConfigKey] = zotBase + "\n"
	c.Update(context.Background(), &base)
	r.Reconcile(context.Background(), ctrl.Request{})
	if baseHash(t, c) == first {
		t.Error("the base changed and zot wasn't restarted")
	}
}

func TestZotAccessBrokenBase(t *testing.T) {
	c, r := newZotEnv(t)
	var base corev1.ConfigMap
	c.Get(context.Background(), types.NamespacedName{Namespace: "registry", Name: zotBaseConfigMap}, &base)
	base.Data[zotConfigKey] = "{not json"
	c.Update(context.Background(), &base)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err == nil {
		t.Error("want an error for a base that isn't JSON")
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "registry", Name: zotConfigMap}, &cm); err == nil {
		t.Error("wrote a configuration from a broken base")
	}
}

// TestZotAccessWriteSample writes a rendered config for checking with
// `zot verify` (ZOT_RENDER_SAMPLE=<dir>).
func TestZotAccessWriteSample(t *testing.T) {
	dir := os.Getenv("ZOT_RENDER_SAMPLE")
	base := os.Getenv("ZOT_RENDER_BASE")
	if dir == "" || base == "" {
		t.Skip("ZOT_RENDER_SAMPLE and ZOT_RENDER_BASE not set")
	}
	b, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderZotConfig(string(b), []zotApp{{Name: "run-demo"}, {Name: "fn-demo", Repo: "maxemiliang/fn-demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}
