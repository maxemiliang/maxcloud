package cli

import (
	"errors"
	"fmt"

	yaml3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

// parseProject reads maxcloud.yaml.
func parseProject(data []byte) (*project, error) {
	var p project
	// env isn't decoded here: through JSON, 0.10 would become 0.1.
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	env, err := parseProjectEnv(data)
	if err != nil {
		return nil, err
	}
	p.Env = env
	return &p, nil
}

// parseProjectEnv reads maxcloud.yaml's env: a map of variable names to
// scalars, each taken exactly as written (unquoted 0.10, 010 and true stay
// those characters), as Kubernetes needs strings. nil if there's no env.
func parseProjectEnv(data []byte) (map[string]string, error) {
	var doc yaml3.Node
	if err := yaml3.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml3.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml3.MappingNode {
		return nil, nil
	}
	top := doc.Content[0]
	var envNode *yaml3.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "env" {
			envNode = top.Content[i+1]
		}
	}
	if envNode == nil || (envNode.Kind == yaml3.ScalarNode && envNode.Tag == "!!null") {
		return nil, nil
	}
	if envNode.Kind != yaml3.MappingNode {
		return nil, errors.New("env: a map of names to values, e.g. env: {LOG_LEVEL: debug}")
	}
	env := make(map[string]string, len(envNode.Content)/2)
	for i := 0; i+1 < len(envNode.Content); i += 2 {
		k, v := envNode.Content[i], envNode.Content[i+1]
		name := k.Value
		if err := checkEnvName(name); err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
		if _, dup := env[name]; dup {
			return nil, fmt.Errorf("env: %s is there twice", name)
		}
		if v.Kind == yaml3.AliasNode {
			v = v.Alias
		}
		if v.Kind != yaml3.ScalarNode {
			return nil, fmt.Errorf("env: %s: a value, not a list or map", name)
		}
		if v.Tag == "!!null" && v.Style == 0 && (v.Value == "" || v.Value == "~" || v.Value == "null") {
			return nil, fmt.Errorf(`env: %s has no value (write "" for an empty one)`, name)
		}
		env[name] = v.Value
	}
	return env, nil
}
