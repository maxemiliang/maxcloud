package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/maxemiliang/maxcloud/controller/internal/caller"
	"github.com/maxemiliang/maxcloud/controller/internal/envname"
	"github.com/maxemiliang/maxcloud/controller/internal/openbao"
)

// An app's secrets (mcloud secrets) live in OpenBao, at
// apps/<namespace>/app-secrets, and External Secrets Operator copies them
// into the Secret app-secrets in the namespace (the ExternalSecret
// app-secrets, which the app registry's ResourceSet makes). People never
// reach OpenBao: these routes write there as the controller, for whoever
// RBAC lets update that Secret.
//
//	GET    /api/v1/apps/{app}/secrets         names, the version and when it was written; never values
//	PUT    /api/v1/apps/{app}/secrets/{name}  {"value": "..."}
//	DELETE /api/v1/apps/{app}/secrets/{name}
//
// A change answers 200 once the Secret has it (the ExternalSecret synced
// after the write), or 202 if that took longer than SyncTimeout: OpenBao
// has the change, and the Secret gets it on the next sync.

// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch;patch

// FeatureSecrets is the secrets routes' feature.
const FeatureSecrets = "secrets"

const (
	// AppSecretsName is the Secret, and the ExternalSecret that makes it.
	AppSecretsName = "app-secrets"
	// ForceSyncAnnotation on an ExternalSecret makes ESO sync it now.
	ForceSyncAnnotation = "force-sync"
	maxSecretValue      = 512 << 10 // a Secret holds 1 MiB in all
)

var externalSecretGVK = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ExternalSecret"}

// SecretStore is OpenBao's KV, as the controller (openbao.Client).
type SecretStore interface {
	Read(ctx context.Context, path string) (*openbao.Secret, error)
	Write(ctx context.Context, path string, data map[string]string, cas int) (int, error)
}

// KVPath is where an app's (namespace's) secrets are, under the KV mount.
func KVPath(namespace string) string { return namespace + "/" + AppSecretsName }

// SecretsResponse lists an app's secrets. Version and Updated are the KV
// secret's (all of an app's secrets are one KV secret); zero if it has
// none yet.
type SecretsResponse struct {
	App     string       `json:"app"`
	Version int          `json:"version"`
	Updated *time.Time   `json:"updated,omitempty"`
	Secrets []SecretName `json:"secrets"`
}

// SecretName is one secret, without its value.
type SecretName struct {
	Name string `json:"name"`
}

// SecretChange is the answer to a PUT or DELETE.
type SecretChange struct {
	App     string `json:"app"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Synced: the Secret app-secrets has the change. If not, Message says
	// why it isn't there yet.
	Synced  bool   `json:"synced"`
	Message string `json:"message,omitempty"`
}

func secretPermission(verb string) caller.Permission {
	return caller.Permission{Verb: verb, Resource: "secrets", Name: AppSecretsName}
}

func (h *Handler) listSecrets(w http.ResponseWriter, r *http.Request) {
	app, err := h.authorize(r, secretPermission("get"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	s, err := h.Secrets.Read(r.Context(), KVPath(app))
	if err != nil && !errors.Is(err, openbao.ErrNotFound) {
		h.fail(w, r, err)
		return
	}
	out := SecretsResponse{App: app, Secrets: []SecretName{}}
	if err == nil {
		out.Version = s.Version
		if !s.Created.IsZero() {
			t := s.Created.UTC()
			out.Updated = &t
		}
		for name := range s.Data {
			out.Secrets = append(out.Secrets, SecretName{Name: name})
		}
		sort.Slice(out.Secrets, func(i, j int) bool { return out.Secrets[i].Name < out.Secrets[j].Name })
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) putSecret(w http.ResponseWriter, r *http.Request) {
	app, err := h.authorize(r, secretPermission("update"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	if err := envname.Check(name); err != nil {
		h.fail(w, r, errorf(http.StatusBadRequest, "secret %v", err))
		return
	}
	var in struct {
		Value *string `json:"value"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSecretValue+1024))
	if err := dec.Decode(&in); err != nil || in.Value == nil {
		h.fail(w, r, errorf(http.StatusBadRequest, `the body must be {"value": "..."}`))
		return
	}
	if *in.Value == "" {
		h.fail(w, r, errorf(http.StatusBadRequest, "empty value"))
		return
	}
	if len(*in.Value) > maxSecretValue {
		h.fail(w, r, errorf(http.StatusRequestEntityTooLarge, "the value is over %d KiB", maxSecretValue>>10))
		return
	}
	value := *in.Value
	h.changeSecret(w, r, app, name, func(data map[string]string) error {
		data[name] = value
		return nil
	})
}

func (h *Handler) deleteSecret(w http.ResponseWriter, r *http.Request) {
	app, err := h.authorize(r, secretPermission("update"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	h.changeSecret(w, r, app, name, func(data map[string]string) error {
		if _, ok := data[name]; !ok {
			return errorf(http.StatusNotFound, "no secret %s", name)
		}
		delete(data, name)
		return nil
	})
}

// changeSecret applies edit to the app's secrets with a check-and-set
// write (again from a fresh read if someone wrote in between), then has
// ESO sync them into the Secret.
func (h *Handler) changeSecret(w http.ResponseWriter, r *http.Request, app, name string, edit func(map[string]string) error) {
	ctx := r.Context()
	path := KVPath(app)
	var version int
	for attempt := 0; ; attempt++ {
		cur, err := h.Secrets.Read(ctx, path)
		data, cas := map[string]string{}, 0
		switch {
		case err == nil:
			data, cas = cur.Data, cur.Version
		case errors.Is(err, openbao.ErrNotFound):
			if cur != nil {
				cas = cur.Version // its current version is deleted
			}
		default:
			h.fail(w, r, err)
			return
		}
		if err := edit(data); err != nil {
			h.fail(w, r, err)
			return
		}
		version, err = h.Secrets.Write(ctx, path, data, cas)
		if err == nil {
			break
		}
		if !errors.Is(err, openbao.ErrCASMismatch) || attempt == 2 {
			h.fail(w, r, err)
			return
		}
	}
	out := SecretChange{App: app, Name: name, Version: version}
	out.Synced, out.Message = h.sync(ctx, app)
	status := http.StatusOK
	if !out.Synced {
		status = http.StatusAccepted
	}
	writeJSON(w, status, out)
}

// sync has ESO sync the namespace's ExternalSecret app-secrets now
// (the force-sync annotation, set to the time), and waits up to
// SyncTimeout for it to have. ESO sets status.syncedResourceVersion from
// the ExternalSecret's generation, labels and annotations after each
// successful sync only, so once it differs from what it was right after
// the annotation changed, and Ready is True, a sync has read OpenBao after
// the write. If it isn't synced, the message says why.
func (h *Handler) sync(ctx context.Context, ns string) (bool, string) {
	const pending = "OpenBao has the change; the Secret " + AppSecretsName + " gets it on the ExternalSecret's next sync"
	timeout := h.SyncTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	poll := h.SyncPoll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	key := types.NamespacedName{Namespace: ns, Name: AppSecretsName}
	es := &unstructured.Unstructured{}
	es.SetGroupVersionKind(externalSecretGVK)
	if err := h.Reader.Get(ctx, key, es); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, fmt.Sprintf("%s: there's no ExternalSecret %s in %s yet", pending, AppSecretsName, ns)
		}
		return false, fmt.Sprintf("%s (reading the ExternalSecret: %v)", pending, err)
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, ForceSyncAnnotation, time.Now().UTC().Format(time.RFC3339Nano))
	if err := h.Writer.Patch(ctx, es, client.RawPatch(types.MergePatchType, []byte(patch))); err != nil {
		return false, fmt.Sprintf("%s (asking for a sync: %v)", pending, err)
	}
	before, _, _ := unstructured.NestedString(es.Object, "status", "syncedResourceVersion")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	why := ""
	for {
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("%s: it hasn't synced within %s", pending, timeout)
			if why != "" {
				msg += " (" + why + ")"
			}
			return false, msg
		case <-time.After(poll):
		}
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(externalSecretGVK)
		if err := h.Reader.Get(ctx, key, cur); err != nil {
			if ctx.Err() == nil {
				why = err.Error()
			}
			continue
		}
		synced, _, _ := unstructured.NestedString(cur.Object, "status", "syncedResourceVersion")
		ready, reason := readyCondition(cur)
		if synced != before && ready {
			return true, ""
		}
		if !ready && reason != "" {
			why = reason
		}
	}
}

// readyCondition is an ExternalSecret's Ready condition: whether it's
// True, and else its reason and message.
func readyCondition(u *unstructured.Unstructured) (bool, string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] != "Ready" {
			continue
		}
		if m["status"] == "True" {
			return true, ""
		}
		reason, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		if msg != "" {
			return false, reason + ": " + msg
		}
		return false, reason
	}
	return false, ""
}
