// Package discovery serves /.well-known/maxcloud.json: what a new machine
// needs to log in to the cluster (its API server, CA and OIDC settings)
// without an admin kubeconfig. It's public and holds nothing secret.
package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Path is where the document is served.
const Path = "/.well-known/maxcloud.json"

// caConfigMap is the ConfigMap Kubernetes puts in every namespace with the
// API server's CA.
const caConfigMap = "kube-root-ca.crt"

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get,resourceNames=kube-root-ca.crt

// Document is the JSON served.
type Document struct {
	APIServer                string  `json:"apiServer"`
	CertificateAuthorityData string  `json:"certificateAuthorityData"`
	OIDC                     *OIDC   `json:"oidc,omitempty"`
	ImageRegistry            string  `json:"imageRegistry,omitempty"`
	Mcloud                   *Mcloud `json:"mcloud,omitempty"`
}

type OIDC struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientID"`
}

type Mcloud struct {
	MinVersion string `json:"minVersion"`
}

// Server serves the document. It runs on every replica, not only the
// leader.
type Server struct {
	// Addr to listen on, e.g. :8082.
	Addr string
	// Reader reads the CA's ConfigMap (uncached: the manager's APIReader).
	Reader client.Reader
	// Namespace the controller runs in, where kube-root-ca.crt is read.
	Namespace string
	// Document is served with certificateAuthorityData filled in.
	Document Document
	// CAMaxAge is how long the CA read is reused (default 5 minutes).
	CAMaxAge time.Duration

	mu     sync.Mutex
	ca     string
	caRead time.Time
}

// NeedLeaderElection: every replica serves the document.
func (s *Server) NeedLeaderElection() bool { return false }

// Start serves until ctx is done.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.FromContext(ctx).WithName("discovery").Info("serving the discovery document", "addr", s.Addr, "path", Path)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != Path {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ca, err := s.caData(r.Context())
	if err != nil {
		log.FromContext(r.Context()).WithName("discovery").Error(err, "reading the cluster's CA")
		http.Error(w, "the cluster's CA can't be read right now", http.StatusServiceUnavailable)
		return
	}
	doc := s.Document
	doc.CertificateAuthorityData = ca
	body, err := json.Marshal(doc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=300")
	if r.Method == http.MethodHead {
		return
	}
	w.Write(append(body, '\n'))
}

// caData is ca.crt from kube-root-ca.crt, base64, as in a kubeconfig. A
// read is reused for CAMaxAge; if a new read fails, the last one is served.
func (s *Server) caData(ctx context.Context) (string, error) {
	maxAge := s.CAMaxAge
	if maxAge == 0 {
		maxAge = 5 * time.Minute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ca != "" && time.Since(s.caRead) < maxAge {
		return s.ca, nil
	}
	var cm corev1.ConfigMap
	err := s.Reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: caConfigMap}, &cm)
	if err == nil && cm.Data["ca.crt"] == "" {
		err = errors.New(caConfigMap + " has no ca.crt")
	}
	if err != nil {
		if s.ca != "" {
			return s.ca, nil
		}
		return "", err
	}
	s.ca = base64.StdEncoding.EncodeToString([]byte(cm.Data["ca.crt"]))
	s.caRead = time.Now()
	return s.ca, nil
}
