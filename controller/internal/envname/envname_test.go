package envname

import "testing"

func TestCheck(t *testing.T) {
	for name, ok := range map[string]bool{
		"STRIPE_KEY": true, "_X": true, "A1": true,
		"1A": false, "lower": false, "WITH-DASH": false, "": false,
		"DATABASE_URL": false, "FUNCTION_TARGET": false, "PORT": false,
		"AWS_REGION": false, "MAXCLOUD_APP": false, "K_SERVICE": false,
	} {
		if err := Check(name); (err == nil) != ok {
			t.Errorf("%q: %v", name, err)
		}
	}
}
