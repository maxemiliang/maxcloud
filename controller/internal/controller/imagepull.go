package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// PullServiceAccount is the identity an app pulls its images from the
	// maxcloud registry as. Nothing runs as it: it exists for its tokens.
	PullServiceAccount = "maxcloud-image-pull"
	// PullSecretName is the app's pull Secret (kubernetes.io/dockerconfigjson),
	// which its default ServiceAccount lists in imagePullSecrets.
	PullSecretName = "maxcloud-registry"
	// pullExpiresAnnotation on the Secret: when its token runs out (RFC 3339).
	pullExpiresAnnotation = "maxcloud.gronblom.dev/expires"
	// pullUsername is what clients send as the Basic username; the registry
	// ignores it (the token says who it is), but it can't be empty.
	pullUsername = "maxcloud"
)

// ImagePull gives every app namespace credentials for the maxcloud registry:
// a token of the ServiceAccount maxcloud-image-pull, with the registry's
// host as its audience, in the pull Secret maxcloud-registry, which the
// namespace's default ServiceAccount uses. The registry trusts the cluster's
// ServiceAccount tokens and lets each namespace read only its own images
// (<host>/<namespace>/...). Tokens are short-lived and renewed at half their
// lifetime.
type ImagePull struct {
	client.Client
	// APIReader reads Secrets straight from the API server (no cache of
	// every Secret in the cluster).
	APIReader client.Reader
	// Host of the registry, e.g. registry.gronblom.dev: the tokens'
	// audience and the dockerconfigjson key.
	Host string
	// Lifetime asked for each token. The API server may shorten it; the
	// renewal follows what it grants.
	Lifetime time.Duration
	// Now is for tests.
	Now func() time.Time
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;patch
// Tokens only for the pull ServiceAccount: create on serviceaccounts/token
// names the ServiceAccount, so resourceNames applies.
// +kubebuilder:rbac:groups="",resources=serviceaccounts/token,verbs=create,resourceNames=maxcloud-image-pull
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update

func (r *ImagePull) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ImagePull) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if ns.Labels[TenantLabel] != "true" || !ns.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	app := ns.Name

	if err := r.ensureServiceAccount(ctx, app); err != nil {
		return ctrl.Result{}, err
	}

	var secret corev1.Secret
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: app, Name: PullSecretName}, &secret)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	expires, renewAt := r.schedule(&secret)
	if !exists || !r.forHost(&secret) || !r.now().Before(renewAt) {
		if expires, err = r.writeSecret(ctx, app, &secret, exists); err != nil {
			return ctrl.Result{}, err
		}
		log.FromContext(ctx).Info("registry pull token renewed", "app", app, "expires", expires)
		_, renewAt = r.schedule(&secret)
	}

	if err := r.useFromDefault(ctx, app); err != nil {
		return ctrl.Result{}, err
	}
	// Secrets aren't watched (that would cache every Secret in the cluster),
	// so come back hourly anyway: a deleted Secret or edited default
	// ServiceAccount is put right within the hour.
	return ctrl.Result{RequeueAfter: min(max(renewAt.Sub(r.now()), time.Minute), time.Hour)}, nil
}

// schedule reads when the Secret's token expires and when to renew it: at
// half its lifetime, so a controller that's down for a while doesn't leave
// apps unable to pull.
func (r *ImagePull) schedule(s *corev1.Secret) (expires, renewAt time.Time) {
	expires, err := time.Parse(time.RFC3339, s.Annotations[pullExpiresAnnotation])
	if err != nil {
		return time.Time{}, time.Time{}
	}
	issued, err := time.Parse(time.RFC3339, s.Annotations[pullExpiresAnnotation+"-issued"])
	if err != nil || !issued.Before(expires) {
		return expires, expires.Add(-r.Lifetime / 2)
	}
	return expires, issued.Add(expires.Sub(issued) / 2)
}

// forHost: the Secret is for the configured registry (it changes if the
// host does).
func (r *ImagePull) forHost(s *corev1.Secret) bool {
	var cfg struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if json.Unmarshal(s.Data[corev1.DockerConfigJsonKey], &cfg) != nil {
		return false
	}
	_, ok := cfg.Auths[r.Host]
	return ok && len(cfg.Auths) == 1
}

func (r *ImagePull) ensureServiceAccount(ctx context.Context, app string) error {
	var sa corev1.ServiceAccount
	err := r.Get(ctx, types.NamespacedName{Namespace: app, Name: PullServiceAccount}, &sa)
	if err == nil || !apierrors.IsNotFound(err) {
		return err
	}
	no := false
	sa = corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: PullServiceAccount, Namespace: app,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "maxcloud-controller"},
			Annotations: map[string]string{
				"maxcloud.gronblom.dev/purpose": "pulls this app's images from the maxcloud registry; nothing runs as it",
			},
		},
		AutomountServiceAccountToken: &no,
	}
	if err := r.Create(ctx, &sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// writeSecret gets a new token and writes it into the pull Secret.
func (r *ImagePull) writeSecret(ctx context.Context, app string, secret *corev1.Secret, exists bool) (time.Time, error) {
	seconds := int64(r.Lifetime / time.Second)
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: PullServiceAccount, Namespace: app}}
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
		Audiences:         []string{r.Host},
		ExpirationSeconds: &seconds,
	}}
	if err := r.SubResource("token").Create(ctx, sa, tr); err != nil {
		return time.Time{}, fmt.Errorf("token for %s/%s: %w", app, PullServiceAccount, err)
	}
	issued := r.now()
	expires := tr.Status.ExpirationTimestamp.Time
	if expires.IsZero() {
		expires = issued.Add(r.Lifetime)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(pullUsername + ":" + tr.Status.Token))
	cfg, err := json.Marshal(map[string]any{"auths": map[string]any{
		r.Host: map[string]string{"username": pullUsername, "password": tr.Status.Token, "auth": auth},
	}})
	if err != nil {
		return time.Time{}, err
	}
	if !exists {
		*secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: PullSecretName, Namespace: app}}
	}
	secret.Type = corev1.SecretTypeDockerConfigJson
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels["app.kubernetes.io/managed-by"] = "maxcloud-controller"
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[pullExpiresAnnotation] = expires.UTC().Format(time.RFC3339)
	secret.Annotations[pullExpiresAnnotation+"-issued"] = issued.UTC().Format(time.RFC3339)
	secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: cfg}
	if exists {
		return expires, r.Update(ctx, secret)
	}
	return expires, r.Create(ctx, secret)
}

// useFromDefault adds the pull Secret to the default ServiceAccount's
// imagePullSecrets (keeping any others), so every pod that doesn't name a
// ServiceAccount, Knative services included, pulls with it.
func (r *ImagePull) useFromDefault(ctx context.Context, app string) error {
	var sa corev1.ServiceAccount
	if err := r.Get(ctx, types.NamespacedName{Namespace: app, Name: "default"}, &sa); err != nil {
		// Kubernetes makes it shortly after the namespace: try again then.
		return err
	}
	if slices.ContainsFunc(sa.ImagePullSecrets, func(s corev1.LocalObjectReference) bool { return s.Name == PullSecretName }) {
		return nil
	}
	patch := client.MergeFrom(sa.DeepCopy())
	sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: PullSecretName})
	return r.Patch(ctx, &sa, patch)
}

func (r *ImagePull) SetupWithManager(mgr ctrl.Manager) error {
	tenant := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[TenantLabel] == "true"
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(tenant)).
		Named("imagepull").
		Complete(r)
}
