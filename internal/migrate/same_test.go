package migrate

import (
	"bytes"
	"os"
	"testing"
)

// The Go SDK carries its own copy of this package (it's a separate module,
// and mcloud can't depend on an unreleased SDK): keep them the same, so
// mcloud db migrate and the app's runtime never disagree.
func TestSameAsSDK(t *testing.T) {
	for _, name := range []string{"migrate.go", "migrate_test.go"} {
		ours, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sdk, err := os.ReadFile("../../functions/internal/migrate/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ours, sdk) {
			t.Errorf("internal/migrate/%s and functions/internal/migrate/%s differ: copy one over the other", name, name)
		}
	}
}
