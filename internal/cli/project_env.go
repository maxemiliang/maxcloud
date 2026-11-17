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
	// The environments' and previews' env:, the same way.
	for name, e := range p.Environments {
		if e == nil {
			e = &projectEnvironment{}
			p.Environments[name] = e
		}
		if e.Env, err = parseEnvAt(data, "environments", name, "env"); err != nil {
			return nil, fmt.Errorf("environments.%s.%w", name, err)
		}
	}
	if p.Previews != nil {
		if p.Previews.Env, err = parseEnvAt(data, "previews", "env"); err != nil {
			return nil, fmt.Errorf("previews.%w", err)
		}
	}
	return &p, nil
}

// parseEnvAt reads an env: map below the top level (environments.staging.env),
// as parseProjectEnv does the app's. nil if it isn't there.
func parseEnvAt(data []byte, path ...string) (map[string]string, error) {
	var doc yaml3.Node
	if err := yaml3.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml3.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	node := doc.Content[0]
	for _, key := range path {
		if node.Kind == yaml3.AliasNode {
			node = node.Alias
		}
		if node.Kind != yaml3.MappingNode {
			return nil, nil
		}
		var next *yaml3.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = node.Content[i+1]
			}
		}
		if next == nil {
			return nil, nil
		}
		node = next
	}
	return envFromNode(node)
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
	return envFromNode(envNode)
}

// envFromNode reads an env: node: a map of names to scalars, each as
// written.
func envFromNode(envNode *yaml3.Node) (map[string]string, error) {
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
