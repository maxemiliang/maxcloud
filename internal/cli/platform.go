package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// defaultPlatformURL is where the platform describes itself (the maxcloud
// controller serves it): the cluster's API server and CA, the login
// provider, the image registry and the oldest mcloud it works with. With it,
// mcloud login needs no admin kubeconfig to start from.
const defaultPlatformURL = "https://maxcloud.gronblom.dev/.well-known/maxcloud.json"

// platform is the discovery document.
type platform struct {
	APIServer string `json:"apiServer"`
	// Gateway is the platform's API gateway, if it runs one: the API server
	// for machines whose address isn't on its allowlist. Its certificate is
	// one every machine trusts, so it needs no CA.
	Gateway string `json:"gateway"`
	// CAData is the cluster's CA certificate (PEM), base64.
	CAData string `json:"certificateAuthorityData"`
	OIDC   *struct {
		Issuer   string `json:"issuer"`
		ClientID string `json:"clientID"`
	} `json:"oidc"`
	ImageRegistry string `json:"imageRegistry"`
	Mcloud        *struct {
		MinVersion string `json:"minVersion"`
	} `json:"mcloud"`
	// API: the apps API (logs, metrics, alerts): its URL on the gateway (a
	// path on this document's host, from platforms before the gateway).
	API *struct {
		Apps     string   `json:"apps"`
		Features []string `json:"features"`
	} `json:"api"`
}

// platformURL is $MCLOUD_PLATFORM_URL, else the default; "off" turns
// discovery off.
func platformURL() string {
	return envOr("MCLOUD_PLATFORM_URL", defaultPlatformURL)
}

// discover fetches the discovery document. An error means there's none to
// go by (offline, or a platform without one); callers fall back.
func discover(ctx context.Context) (*platform, error) {
	u := platformURL()
	if u == "off" {
		return nil, fmt.Errorf("discovery is off (MCLOUD_PLATFORM_URL=off)")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	var p platform
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	if p.APIServer == "" || p.CAData == "" {
		return nil, fmt.Errorf("%s names no API server", u)
	}
	return &p, nil
}

// How mcloud reaches the API server (mcloud login --via).
const (
	viaAuto    = "auto"
	viaDirect  = "direct"
	viaGateway = "gateway"
)

// canReach reports whether a TCP connection to rawURL's host opens within
// a few seconds. An API server that doesn't have this machine's address on
// its allowlist drops the connection attempt, which is what this notices.
// A variable so tests needn't dial.
var canReach = func(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// route picks the address (and CA) mcloud's kubeconfig gets: the API server
// itself, or the platform's gateway. auto takes the API server when this
// machine can reach it and the gateway when it can't.
func (p *platform) route(ctx context.Context, via string) (server string, ca []byte, err error) {
	direct := func() (string, []byte, error) {
		ca, err := base64.StdEncoding.DecodeString(p.CAData)
		if err != nil {
			return "", nil, fmt.Errorf("discovery document: certificateAuthorityData: %w", err)
		}
		return p.APIServer, ca, nil
	}
	switch via {
	case viaDirect:
		return direct()
	case viaGateway:
		if p.Gateway == "" {
			return "", nil, fmt.Errorf("--via gateway: the platform has no API gateway (its discovery document names none)")
		}
		return p.Gateway, nil, nil
	case viaAuto, "":
		if p.Gateway == "" || canReach(ctx, p.APIServer) {
			return direct()
		}
		return p.Gateway, nil, nil
	}
	return "", nil, fmt.Errorf("--via %s: it's auto, direct or gateway", via)
}

// minVersion is the oldest mcloud the platform works with ("" if it
// doesn't say).
func (p *platform) minVersion() string {
	if p == nil || p.Mcloud == nil {
		return ""
	}
	return p.Mcloud.MinVersion
}

// olderThan reports whether version v (v1.2.3) is older than min. A
// development build is never older.
func olderThan(v, min string) bool {
	have, want := semver(strings.TrimPrefix(v, "v")), semver(strings.TrimPrefix(min, "v"))
	if have == nil || want == nil {
		return false
	}
	for i := range 3 {
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return false
}

// warnIfOutdated says so when this mcloud is older than the platform wants.
func (g *globals) warnIfOutdated(p *platform) {
	if min := p.minVersion(); min != "" && olderThan(version(), min) {
		g.UI().Warn("this mcloud is %s; the platform wants %s or newer (mise up mcloud, or go install github.com/maxemiliang/maxcloud/cmd/mcloud@latest)", version(), min)
	}
}

// expandHome turns a leading ~/ into the home directory.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
