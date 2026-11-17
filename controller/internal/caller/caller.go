// Package caller says who sent an HTTP request and what they may do, the
// way the API server would: the request's bearer token (an mcloud login's
// ID token, the same the kubeconfig sends) goes to a TokenReview, and each
// permission to a SubjectAccessReview for that user. The controller's
// public HTTP server uses it for anything that isn't public; it trusts
// nothing the client says about itself besides the token.
package caller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

// ErrUnauthenticated: no token, or one the API server doesn't accept.
var ErrUnauthenticated = errors.New("not logged in: no valid bearer token (mcloud login)")

// User is who the API server says the token belongs to.
type User struct {
	Name   string
	UID    string
	Groups []string
	Extra  map[string]authzv1.ExtraValue
}

// Permission is one thing to check, as kubectl auth can-i would:
// verb, resource (with subresource) and namespace.
type Permission struct {
	Verb, Group, Resource, Subresource, Namespace string
}

func (p Permission) String() string {
	r := p.Resource
	if p.Subresource != "" {
		r += "/" + p.Subresource
	}
	if p.Group != "" {
		r += "." + p.Group
	}
	return fmt.Sprintf("%s %s in namespace %s", p.Verb, r, p.Namespace)
}

// Reviewer checks tokens and permissions with the API server. Results are
// kept for a short while (TTL), keyed by a hash of the token, so a burst
// of requests doesn't become a burst of reviews.
type Reviewer struct {
	// Client creates TokenReviews and SubjectAccessReviews.
	Client client.Client
	// TTL of a cached answer (default 1 minute). A token is never trusted
	// past its expiry by more than this.
	TTL time.Duration

	now   func() time.Time
	mu    sync.Mutex
	users map[[32]byte]cached[*User]
	perms map[string]cached[bool]
}

type cached[T any] struct {
	v   T
	exp time.Time
}

func (r *Reviewer) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return time.Minute
}

func (r *Reviewer) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// BearerToken is the token in a request's Authorization header, or "".
func BearerToken(req *http.Request) string {
	h := req.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// Authenticate returns the token's user, or ErrUnauthenticated.
func (r *Reviewer) Authenticate(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	key := sha256.Sum256([]byte(token))
	now := r.clock()
	r.mu.Lock()
	if c, ok := r.users[key]; ok && now.Before(c.exp) {
		r.mu.Unlock()
		if c.v == nil {
			return nil, ErrUnauthenticated
		}
		return c.v, nil
	}
	r.mu.Unlock()

	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}
	if err := r.Client.Create(ctx, tr); err != nil {
		return nil, fmt.Errorf("reviewing the token: %w", err)
	}
	var u *User
	if tr.Status.Authenticated {
		u = &User{Name: tr.Status.User.Username, UID: tr.Status.User.UID, Groups: tr.Status.User.Groups}
		if len(tr.Status.User.Extra) > 0 {
			u.Extra = map[string]authzv1.ExtraValue{}
			for k, v := range tr.Status.User.Extra {
				u.Extra[k] = authzv1.ExtraValue(v)
			}
		}
	}
	r.mu.Lock()
	if r.users == nil {
		r.users = map[[32]byte]cached[*User]{}
	}
	r.prune(now)
	r.users[key] = cached[*User]{v: u, exp: now.Add(r.ttl())}
	r.mu.Unlock()
	if u == nil {
		return nil, ErrUnauthenticated
	}
	return u, nil
}

// Allowed asks the API server whether u may do p.
func (r *Reviewer) Allowed(ctx context.Context, u *User, p Permission) (bool, error) {
	key := u.Name + "\x00" + u.UID + "\x00" + strings.Join(u.Groups, ",") + "\x00" + p.String()
	now := r.clock()
	r.mu.Lock()
	if c, ok := r.perms[key]; ok && now.Before(c.exp) {
		r.mu.Unlock()
		return c.v, nil
	}
	r.mu.Unlock()

	sar := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
		User: u.Name, UID: u.UID, Groups: u.Groups, Extra: u.Extra,
		ResourceAttributes: &authzv1.ResourceAttributes{
			Namespace: p.Namespace, Verb: p.Verb, Group: p.Group, Resource: p.Resource, Subresource: p.Subresource,
		},
	}}
	if err := r.Client.Create(ctx, sar); err != nil {
		return false, fmt.Errorf("checking permissions: %w", err)
	}
	ok := sar.Status.Allowed && !sar.Status.Denied
	r.mu.Lock()
	if r.perms == nil {
		r.perms = map[string]cached[bool]{}
	}
	r.prune(now)
	r.perms[key] = cached[bool]{v: ok, exp: now.Add(r.ttl())}
	r.mu.Unlock()
	return ok, nil
}

// prune drops expired answers once the caches grow; r.mu is held.
func (r *Reviewer) prune(now time.Time) {
	if len(r.users)+len(r.perms) < 1000 {
		return
	}
	for k, c := range r.users {
		if !now.Before(c.exp) {
			delete(r.users, k)
		}
	}
	for k, c := range r.perms {
		if !now.Before(c.exp) {
			delete(r.perms, k)
		}
	}
}
