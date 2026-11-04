package registry

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

func TestRenderParseRoundTrip(t *testing.T) {
	e := &Entry{Name: "shop", Repo: "maxemiliang/shop", CPU: "1", Branch: "1.0", Pods: "30"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	out := string(e.Render())
	for _, want := range []string{`cpu: "1"`, `branch: "1.0"`, `run.gronblom.dev/app: "true"`, "  name: shop\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "memory:") || strings.Contains(out, "path:") {
		t.Errorf("render has unset fields:\n%s", out)
	}
	back, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if *back != *e {
		t.Errorf("round trip: got %+v want %+v", *back, *e)
	}
}

func TestParseUnquoted(t *testing.T) {
	in := "apiVersion: fluxcd.controlplane.io/v1\nkind: ResourceSetInputProvider\nspec:\n  defaultValues:\n    name: run-demo\n    path: apps/run-demo\n    pods: 30\n"
	e, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "run-demo" || e.Path != "apps/run-demo" || e.Pods != "30" {
		t.Errorf("got %+v", *e)
	}
}

func TestValidate(t *testing.T) {
	bad := []Entry{
		{Name: "Shop", Repo: "a/b"},
		{Name: "monitoring", Repo: "a/b"},
		{Name: "kube-thing", Repo: "a/b"},
		{Name: strings.Repeat("a", 31), Repo: "a/b"},
		{Name: "shop"},                              // in-repo app without a path
		{Name: "shop", Repo: "nope"},                // not owner/repo
		{Name: "shop", Repo: "a/b", Path: "../etc"}, // escapes the repo
		{Name: "shop", Repo: "a/b", Path: "/abs"},
		{Name: "shop", Repo: "a/b", CPU: "lots"},
		{Name: "shop", Repo: "a/b", Memory: "0"},
		{Name: "shop", Repo: "a/b", Pods: "-1"},
		{Name: "shop", Repo: "a/b", Branch: "a..b"},
	}
	for _, e := range bad {
		if err := e.Validate(); err == nil {
			t.Errorf("%+v: want an error", e)
		}
	}
	e := Entry{Name: "shop", Repo: "a/b", Path: "./deploy/k8s/"}
	if err := e.Validate(); err != nil || e.Path != "deploy/k8s" {
		t.Errorf("path not normalised: %q, %v", e.Path, err)
	}
}
