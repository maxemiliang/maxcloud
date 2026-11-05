package functions

import (
	"strings"
	"testing"
)

func TestSecret(t *testing.T) {
	env := map[string]string{"STRIPE_KEY": "sk"}
	if v, err := secret("STRIPE_KEY", func(k string) string { return env[k] }); err != nil || v != "sk" {
		t.Errorf("secret = %q, %v", v, err)
	}
	if _, err := secret("MISSING", func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "mcloud secrets set MISSING") {
		t.Errorf("missing secret: %v", err)
	}
}
