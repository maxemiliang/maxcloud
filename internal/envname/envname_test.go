package envname

import (
	"bytes"
	"os"
	"testing"
)

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

// The controller has a copy of this package (a separate module can't
// import it); both must say the same.
func TestSameAsControllers(t *testing.T) {
	mine, err := os.ReadFile("envname.go")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := os.ReadFile("../../controller/internal/envname/envname.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mine, theirs) {
		t.Error("internal/envname/envname.go and controller/internal/envname/envname.go differ: change both")
	}
}
