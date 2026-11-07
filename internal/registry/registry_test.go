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

func TestBuckets(t *testing.T) {
	e := Entry{Name: "shop", Repo: "a/b", Buckets: " uploads , assets:public,"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.Buckets != "uploads,assets:public" {
		t.Errorf("Buckets normalised to %q", e.Buckets)
	}
	bs, err := ParseBuckets(e.Buckets)
	if err != nil || len(bs) != 2 || bs[0] != (Bucket{Name: "uploads"}) || bs[1] != (Bucket{Name: "assets", Public: true}) {
		t.Errorf("ParseBuckets = %+v, %v", bs, err)
	}
	back, err := Parse(e.Render())
	if err != nil || back.Buckets != e.Buckets {
		t.Errorf("round trip: %+v, %v", back, err)
	}

	for _, bad := range []string{
		"db",                    // the database's backups
		"a--b",                  // "--" separates app and bucket
		"Uploads",               // uppercase
		"up.loads",              // dots
		"x:private",             // unknown option
		"x,x",                   // twice
		"x-s3alias",             // reserved by S3
		strings.Repeat("a", 55), // mc-shop--<55> is 64 characters
	} {
		e := Entry{Name: "shop", Repo: "a/b", Buckets: bad}
		if err := e.Validate(); err == nil {
			t.Errorf("buckets %q: want an error", bad)
		}
	}
	if err := (&Entry{Name: "sh--op", Repo: "a/b"}).Validate(); err == nil {
		t.Error("app name with -- should be rejected")
	}
}

func TestDB(t *testing.T) {
	e := Entry{Name: "shop", Repo: "a/b", DB: "true", DBInstances: "2", DBSize: "2Gi"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	back, err := Parse(e.Render())
	if err != nil || *back != e {
		t.Errorf("round trip: %+v, %v", back, err)
	}
	off := Entry{Name: "shop", Repo: "a/b", DB: "false"}
	if err := off.Validate(); err != nil || off.DB != "" {
		t.Errorf("db=false: %q, %v", off.DB, err)
	}
	for _, bad := range []Entry{
		{Name: "shop", Repo: "a/b", DB: "yes"},
		{Name: "shop", Repo: "a/b", DBSize: "2Gi"},                // without --db
		{Name: "shop", Repo: "a/b", DB: "true", DBInstances: "3"}, // 1 or 2
		{Name: "shop", Repo: "a/b", DB: "true", DBSize: "lots"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}

func TestObjectRoundTrip(t *testing.T) {
	e := Entry{Name: "shop", Repo: "a/b", CPU: "500m", Buckets: "uploads,assets:public", DB: "true", DBSize: "2Gi"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	o := e.Object()
	labels := o["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels[AppLabel] != "true" || labels[ManagedByLabel] != ManagedBy {
		t.Errorf("labels = %v", labels)
	}
	back, err := FromObject(o)
	if err != nil || *back != e {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

func TestDomains(t *testing.T) {
	e := &Entry{Name: "paja", Repo: "a/b", Domains: " Paja.lankkupelit.fi:web:proxied , api.example.com:api"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.Domains != "paja.lankkupelit.fi:web:proxied,api.example.com:api" {
		t.Errorf("normalised to %q", e.Domains)
	}
	ds, _ := ParseDomains(e.Domains)
	if len(ds) != 2 || !ds[0].Proxied || ds[1].Proxied || ds[1].Service != "api" {
		t.Errorf("parsed %+v", ds)
	}
	back, err := Parse(e.Render())
	if err != nil || back.Domains != e.Domains {
		t.Errorf("round trip: %q, %v", back.Domains, err)
	}
	for _, bad := range []string{
		"paja.lankkupelit.fi",                 // no service
		"paja.lankkupelit.fi:web:cached",      // unknown option
		"*.lankkupelit.fi:web",                // wildcard
		"localhost:web",                       // one label
		"paja.lankkupelit.fi:Web",             // not a service name
		"a.example.com:web,a.example.com:api", // twice
		"paja.lankkupelit.fi:web:proxied:x",
	} {
		e := &Entry{Name: "paja", Repo: "a/b", Domains: bad}
		if err := e.Validate(); err == nil {
			t.Errorf("domains %q: want an error", bad)
		}
	}
}

func TestOwners(t *testing.T) {
	e := &Entry{Name: "shop", Repo: "a/shop", Owners: " alice, bob,alice,, maxemiliang "}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.Owners != "alice,bob,maxemiliang" {
		t.Errorf("owners = %q", e.Owners)
	}
	if !e.HasOwner("bob") || e.HasOwner("carol") {
		t.Errorf("HasOwner: %v", e.OwnerList())
	}
	back, err := FromObject(e.Object())
	if err != nil || back.Owners != e.Owners {
		t.Errorf("object round trip: %+v, %v", back, err)
	}
	for _, bad := range []string{"al ice", "-alice", "alice-", "a\nb", "authelia:alice", strings.Repeat("a", 65)} {
		e := &Entry{Name: "shop", Repo: "a/shop", Owners: bad}
		if err := e.Validate(); err == nil {
			t.Errorf("owners %q passed", bad)
		}
	}
	for _, good := range []string{"a", "alice.smith", "a_b", "alice@example.com", "Alice"} {
		e := &Entry{Name: "shop", Repo: "a/shop", Owners: good}
		if err := e.Validate(); err != nil {
			t.Errorf("owners %q: %v", good, err)
		}
	}
}
