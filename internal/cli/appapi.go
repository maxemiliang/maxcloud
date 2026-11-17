package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

// errNoAppAPI: the platform doesn't serve this part of the apps API (an
// older controller, discovery off, or the backend not configured).
var errNoAppAPI = errors.New("the platform doesn't offer this yet")

// appAPI returns a client for the platform's apps API (logs from Loki,
// metrics and alerts from Prometheus) if the discovery document says it
// serves feature. It logs in with the kubeconfig's token.
func (g *globals) appAPI(ctx context.Context, k *kube.Client, feature string) (*appapi.Client, error) {
	if g.testAppAPI != nil {
		return g.testAppAPI, nil
	}
	p, err := discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", errNoAppAPI, err)
	}
	if p.API == nil || p.API.Apps == "" || !slices.Contains(p.API.Features, feature) {
		return nil, fmt.Errorf("%w: %s's discovery document lists no %s", errNoAppAPI, hostOfURL(platformURL()), feature)
	}
	base, err := apiBase(platformURL(), p.Gateway, p.API.Apps)
	if err != nil {
		return nil, err
	}
	httpc, err := k.PlatformHTTP()
	if err != nil {
		return nil, err
	}
	return &appapi.Client{Base: base, HTTP: httpc}, nil
}

// apiBase is the apps API's URL: what the discovery document names, which
// has to be on the platform's API gateway (where it's served, behind the
// gateway's check of the login) or, as older platforms had it, a path on
// the document's own host. The token goes along, so nowhere else, and only
// over HTTPS (or to this machine).
func apiBase(discoveryURL, gateway, apps string) (string, error) {
	d, err := url.Parse(discoveryURL)
	if err != nil {
		return "", err
	}
	u, err := d.Parse(apps)
	if err != nil {
		return "", err
	}
	onGateway := false
	if g, err := url.Parse(gateway); err == nil && gateway != "" {
		onGateway = u.Scheme == g.Scheme && u.Host == g.Host
	}
	if !onGateway && u.Host != d.Host {
		return "", fmt.Errorf("the apps API (%s) is neither on the platform's API gateway nor on the discovery document's host", u)
	}
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return "", fmt.Errorf("the apps API (%s) isn't HTTPS; mcloud won't send your token there", u)
	}
	return u.String(), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hostOfURL(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Host
	}
	return s
}

// apiHint turns an unavailable API into a note on what to do instead.
func apiHint(err error, instead string) string {
	msg := err.Error()
	if errors.Is(err, errNoAppAPI) {
		msg = strings.TrimPrefix(msg, errNoAppAPI.Error()+": ")
		return fmt.Sprintf("%s (%s); %s", errNoAppAPI, msg, instead)
	}
	return msg
}
