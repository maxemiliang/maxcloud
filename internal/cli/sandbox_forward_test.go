package cli

import (
	"strings"
	"testing"
)

func TestParseForward(t *testing.T) {
	for spec, want := range map[string][2]int{
		"5173": {5173, 5173}, "8080:3000": {8080, 3000}, "1:65535": {1, 65535},
	} {
		local, session, err := parseForward(spec)
		if err != nil || local != want[0] || session != want[1] {
			t.Errorf("%q: %d, %d, %v", spec, local, session, err)
		}
	}
	// Nothing but ports gets into ssh's -L: not a host, not an option.
	for _, spec := range []string{"", "0", "65536", "-1", "+80", "80:", ":80", "80:x", "a:80", "80:localhost:80", "0.0.0.0:80", "80 ", "8e1", "refactor"} {
		if _, _, err := parseForward(spec); err == nil {
			t.Errorf("%q: want an error", spec)
		}
	}
	if _, err := parseForwards([]string{"3000", "3000:4000"}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("the same port here twice: %v", err)
	}
}

func TestForwardOptions(t *testing.T) {
	got := strings.Join(forwardOptions([]forward{{5173, 5173}, {8080, 3000}}), " ")
	want := "-N -T -o ExitOnForwardFailure yes -o GatewayPorts no -L 5173:localhost:5173 -L 8080:localhost:3000"
	if got != want {
		t.Errorf("%q", got)
	}
}
