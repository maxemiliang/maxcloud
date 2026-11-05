// Package auth logs mcloud (and kubectl, through mcloud credential) in to
// the cluster with the homelab's OpenID Connect provider (Authelia): the
// cluster's API server trusts its ID tokens (UKS cluster authentication).
//
// Two ways to log in: the device code flow (mcloud prints a code, you
// approve it in any browser; works over SSH), and the browser flow with
// PKCE on a fixed loopback port. The ID token, which the API server checks,
// and the refresh token are cached in ~/.cache/mcloud; the ID token is
// refreshed when it expires, and a new login is needed when the refresh
// token does.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Defaults for the homelab: Authelia, and the client the cluster's API
// server accepts tokens for (its audience).
const (
	DefaultIssuer   = "https://auth.lab.gronblom.dev"
	DefaultClientID = "kubernetes"
	// The browser flow's redirect, registered exactly so in Authelia.
	CallbackAddr = "127.0.0.1:18000"
	CallbackPath = "/callback"
)

var scopes = []string{"openid", "profile", "email", "groups", "offline_access"}

// Config says which provider and client to log in with.
type Config struct {
	Issuer   string
	ClientID string
	// CacheDir holds the tokens (default ~/.cache/mcloud).
	CacheDir string
	HTTP     *http.Client
}

func (c Config) withDefaults() Config {
	if c.Issuer == "" {
		c.Issuer = DefaultIssuer
	}
	if c.ClientID == "" {
		c.ClientID = DefaultClientID
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return c
}

// Tokens is what's cached.
type Tokens struct {
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"clientID"`
	IDToken      string    `json:"idToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry"` // the ID token's
	User         string    `json:"user,omitempty"`
	Groups       []string  `json:"groups,omitempty"`
}

// Valid reports whether the ID token is good for at least another minute.
func (t *Tokens) Valid(now time.Time) bool {
	return t != nil && t.IDToken != "" && now.Add(time.Minute).Before(t.Expiry)
}

type discovery struct {
	Issuer                      string `json:"issuer"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

func discover(ctx context.Context, c Config) (*discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", c.Issuer, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s discovery: %s", c.Issuer, resp.Status)
	}
	var d discovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("%s discovery: %w", c.Issuer, err)
	}
	if strings.TrimRight(d.Issuer, "/") != strings.TrimRight(c.Issuer, "/") {
		return nil, fmt.Errorf("%s says its issuer is %q", c.Issuer, d.Issuer)
	}
	return &d, nil
}

func oauthConfig(c Config, d *discovery, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: c.ClientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:       d.AuthorizationEndpoint,
			TokenURL:      d.TokenEndpoint,
			DeviceAuthURL: d.DeviceAuthorizationEndpoint,
			AuthStyle:     oauth2.AuthStyleInParams, // a public client: no secret
		},
		RedirectURL: redirect,
		Scopes:      scopes,
	}
}

// DeviceLogin logs in with the device code flow. prompt is told where to go
// and which code to enter.
func DeviceLogin(ctx context.Context, cfg Config, prompt func(url, code string)) (*Tokens, error) {
	c := cfg.withDefaults()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.HTTP)
	d, err := discover(ctx, c)
	if err != nil {
		return nil, err
	}
	if d.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("%s doesn't offer the device code flow; use the browser flow", c.Issuer)
	}
	oc := oauthConfig(c, d, "")
	da, err := oc.DeviceAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting the device login: %w", err)
	}
	url := da.VerificationURIComplete
	if url == "" {
		url = da.VerificationURI
	}
	prompt(url, da.UserCode)
	tok, err := oc.DeviceAccessToken(ctx, da)
	if err != nil {
		return nil, fmt.Errorf("device login: %w", err)
	}
	return tokens(c, tok, "")
}

// BrowserLogin logs in with the authorization code flow and PKCE: it
// listens on CallbackAddr for the redirect, and open is asked to show the
// login page.
func BrowserLogin(ctx context.Context, cfg Config, open func(url string)) (*Tokens, error) {
	c := cfg.withDefaults()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.HTTP)
	d, err := discover(ctx, c)
	if err != nil {
		return nil, err
	}
	oc := oauthConfig(c, d, "http://"+CallbackAddr+CallbackPath)
	verifier := oauth2.GenerateVerifier()
	state := randomString()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "unexpected state", http.StatusBadRequest)
			return // not ours: keep waiting
		case q.Get("error") != "":
			done <- result{err: fmt.Errorf("login refused: %s %s", q.Get("error"), q.Get("error_description"))}
		default:
			done <- result{code: q.Get("code")}
		}
		fmt.Fprintln(w, "Logged in to the cluster. You can close this tab and go back to the terminal.")
	})
	srv := &http.Server{Addr: CallbackAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := listen(CallbackAddr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s for the login's redirect (is another login running?): %w", CallbackAddr, err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	open(oc.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)))
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}
	tok, err := oc.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("finishing the login: %w", err)
	}
	return tokens(c, tok, "")
}

// Refresh gets a new ID token with the refresh token.
func Refresh(ctx context.Context, cfg Config, t *Tokens) (*Tokens, error) {
	c := cfg.withDefaults()
	if t == nil || t.RefreshToken == "" {
		return nil, errors.New("no refresh token")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.HTTP)
	d, err := discover(ctx, c)
	if err != nil {
		return nil, err
	}
	src := oauthConfig(c, d, "").TokenSource(ctx, &oauth2.Token{RefreshToken: t.RefreshToken, Expiry: time.Unix(1, 0)})
	tok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("refreshing: %w", err)
	}
	return tokens(c, tok, t.RefreshToken)
}

// tokens reads the ID token out of a token response.
func tokens(c Config, tok *oauth2.Token, oldRefresh string) (*Tokens, error) {
	id, _ := tok.Extra("id_token").(string)
	if id == "" {
		return nil, errors.New("the provider returned no ID token (is the openid scope allowed for the client?)")
	}
	claims, err := Claims(id)
	if err != nil {
		return nil, err
	}
	t := &Tokens{
		Issuer:       c.Issuer,
		ClientID:     c.ClientID,
		IDToken:      id,
		RefreshToken: tok.RefreshToken,
		Expiry:       time.Unix(claims.Exp, 0),
		User:         claims.PreferredUsername,
		Groups:       claims.Groups,
	}
	if t.RefreshToken == "" { // not rotated
		t.RefreshToken = oldRefresh
	}
	return t, nil
}

// IDClaims are the ID token's claims mcloud shows. It doesn't check the
// signature: the API server does.
type IDClaims struct {
	Exp               int64    `json:"exp"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

func Claims(idToken string) (*IDClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("the ID token isn't a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("ID token: %w", err)
	}
	var c IDClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("ID token: %w", err)
	}
	if c.Exp == 0 {
		return nil, errors.New("ID token without exp")
	}
	return &c, nil
}

// --- the cache --------------------------------------------------------------

func cachePath(cfg Config) (string, error) {
	c := cfg.withDefaults()
	dir := c.CacheDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "mcloud")
	}
	name := strings.NewReplacer("https://", "", "http://", "", "/", "_", ":", "_").Replace(c.Issuer)
	return filepath.Join(dir, "oidc-"+name+"-"+c.ClientID+".json"), nil
}

// Load returns the cached tokens, nil if there are none.
func Load(cfg Config) (*Tokens, error) {
	p, err := cachePath(cfg)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &t, nil
}

// Save caches t, readable by the user only.
func Save(cfg Config, t *Tokens) error {
	p, err := cachePath(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Forget removes the cached tokens.
func Forget(cfg Config) error {
	p, err := cachePath(cfg)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
