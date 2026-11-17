package caller

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// apiServer answers reviews: tokens maps a token to its user, and allowed
// lists "user verb resource/sub ns" that may.
type apiServer struct {
	tokens            map[string]string
	allowed           map[string]bool
	reviews, accesses int
}

func (a *apiServer) client() client.Client {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return interceptor.NewClient(fake.NewClientBuilder().WithScheme(s).Build(), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch o := obj.(type) {
			case *authnv1.TokenReview:
				a.reviews++
				if u, ok := a.tokens[o.Spec.Token]; ok {
					o.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: u, Groups: []string{"authelia:maxcloud-developers"}}}
				}
			case *authzv1.SubjectAccessReview:
				a.accesses++
				ra := o.Spec.ResourceAttributes
				key := o.Spec.User + " " + ra.Verb + " " + ra.Resource + "/" + ra.Subresource + " " + ra.Namespace
				o.Status.Allowed = a.allowed[key]
			default:
				return errors.New("unexpected create")
			}
			return nil
		},
	})
}

func TestAuthenticateAndAllowed(t *testing.T) {
	api := &apiServer{
		tokens:  map[string]string{"good": "authelia:ann"},
		allowed: map[string]bool{"authelia:ann get pods/log shop": true},
	}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	r := &Reviewer{Client: api.client(), now: func() time.Time { return now }}
	ctx := context.Background()

	if _, err := r.Authenticate(ctx, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("no token: %v", err)
	}
	if _, err := r.Authenticate(ctx, "bad"); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("bad token: %v", err)
	}
	u, err := r.Authenticate(ctx, "good")
	if err != nil || u.Name != "authelia:ann" {
		t.Fatalf("good token: %v, %v", u, err)
	}
	for ns, want := range map[string]bool{"shop": true, "other": false} {
		ok, err := r.Allowed(ctx, u, Permission{Verb: "get", Resource: "pods", Subresource: "log", Namespace: ns})
		if err != nil || ok != want {
			t.Errorf("%s: allowed = %v, %v", ns, ok, err)
		}
	}

	// Answers are reused for the TTL, then asked again.
	reviews, accesses := api.reviews, api.accesses
	r.Authenticate(ctx, "good")
	r.Authenticate(ctx, "bad")
	r.Allowed(ctx, u, Permission{Verb: "get", Resource: "pods", Subresource: "log", Namespace: "shop"})
	if api.reviews != reviews || api.accesses != accesses {
		t.Errorf("cached answers were asked again: %d reviews, %d accesses", api.reviews, api.accesses)
	}
	now = now.Add(2 * time.Minute)
	r.Authenticate(ctx, "good")
	if api.reviews != reviews+1 {
		t.Errorf("an expired answer wasn't asked again")
	}
}

func TestBearerToken(t *testing.T) {
	for h, want := range map[string]string{
		"Bearer abc":  "abc",
		"bearer  abc": "abc",
		"Basic abc":   "",
		"":            "",
		"Bearer":      "",
	} {
		req := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		if got := BearerToken(req); got != want {
			t.Errorf("%q: got %q", h, got)
		}
	}
}
