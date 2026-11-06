package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const pem = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func newClient(t *testing.T, objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func caMap(data string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "maxcloud-system", Name: "kube-root-ca.crt"}, Data: map[string]string{"ca.crt": data}}
}

func get(s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestDocument(t *testing.T) {
	c := newClient(t, caMap(pem))
	s := &Server{Reader: c, Namespace: "maxcloud-system", Document: Document{
		APIServer:     "https://k8s.example:6443",
		OIDC:          &OIDC{Issuer: "https://id.example", ClientID: "mcloud"},
		ImageRegistry: "registry.example",
		Mcloud:        &Mcloud{MinVersion: "0.9.0"},
	}}
	w := get(s, Path)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "max-age=300" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiServer":                "https://k8s.example:6443",
		"certificateAuthorityData": base64.StdEncoding.EncodeToString([]byte(pem)),
		"oidc":                     map[string]any{"issuer": "https://id.example", "clientID": "mcloud"},
		"imageRegistry":            "registry.example",
		"mcloud":                   map[string]any{"minVersion": "0.9.0"},
	}
	got, _ := json.Marshal(doc)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("document = %s\nwant %s", got, exp)
	}

	// The CA is read again only after CAMaxAge, and a failed read serves the
	// last one.
	c.Delete(context.Background(), caMap(""))
	if w := get(s, Path); w.Code != 200 {
		t.Errorf("cached CA: %d", w.Code)
	}
	s.CAMaxAge = 1 // expired
	if w := get(s, Path); w.Code != 200 {
		t.Errorf("stale CA after a failed read: %d", w.Code)
	}

	if w := get(s, "/other"); w.Code != 404 {
		t.Errorf("/other: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, Path, nil))
	if w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}
}

func TestDocumentOmitsUnset(t *testing.T) {
	s := &Server{Reader: newClient(t, caMap(pem)), Namespace: "maxcloud-system", Document: Document{APIServer: "https://k8s.example:6443"}}
	var doc map[string]any
	json.Unmarshal(get(s, Path).Body.Bytes(), &doc)
	for _, k := range []string{"oidc", "mcloud", "imageRegistry"} {
		if _, ok := doc[k]; ok {
			t.Errorf("%s set: %v", k, doc)
		}
	}
}

func TestNoCA(t *testing.T) {
	s := &Server{Reader: newClient(t), Namespace: "maxcloud-system", Document: Document{APIServer: "https://k8s.example:6443"}}
	if w := get(s, Path); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without a CA: %d", w.Code)
	}
	if s.NeedLeaderElection() {
		t.Error("served only by the leader")
	}
}
