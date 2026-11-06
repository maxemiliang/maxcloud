package kube

import (
	"strings"
	"testing"
)

func TestBuildPod(t *testing.T) {
	b := ClusterBuild{
		Builder: "paketobuildpacks/ubuntu-noble-builder:1", Image: "r.example/shop/shop:t", Cache: "r.example/shop/shop:build-cache",
		Registry: "r.example", Token: "tok", Env: map[string]string{"BP_NODE_RUN_SCRIPTS": "build", "Q": "it's"},
	}
	pod, secret := buildPod("b1", b)
	if !strings.Contains(secret.StringData["CNB_REGISTRY_AUTH"], `"r.example":"Basic bWF4Y2xvdWQ6dG9r"`) {
		t.Errorf("auth: %s", secret.StringData["CNB_REGISTRY_AUTH"])
	}
	c := pod.Spec.Containers[0]
	script := c.Command[2]
	for _, want := range []string{
		"printf '%s' 'build' > /platform/env/BP_NODE_RUN_SCRIPTS",
		`printf '%s' 'it'\''s' > /platform/env/Q`,
		"-cache-image=r.example/shop/shop:build-cache -report=/workspace/report.toml r.example/shop/shop:t",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "tok") || len(c.Env) != 1 {
		t.Error("the token is in the pod spec")
	}
	if pod.Spec.PriorityClassName != "tenant" || *pod.Spec.SecurityContext.RunAsUser != cnbUID || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("pod security: %+v", pod.Spec.SecurityContext)
	}
}

func TestReportDigest(t *testing.T) {
	logs := "[exporter] done\n" + reportBegin + "\n[image]\n  tags = [\"x\"]\n  digest = \"sha256:abc\"\n  manifest-size = 1\n"
	if d := reportDigest(logs); d != "sha256:abc" {
		t.Errorf("digest %q", d)
	}
	var out strings.Builder
	f := &reportFilter{w: &out}
	f.Write([]byte("building\n"))
	f.Write([]byte("more\n" + reportBegin + "\n[image]"))
	f.Write([]byte("digest = 1"))
	if out.String() != "building\nmore\n" {
		t.Errorf("shown: %q", out.String())
	}
}
