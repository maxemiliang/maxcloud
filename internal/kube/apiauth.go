package kube

import (
	"errors"
	"net/http"
	"time"

	"k8s.io/client-go/rest"
)

// PlatformHTTP is an HTTP client for the platform's own APIs (the maxcloud
// controller's apps API) that logs in the way the kubeconfig does: the
// same bearer token (mcloud credential's ID token, refreshed as needed),
// but the system's CAs instead of the cluster's, since those APIs are on a
// public hostname. A kubeconfig that logs in with a client certificate
// (the break-glass admin one) has no token to send; the API then refuses.
func (c *Client) PlatformHTTP() (*http.Client, error) {
	if c.config == nil {
		return nil, errors.New("no cluster login to send to the platform")
	}
	cfg := rest.CopyConfig(c.config)
	cfg.TLSClientConfig = rest.TLSClientConfig{}
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: rt, Timeout: 60 * time.Second}, nil
}
