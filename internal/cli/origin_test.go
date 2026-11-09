package cli

import (
	"os/exec"
	"testing"
)

func TestGithubRepo(t *testing.T) {
	for _, c := range []struct {
		urls []string
		want string
	}{
		{[]string{"git@github.com:o/r.git"}, "o/r"},
		{[]string{"https://github.com/o/r"}, "o/r"},
		{[]string{"ssh://git@github.com/o/r.git"}, "o/r"},
		{[]string{"git@github-personal:o/r.git"}, ""},
		{[]string{"git@github-personal:o/r.git", "https://github.com/o/r.git"}, "o/r"},
		{nil, ""},
	} {
		if got := githubRepo(c.urls...); got != c.want {
			t.Errorf("githubRepo(%q) = %q, want %q", c.urls, got, c.want)
		}
	}
}

// An insteadOf rule that rewrites github.com to an SSH host alias, as people
// with several GitHub accounts have: origin is still the GitHub repo.
func TestOriginRepoInsteadOf(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "url.git@github-personal:o/.insteadOf", "git@github.com:o/"},
		{"remote", "add", "origin", "git@github.com:o/r.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if out, _ := exec.Command("git", "remote", "get-url", "origin").Output(); string(out) != "git@github-personal:o/r.git\n" {
		t.Fatalf("insteadOf not applied: %q", out)
	}
	if got := originRepo(); got != "o/r" {
		t.Errorf("originRepo() = %q, want o/r", got)
	}
}
