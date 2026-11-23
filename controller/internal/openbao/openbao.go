// Package openbao is the controller's small client for OpenBao: it logs in
// with its ServiceAccount token (Kubernetes auth) and reads and writes KV
// version 2 secrets. Plain net/http; only what the controller needs.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultTokenFile is where the kubelet puts the pod's ServiceAccount
// token (renewed while the pod runs, so it's read again at each login).
const DefaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

var (
	// ErrNotFound: no such secret, or its current version is deleted.
	ErrNotFound = errors.New("not found")
	// ErrCASMismatch: a check-and-set write found another version than
	// the one it named (someone wrote in between, or the secret exists
	// when it was to be created).
	ErrCASMismatch = errors.New("check-and-set: the secret's version has changed")
	// ErrForbidden: OpenBao refused even after a fresh login (the
	// controller's policy doesn't allow it).
	ErrForbidden = errors.New("permission denied")
)

// Client talks to one OpenBao server. Set the fields, then use it from any
// goroutine.
type Client struct {
	// Addr is the server, e.g. https://openbao.openbao.svc:8200.
	Addr string
	// HTTP reaches it (New sets one that trusts its CA).
	HTTP *http.Client
	// AuthMount is the Kubernetes auth method's mount (kubernetes), Role
	// the role to log in as (controller).
	AuthMount, Role string
	// KVMount is the KV v2 engine's mount (apps; the platform's own secrets are in platform, out of the controller's reach).
	KVMount string
	// TokenFile is the ServiceAccount token to log in with (default
	// DefaultTokenFile).
	TokenFile string

	mu      sync.Mutex
	token   string
	renewAt time.Time
	now     func() time.Time
}

// New is a client for addr that trusts the CA certificates in caFile (the
// system's if caFile is empty), with the default mounts and role.
func New(addr, caFile string) (*Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no PEM certificates", caFile)
		}
		tlsConfig.RootCAs = pool
	}
	return &Client{
		Addr: strings.TrimRight(addr, "/"),
		HTTP: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second},
		},
		AuthMount: "kubernetes", Role: "controller", KVMount: "apps",
	}, nil
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// apiError is OpenBao's error answer.
type apiError struct {
	status int
	errors []string
}

func (e *apiError) Error() string {
	msg := strings.Join(e.errors, "; ")
	if msg == "" {
		msg = http.StatusText(e.status)
	}
	return fmt.Sprintf("openbao: %d: %s", e.status, msg)
}

// Is maps OpenBao's answers onto this package's errors.
func (e *apiError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.status == http.StatusNotFound
	case ErrForbidden:
		return e.status == http.StatusForbidden
	case ErrCASMismatch:
		return e.status == http.StatusBadRequest && strings.Contains(strings.Join(e.errors, " "), "check-and-set")
	}
	return false
}

// login gets a new token with the ServiceAccount's; c.mu is held.
func (c *Client) login(ctx context.Context) error {
	file := c.TokenFile
	if file == "" {
		file = DefaultTokenFile
	}
	jwt, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("openbao login: %w", err)
	}
	body := map[string]string{"role": c.Role, "jwt": strings.TrimSpace(string(jwt))}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if _, err := c.send(ctx, http.MethodPost, "/v1/auth/"+c.AuthMount+"/login", "", body, &out); err != nil {
		return fmt.Errorf("openbao login as %s: %w", c.Role, err)
	}
	if out.Auth.ClientToken == "" {
		return errors.New("openbao login: no token in the answer")
	}
	c.token = out.Auth.ClientToken
	// A fresh login once two thirds of the lease are gone, so a token is
	// never used near its end. No lease (a root-like token): an hour.
	lease := time.Duration(out.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = time.Hour
	}
	c.renewAt = c.clock().Add(lease * 2 / 3)
	return nil
}

// currentToken logs in if there's no token or it's due for renewal.
func (c *Client) currentToken(ctx context.Context, fresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fresh || c.token == "" || !c.clock().Before(c.renewAt) {
		if err := c.login(ctx); err != nil {
			return "", err
		}
	}
	return c.token, nil
}

// do sends an authenticated request. A 403 gets one more try with a fresh
// login: the token may have been revoked, or expired early.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	token, err := c.currentToken(ctx, false)
	if err != nil {
		return 0, err
	}
	status, err := c.send(ctx, method, path, token, in, out)
	if status != http.StatusForbidden {
		return status, err
	}
	if token, err = c.currentToken(ctx, true); err != nil {
		return 0, err
	}
	return c.send(ctx, method, path, token, in, out)
}

// send is one request; out is decoded from a 2xx answer with a body.
func (c *Client) send(ctx context.Context, method, path, token string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Addr+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Errors []string `json:"errors"`
		}
		_ = json.Unmarshal(b, &e)
		return resp.StatusCode, &apiError{status: resp.StatusCode, errors: e.Errors}
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("openbao %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// Secret is a KV v2 secret's current version.
type Secret struct {
	Data    map[string]string
	Version int
	// Created is when this version was written.
	Created time.Time
}

type versionMeta struct {
	Version      int       `json:"version"`
	CreatedTime  time.Time `json:"created_time"`
	DeletionTime string    `json:"deletion_time"`
	Destroyed    bool      `json:"destroyed"`
}

func (c *Client) dataPath(path string) string { return "/v1/" + c.KVMount + "/data/" + path }
func (c *Client) metaPath(path string) string { return "/v1/" + c.KVMount + "/metadata/" + path }

// Read returns the secret at path (under the KV mount). ErrNotFound if
// there's none; if its current version is deleted, the error is
// ErrNotFound and the returned Secret still carries that Version, for a
// check-and-set write.
func (c *Client) Read(ctx context.Context, path string) (*Secret, error) {
	var out struct {
		Data struct {
			Data     map[string]any `json:"data"`
			Metadata versionMeta    `json:"metadata"`
		} `json:"data"`
	}
	_, err := c.do(ctx, http.MethodGet, c.dataPath(path), nil, &out)
	if errors.Is(err, ErrNotFound) {
		// A deleted current version answers 404, with its metadata.
		m, merr := c.Metadata(ctx, path)
		if merr != nil {
			return nil, err
		}
		return &Secret{Version: m.CurrentVersion}, err
	}
	if err != nil {
		return nil, err
	}
	s := &Secret{Data: map[string]string{}, Version: out.Data.Metadata.Version, Created: out.Data.Metadata.CreatedTime}
	for k, v := range out.Data.Data {
		str, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			str = string(b)
		}
		s.Data[k] = str
	}
	return s, nil
}

// Write makes data the secret's next version. cas >= 0 is a check-and-set:
// the write happens only if the current version is cas (0: only if there's
// none yet), else ErrCASMismatch. cas < 0 writes whatever is there.
// Returns the new version.
func (c *Client) Write(ctx context.Context, path string, data map[string]string, cas int) (int, error) {
	if data == nil {
		data = map[string]string{}
	}
	in := map[string]any{"data": data}
	if cas >= 0 {
		in["options"] = map[string]int{"cas": cas}
	}
	var out struct {
		Data versionMeta `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodPost, c.dataPath(path), in, &out); err != nil {
		return 0, err
	}
	return out.Data.Version, nil
}

// Metadata is a secret's KV v2 metadata.
type Metadata struct {
	CurrentVersion int       `json:"current_version"`
	CreatedTime    time.Time `json:"created_time"`
	UpdatedTime    time.Time `json:"updated_time"`
}

// Metadata reads path's metadata; ErrNotFound if the secret has never been
// written (or its metadata was deleted).
func (c *Client) Metadata(ctx context.Context, path string) (*Metadata, error) {
	var out struct {
		Data Metadata `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, c.metaPath(path), nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Exists: path has been written and its metadata not deleted (its current
// version may be deleted).
func (c *Client) Exists(ctx context.Context, path string) (bool, error) {
	_, err := c.Metadata(ctx, path)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// DeleteMetadata deletes the secret at path with every version. Deleting
// one that isn't there is no error.
func (c *Client) DeleteMetadata(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, c.metaPath(path), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
