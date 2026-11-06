package cli

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

// maxcloud.schema.json (the editor schema, and the source of
// docs/reference/maxcloud-yaml.md) names exactly the fields mcloud reads.
func TestSchemaMatchesProject(t *testing.T) {
	data, err := os.ReadFile("../../maxcloud.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		OneOf      []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"oneOf"`
		Items *struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"items"`
	}
	var root schema
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	sub := func(name string) schema {
		var s schema
		if err := json.Unmarshal(root.Properties[name], &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	check := func(what string, props map[string]json.RawMessage, want []string) {
		t.Helper()
		var got []string
		for k := range props {
			got = append(got, k)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: schema has %v, mcloud reads %v", what, got, want)
		}
	}
	// parseProjectEnv reads env, outside the struct (project.Env is json:"-").
	check("maxcloud.yaml", root.Properties, append(jsonFields(project{}), "env"))
	check("quota", sub("quota").Properties, jsonFields(projectQuota{}))
	check("domains", sub("domains").Items.Properties, jsonFields(registry.Domain{}))
	db := sub("db")
	if len(db.OneOf) != 2 {
		t.Fatalf("db: want oneOf [boolean, settings]")
	}
	// projectDB.UnmarshalJSON reads {size, instances}.
	check("db", db.OneOf[1].Properties, []string{"size", "instances"})
}

func jsonFields(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}
