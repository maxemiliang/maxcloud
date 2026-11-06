package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
