package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/maxemiliang/maxcloud/internal/registry"
)

// projectEnvironment is one of maxcloud.yaml's environments: a long-lived
// copy of the app (staging), deployed from its own branch into its own
// namespace, <app>--<env>, with its own secrets, variables, buckets and
// database. What it leaves out is the app's (quota, buckets, db), except
// domains, which it has none of unless it says.
type projectEnvironment struct {
	// Branch it deploys from (GitOps); default the environment's name.
	Branch  string            `json:"branch"`
	Quota   *projectQuota     `json:"quota"`
	Buckets []string          `json:"buckets"`
	DB      *projectDB        `json:"db"`
	Domains []registry.Domain `json:"domains"`
	// Env: variables for this environment's functions, over the app's
	// env:. Read by parseProject, as the app's are.
	Env map[string]string `json:"-"`
}

// projectPreviews is previews: true, false, or settings.
type projectPreviews struct {
	Enabled bool
	TTL     string // how long a preview lives after its last deploy
	Max     string // how many the app may have at once
	DB      bool   // an empty database in each
	// Env: variables for previews' functions, over the app's env:.
	Env map[string]string
}

func (pp *projectPreviews) UnmarshalJSON(b []byte) error {
	var on bool
	if err := json.Unmarshal(b, &on); err == nil {
		pp.Enabled = on
		return nil
	}
	var v struct {
		TTL string `json:"ttl"`
		Max any    `json:"max"`
		DB  bool   `json:"db"`
		Env any    `json:"env"` // read by parseProject
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return errors.New("previews: true, false, or {ttl: 72h, max: 2, db: true, env: {...}}")
	}
	pp.Enabled, pp.TTL, pp.DB = true, v.TTL, v.DB
	if v.Max != nil {
		pp.Max = fmt.Sprint(v.Max)
	}
	return nil
}

// spec is the environment as the registry entry's environments JSON keeps
// it (the maxcloud controller's AppSync writes the same).
func (e *projectEnvironment) spec() map[string]string {
	s := map[string]string{}
	if e.Branch != "" {
		s["branch"] = e.Branch
	}
	if q := e.Quota; q != nil {
		for k, v := range map[string]string{"cpu": q.CPU, "memory": q.Memory, "pods": q.Pods} {
			if v != "" {
				s[k] = v
			}
		}
	}
	if e.Buckets != nil {
		parts := make([]string, 0, len(e.Buckets))
		for _, b := range e.Buckets {
			parts = append(parts, strings.TrimSpace(b))
		}
		s["buckets"] = strings.Join(parts, ",")
	}
	if d := e.DB; d != nil {
		if d.Enabled {
			s["db"] = "true"
			if d.Size != "" {
				s["dbSize"] = d.Size
			}
			if d.Instances != "" {
				s["dbInstances"] = d.Instances
			}
		} else {
			s["db"] = "false"
		}
	}
	if e.Domains != nil {
		s["domains"] = registry.FormatDomains(e.Domains)
	}
	return s
}

// environmentsValue is the registry entry's environments: JSON with sorted
// keys; "{}" for none (an entry without the key keeps the environments it
// has).
func (p *project) environmentsValue() string {
	out := map[string]map[string]string{}
	for name, e := range p.Environments {
		out[name] = e.spec()
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err) // strings only
	}
	return string(b)
}

// checkEnvironments checks the environments' names and branches; the rest
// is checked as the entries they become (the controller, admission).
func (p *project) checkEnvironments() error {
	for _, name := range slices.Sorted(maps.Keys(p.Environments)) {
		if err := registry.ValidateEnvironment(p.App, name); err != nil {
			return fmt.Errorf("environments: %w", err)
		}
		if strings.HasPrefix(name, registry.PullRequestPrefix) {
			return fmt.Errorf("environments: %s: names starting with %s are pull requests' previews", name, registry.PullRequestPrefix)
		}
		e := p.Environments[name]
		if e == nil {
			p.Environments[name] = &projectEnvironment{}
			continue
		}
		if e.Branch != "" && (!validBranch(e.Branch) || e.Branch == p.branch()) {
			return fmt.Errorf("environments.%s.branch %q: a branch of its own (not the app's, %s)", name, e.Branch, p.branch())
		}
		if e.Branch == "" && name == p.branch() {
			return fmt.Errorf("environments.%s: deploys from branch %s by default, which is the app's: give it branch:", name, name)
		}
	}
	return nil
}

func validBranch(b string) bool {
	if b == "" || strings.Contains(b, "..") {
		return false
	}
	for _, r := range b {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/-", r)) {
			return false
		}
	}
	return true
}

// branch is the app's own (production) branch.
func (p *project) branch() string {
	if p.Branch != "" {
		return p.Branch
	}
	return registry.DefaultBranch
}

// environmentBranch is the branch environment env deploys from.
func (p *project) environmentBranch(env string) string {
	if e := p.Environments[env]; e != nil && e.Branch != "" {
		return e.Branch
	}
	return env
}

// environmentForBranch is the environment that deploys from branch, if any.
func (p *project) environmentForBranch(branch string) (string, bool) {
	for _, name := range slices.Sorted(maps.Keys(p.Environments)) {
		if p.environmentBranch(name) == branch {
			return name, true
		}
	}
	return "", false
}

// envFor is the variables a build for environment env (or a preview, with
// env "") writes into the functions: the app's env:, with the
// environment's (or previews') over it.
func (p *project) envFor(env string, preview bool) map[string]string {
	out := maps.Clone(p.Env)
	if out == nil {
		out = map[string]string{}
	}
	var extra map[string]string
	switch {
	case preview && p.Previews != nil:
		extra = p.Previews.Env
	case env != "":
		if e := p.Environments[env]; e != nil {
			extra = e.Env
		}
	}
	maps.Copy(out, extra)
	if len(out) == 0 {
		return nil
	}
	return out
}

// envFlag is the global --env: the environment (or preview) commands act
// on. Bound by newRoot; $MCLOUD_ENV otherwise.
var envFlag string

// targetEnvironment is --env, else $MCLOUD_ENV.
func targetEnvironment() string {
	if envFlag != "" {
		return envFlag
	}
	return os.Getenv("MCLOUD_ENV")
}

// withEnvironment is the app or environment a command acts on: app, or
// <app>--<env> with --env (or $MCLOUD_ENV). An argument that already names
// an environment (shop--staging) is taken as it is.
func withEnvironment(app string) (string, error) {
	env := targetEnvironment()
	if env == "" {
		return app, nil
	}
	if parent, own, ok := registry.SplitEnvironment(app); ok {
		if own != env {
			return "", fmt.Errorf("%s is environment %s of %s, but --env says %s", app, own, parent, env)
		}
		return app, nil
	}
	if err := registry.ValidateEnvironment(app, env); err != nil {
		return "", err
	}
	return registry.EnvironmentName(app, env), nil
}
