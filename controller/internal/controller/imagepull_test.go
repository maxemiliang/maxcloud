package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type pullEnv struct {
	t      *testing.T
	c      client.Client
	r      *ImagePull
	now    time.Time
	tokens int
	asked  []*authenticationv1.TokenRequest
}

func newPullEnv(t *testing.T, objs ...client.Object) *pullEnv {
	e := &pullEnv{t: t, now: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)}
	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	e.c = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		SubResourceCreate: func(ctx context.Context, c client.Client, sub string, obj client.Object, req client.Object, opts ...client.SubResourceCreateOption) error {
			tr := req.(*authenticationv1.TokenRequest)
			e.tokens++
			e.asked = append(e.asked, tr.DeepCopy())
			tr.Status.Token = "token-" + string(rune('0'+e.tokens))
			// The API server grants less than asked for.
			tr.Status.ExpirationTimestamp = metav1.NewTime(e.now.Add(24 * time.Hour))
			return nil
		},
	})
	e.r = &ImagePull{Client: e.c, APIReader: e.c, Host: "registry.example", Lifetime: 7 * 24 * time.Hour, Now: func() time.Time { return e.now }}
	return e
}

func (e *pullEnv) reconcile(ns string) ctrl.Result {
	e.t.Helper()
	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ns}})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *pullEnv) secret(ns string) *corev1.Secret {
	e.t.Helper()
	var s corev1.Secret
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: PullSecretName}, &s); err != nil {
		e.t.Fatal(err)
	}
	return &s
}

func defaultSA(ns string, pullSecrets ...string) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default"}}
	for _, s := range pullSecrets {
		sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: s})
	}
	return sa
}

func TestImagePullWritesSecretAndUsesIt(t *testing.T) {
	e := newPullEnv(t, tenantNS("shop"), defaultSA("shop", "ghcr-pull"))
	res := e.reconcile("shop")

	var sa corev1.ServiceAccount
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: PullServiceAccount}, &sa); err != nil {
		t.Fatalf("pull ServiceAccount: %v", err)
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Error("the pull ServiceAccount mounts its token into pods")
	}
	if len(e.asked) != 1 || e.asked[0].Spec.Audiences[0] != "registry.example" || *e.asked[0].Spec.ExpirationSeconds != 7*24*3600 {
		t.Fatalf("token requests = %+v", e.asked)
	}

	s := e.secret("shop")
	if s.Type != corev1.SecretTypeDockerConfigJson {
		t.Errorf("type = %s", s.Type)
	}
	var cfg struct {
		Auths map[string]struct{ Username, Password, Auth string } `json:"auths"`
	}
	if err := json.Unmarshal(s.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatal(err)
	}
	a := cfg.Auths["registry.example"]
	if a.Password != "token-1" || a.Username == "" || a.Auth != base64.StdEncoding.EncodeToString([]byte(a.Username+":token-1")) {
		t.Errorf("auths = %+v", cfg.Auths)
	}

	var def corev1.ServiceAccount
	e.c.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: "default"}, &def)
	if len(def.ImagePullSecrets) != 2 || def.ImagePullSecrets[0].Name != "ghcr-pull" || def.ImagePullSecrets[1].Name != PullSecretName {
		t.Errorf("default imagePullSecrets = %v", def.ImagePullSecrets)
	}
	// Granted 24h: renew at 12h; check back hourly meanwhile.
	if res.RequeueAfter != time.Hour {
		t.Errorf("requeue after %v", res.RequeueAfter)
	}

	// Before half-life: nothing new. After: a new token.
	e.now = e.now.Add(11 * time.Hour)
	e.reconcile("shop")
	if e.tokens != 1 {
		t.Errorf("renewed early: %d tokens", e.tokens)
	}
	if res := e.reconcile("shop"); res.RequeueAfter != time.Hour {
		t.Errorf("requeue after %v near renewal", res.RequeueAfter)
	}
	e.now = e.now.Add(90 * time.Minute)
	e.reconcile("shop")
	if e.tokens != 2 || e.secret("shop").Annotations[pullExpiresAnnotation] != e.now.Add(24*time.Hour).Format(time.RFC3339) {
		t.Errorf("not renewed: %d tokens, %v", e.tokens, e.secret("shop").Annotations)
	}
	e.c.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: "default"}, &def)
	if len(def.ImagePullSecrets) != 2 {
		t.Errorf("default imagePullSecrets = %v", def.ImagePullSecrets)
	}
}

func TestImagePullOtherCases(t *testing.T) {
	t.Run("not an app namespace", func(t *testing.T) {
		e := newPullEnv(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}, defaultSA("kube-system"))
		e.reconcile("kube-system")
		if e.tokens != 0 {
			t.Error("made a token outside the apps")
		}
	})
	t.Run("host changed", func(t *testing.T) {
		e := newPullEnv(t, tenantNS("shop"), defaultSA("shop"))
		e.reconcile("shop")
		e.r.Host = "registry.other"
		e.reconcile("shop")
		if e.tokens != 2 || !e.r.forHost(e.secret("shop")) {
			t.Errorf("tokens %d; secret for the new host: %v", e.tokens, e.r.forHost(e.secret("shop")))
		}
	})
	t.Run("default ServiceAccount not made yet", func(t *testing.T) {
		e := newPullEnv(t, tenantNS("shop"))
		if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "shop"}}); err == nil {
			t.Error("want an error (and a retry) while the default ServiceAccount is missing")
		}
	})
}
