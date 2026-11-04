package kustomize

import (
	"strings"
	"testing"
)

const kust = `# maxcloud: apps on the run platform.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - resourceset.yaml
  - apps/lankkupaja.yaml
  - apps/run-demo.yaml
`

func TestAddResourceSorted(t *testing.T) {
	cases := map[string]string{
		"apps/aaa.yaml": "  - resourceset.yaml\n  - apps/aaa.yaml\n  - apps/lankkupaja.yaml\n",
		"apps/mmm.yaml": "  - apps/lankkupaja.yaml\n  - apps/mmm.yaml\n  - apps/run-demo.yaml\n",
		"apps/zzz.yaml": "  - apps/run-demo.yaml\n  - apps/zzz.yaml\n",
	}
	for res, want := range cases {
		got, changed, err := AddResource(kust, res)
		if err != nil || !changed {
			t.Fatalf("%s: changed=%v err=%v", res, changed, err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: got\n%s", res, got)
		}
		if !strings.HasPrefix(got, "# maxcloud") {
			t.Errorf("%s: lost the comment", res)
		}
	}
}

func TestAddResourceIdempotent(t *testing.T) {
	got, changed, err := AddResource(kust, "apps/run-demo.yaml")
	if err != nil || changed || got != kust {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestAddResourceEmptyDir(t *testing.T) {
	in := "resources:\n  - resourceset.yaml\n# trailing comment\n"
	got, _, err := AddResource(in, "apps/a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := "resources:\n  - resourceset.yaml\n  - apps/a.yaml\n# trailing comment\n"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestRemoveResource(t *testing.T) {
	got, changed, err := RemoveResource(kust, "apps/lankkupaja.yaml")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Contains(got, "lankkupaja") || !strings.Contains(got, "apps/run-demo.yaml") {
		t.Errorf("got\n%s", got)
	}
	if _, changed, _ := RemoveResource(kust, "apps/nope.yaml"); changed {
		t.Error("removed an entry that isn't there")
	}
}

// The layout `kustomize edit` writes: list items not indented.
func TestUnindentedList(t *testing.T) {
	in := "kind: Kustomization\nresources:\n- functions.yaml\n# comment\nimages:\n- digest: sha256:abc\n  name: app\n"
	got, changed, err := AddResource(in, "functions.yaml")
	if err != nil || changed || got != in {
		t.Fatalf("already listed: changed=%v err=%v\n%s", changed, err, got)
	}
	got, _, err = AddResource(in, "database.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "resources:\n- database.yaml\n- functions.yaml\n# comment\nimages:\n- digest") {
		t.Errorf("got\n%s", got)
	}
	got, changed, _ = RemoveResource(got, "database.yaml")
	if !changed || got != in {
		t.Errorf("remove: got\n%s", got)
	}
}
