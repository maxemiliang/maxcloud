package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// sdkFallback is the @max-cloud/functions version a new app gets when npm
// can't be asked for the newest one (offline). release-please bumps it with
// each @max-cloud/functions release (release-please-config.json).
const sdkFallback = "0.6.0" // x-release-please-version

// latestSDK is the newest @max-cloud/functions on npm, else sdkFallback.
func latestSDK(ctx context.Context) string {
	if v := os.Getenv("MCLOUD_SDK_VERSION"); v != "" {
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/@max-cloud/functions/latest", nil)
	if err != nil {
		return sdkFallback
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return sdkFallback
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil || v.Version == "" {
		return sdkFallback
	}
	return v.Version
}

// caretAllows says whether an npm range like ^0.1.0 takes version v. Only
// caret and exact ranges are judged; anything else counts as allowing it.
func caretAllows(rng, v string) bool {
	want := semver(v)
	if want == nil {
		return true
	}
	if !strings.HasPrefix(rng, "^") {
		if got := semver(rng); got != nil {
			return rng == v
		}
		return true
	}
	have := semver(strings.TrimPrefix(rng, "^"))
	if have == nil {
		return true
	}
	// ^1.2.3: 1.x; ^0.2.3: 0.2.x; ^0.0.3: exactly 0.0.3.
	switch {
	case have[0] > 0:
		return want[0] == have[0]
	case have[1] > 0:
		return want[0] == 0 && want[1] == have[1]
	default:
		return want[0] == 0 && want[1] == 0 && want[2] == have[2]
	}
}

func semver(s string) []int {
	parts := strings.SplitN(strings.SplitN(s, "-", 2)[0], ".", 3)
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}
