package kube

import (
	"context"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PushServiceAccount is the ServiceAccount in an app's namespace whose
// tokens may push the app's images to the maxcloud registry (the maxcloud
// controller makes it; the registry's rules come from the app registry).
const PushServiceAccount = "maxcloud-image-push"

// RegistryPushToken returns a token for pushing app's images to the
// registry at host (the token's audience), valid for d. Getting one takes
// the right to create ServiceAccount tokens in the app's namespace, which
// Kubernetes' admin and edit roles have.
func (c *Client) RegistryPushToken(ctx context.Context, app, host string, d time.Duration) (string, time.Time, error) {
	seconds := int64(d / time.Second)
	tr, err := c.Core.CoreV1().ServiceAccounts(app).CreateToken(ctx, PushServiceAccount, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{Audiences: []string{host}, ExpirationSeconds: &seconds},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("a push token for %s (ServiceAccount %s/%s, made by the maxcloud controller): %w", app, app, PushServiceAccount, err)
	}
	return tr.Status.Token, tr.Status.ExpirationTimestamp.Time, nil
}
