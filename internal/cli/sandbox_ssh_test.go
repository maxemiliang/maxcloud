package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestParseSSHHost(t *testing.T) {
	for _, c := range []struct {
		host, session, user string
		ok                  bool
	}{
		{"work.sbx", "work", "", true},
		{"work.alice.sbx", "work", "alice", true},
		{"session-x1z.sbx", "session-x1z", "", true},
		{"work", "", "", false},
		{"work.sbx.evil", "", "", false},
		{".sbx", "", "", false},
		{"a.b.c.sbx", "", "", false},
		{"../x.sbx", "", "", false},
		{"work..sbx", "", "", false},
		{"work/x.sbx", "", "", false},
	} {
		session, user, err := parseSSHHost(c.host)
		if (err == nil) != c.ok || session != c.session || user != c.user {
			t.Errorf("%q: %q %q %v", c.host, session, user, err)
		}
	}
}

func TestSSHConfigPath(t *testing.T) {
	got, err := sshConfigPath("/home/a b/50%/key")
	if err != nil || got != `"/home/a b/50%%/key"` {
		t.Errorf("%s %v", got, err)
	}
	for _, p := range []string{`/a"b`, "/a\nb", `/a\b`} {
		if _, err := sshConfigPath(p); err == nil {
			t.Errorf("%q: no error", p)
		}
	}
}

// A shell gets back the same word, whatever is in it.
func TestShellQuote(t *testing.T) {
	for _, s := range []string{"plain", "with space", "it's", `$HOME "x" \n`, ""} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("%q: %q %v", s, out, err)
		}
	}
}

// ssh runs ProxyCommand with a shell, the host name put in as it is: the
// shell must see one quoted word, whatever the name.
func TestProxyCommandQuotes(t *testing.T) {
	g := &globals{kubeconfig: "/k c/50%/x'y", kubeContext: "ctx"}
	got, err := g.proxyCommand()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, ` '--kubeconfig' '/k c/50%%/x'\''y' '--context' 'ctx' sandbox ssh-proxy '%n'`) {
		t.Errorf("%s", got)
	}
	host := "x$(touch pwned);y.sbx"
	cmd := strings.NewReplacer("%%", "%", "%n", host).Replace(`printf %s '%n'`)
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil || string(out) != host {
		t.Errorf("%q %v", out, err)
	}
}

func TestOnlyForwards(t *testing.T) {
	for _, c := range []struct {
		args string
		want bool
	}{
		{"", false},
		{"-L 3000:localhost:3000", false},
		{"-N -L 3000:localhost:3000", true},
		{"-fNT", true},
		{"-o ServerAliveInterval=5", false},
		{"-L N:localhost:1", false},
	} {
		if got := onlyForwards(strings.Fields(c.args)); got != c.want {
			t.Errorf("%q: %v", c.args, got)
		}
	}
}

func TestSSHKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")
	file, line, err := sshKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info.Mode(), err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil || !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Errorf("%q: %v", line, err)
	}
	file2, line2, err := sshKey(dir)
	if err != nil || file2 != file || line2 != line {
		t.Errorf("a second call made another key: %q %v", line2, err)
	}
	pub, err := os.ReadFile(file + ".pub")
	if err != nil || string(pub) != line+"\n" {
		t.Errorf("%q %v", pub, err)
	}
}

func TestWriteKnownHost(t *testing.T) {
	dir := t.TempDir()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	if err := writeKnownHost(dir, "work.sbx", key); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(knownHostsFile(dir, "work.sbx"))
	if err != nil || string(got) != "* "+string(ssh.MarshalAuthorizedKey(key)) {
		t.Errorf("%q %v", got, err)
	}
}

// What mcloud gives ssh forwards nothing of the user's to a session.
func TestSSHOptions(t *testing.T) {
	opts, err := sshOptions("/cfg/mcloud/sandbox-ssh", "mcloud sandbox ssh-proxy %n")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range opts {
		got[o[0]] = o[1]
	}
	for k, v := range map[string]string{
		"ForwardAgent":          "no",
		"ForwardX11":            "no",
		"IdentityAgent":         "none",
		"IdentitiesOnly":        "yes",
		"StrictHostKeyChecking": "yes",
		"PermitLocalCommand":    "no",
		"UserKnownHostsFile":    `"/cfg/mcloud/sandbox-ssh/known_hosts/%n"`,
		"IdentityFile":          `"/cfg/mcloud/sandbox-ssh/id_ed25519"`,
	} {
		if got[k] != v {
			t.Errorf("%s %q, want %q", k, got[k], v)
		}
	}
}

func TestIncludeSSHConfig(t *testing.T) {
	dir := t.TempDir()
	userConfig := filepath.Join(dir, ".ssh", "config")
	file := filepath.Join(dir, "mcloud", "config")
	added, err := includeSSHConfig(userConfig, file)
	if err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	info, err := os.Stat(userConfig)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info.Mode(), err)
	}
	own := "Host *\n  ForwardAgent yes\n"
	os.WriteFile(userConfig, []byte(own), 0o600)
	os.Chmod(userConfig, 0o640)
	if added, err := includeSSHConfig(userConfig, file); err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	got, _ := os.ReadFile(userConfig)
	include := `Include "` + file + `"`
	if !strings.Contains(string(got), include+"\n") || !strings.HasSuffix(string(got), "\n\n"+own) || strings.Index(string(got), include) > strings.Index(string(got), "Host *") {
		t.Errorf("not first, or the rest changed:\n%s", got)
	}
	if info, _ := os.Stat(userConfig); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode())
	}
	if added, err := includeSSHConfig(userConfig, file); err != nil || added {
		t.Errorf("added twice: %v %v", added, err)
	}
}
