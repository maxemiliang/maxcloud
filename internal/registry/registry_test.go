package registry

import (
	"strings"
	"testing"
)

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
