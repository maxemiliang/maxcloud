package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

// sshSuffix ends the host names ssh knows sessions by: SESSION.sbx for
// one of your own, SESSION.USER.sbx for USER's.
const sshSuffix = ".sbx"

// sshHost is the host name ssh knows user's session by.
func sshHost(session, user string) string { return session + "." + user + sshSuffix }

// parseSSHHost reads a host name ssh was given (SESSION.sbx or
// SESSION.USER.sbx). user is "" for the caller's own.
func parseSSHHost(host string) (session, user string, err error) {
	name, ok := strings.CutSuffix(strings.ToLower(host), sshSuffix)
	parts := strings.Split(name, ".")
	if !ok || len(parts) > 2 {
		return "", "", fmt.Errorf("%q isn't a session's host name: SESSION%s, or SESSION.USER%s for someone else's", host, sshSuffix, sshSuffix)
	}
	if err := kube.CheckSessionName(parts[0]); err != nil {
		return "", "", err
	}
	if len(parts) == 2 {
		if err := kube.CheckSandboxUser(parts[1]); err != nil {
			return "", "", err
		}
		user = parts[1]
	}
	return parts[0], user, nil
}

// sshDir holds the key mcloud logs in to sessions with, and the host keys
// it has read from them: one file per host name, written by ssh-proxy
// before each connection.
func sshDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcloud", "sandbox-ssh"), nil
}

// sshKey makes the key mcloud logs in to sessions with, if there's none,
// and returns its file and its authorized_keys line. It is a key of its
// own, not one of yours: only the public half goes into a session.
func sshKey(dir string) (file, authorized string, err error) {
	file = filepath.Join(dir, "id_ed25519")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	pem, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		pem, err = newSSHKey(file)
	}
	if err != nil {
		return "", "", err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", file, err)
	}
	authorized = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " mcloud-sandbox"
	// ssh reads the .pub next to a key: rewriting it while another
	// connection starts would hand that one half a file.
	if pub, err := os.ReadFile(file + ".pub"); err != nil || string(pub) != authorized+"\n" {
		if err := os.WriteFile(file+".pub", []byte(authorized+"\n"), 0o644); err != nil {
			return "", "", err
		}
	}
	return file, authorized, nil
}

// newSSHKey writes a new ed25519 private key to file, unless another
// mcloud got there first, and returns what file holds.
func newSSHKey(file string) ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "mcloud-sandbox")
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".id_ed25519-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(pem.EncodeToMemory(block))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), file); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return os.ReadFile(file)
}

// knownHostsFile is where the host key of the session ssh calls host goes.
func knownHostsFile(dir, host string) string { return filepath.Join(dir, "known_hosts", host) }

// writeKnownHost makes key the one key ssh accepts for host. The file is
// that host's alone, so the line names any host.
func writeKnownHost(dir, host string, key ssh.PublicKey) error {
	file := knownHostsFile(dir, host)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append([]byte("* "), ssh.MarshalAuthorizedKey(key)...))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// sshOptions are the ssh settings for sessions, in order; proxy is the
// ProxyCommand. Nothing of the user's is forwarded to a session, where
// the agent could use it: no SSH agent, no X11, no local commands.
func sshOptions(dir, proxy string) ([][2]string, error) {
	key, err := sshConfigPath(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		return nil, err
	}
	known, err := sshConfigPath(filepath.Join(dir, "known_hosts"))
	if err != nil {
		return nil, err
	}
	return [][2]string{
		{"ProxyCommand", proxy},
		{"User", kube.SandboxSSHUser},
		{"IdentityFile", key},
		{"IdentitiesOnly", "yes"},
		{"IdentityAgent", "none"},
		{"UserKnownHostsFile", strings.TrimSuffix(known, `"`) + `/%n"`},
		{"StrictHostKeyChecking", "yes"},
		{"UpdateHostKeys", "no"},
		{"CheckHostIP", "no"},
		{"ForwardAgent", "no"},
		{"ForwardX11", "no"},
		{"ForwardX11Trusted", "no"},
		{"PermitLocalCommand", "no"},
		{"ServerAliveInterval", "30"},
	}, nil
}

// sshConfigPath is p quoted for an ssh setting that takes a file: double
// quotes, forward slashes, % (ssh's tokens) doubled.
func sshConfigPath(p string) (string, error) {
	if runtime.GOOS == "windows" {
		p = filepath.ToSlash(p)
	}
	if strings.ContainsAny(p, "\"\\\n\r") {
		return "", fmt.Errorf("can't name %q in an ssh setting: it has a quote, backslash or line break in it", p)
	}
	return `"` + strings.ReplaceAll(p, "%", "%%") + `"`, nil
}

// shellQuote is s as one word for a POSIX shell (and fish).
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// proxyCommand is the ProxyCommand that runs this mcloud's ssh-proxy, on
// the same cluster as this command.
func (g *globals) proxyCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	argv := []string{exe}
	if g.kubeconfig != "" {
		abs, err := filepath.Abs(g.kubeconfig)
		if err != nil {
			return "", err
		}
		argv = append(argv, "--kubeconfig", abs)
	}
	if g.kubeContext != "" {
		argv = append(argv, "--context", g.kubeContext)
	}
	words := make([]string, 0, len(argv)+3)
	for _, a := range argv {
		if strings.ContainsAny(a, "\n\r") {
			return "", fmt.Errorf("can't put %q in an ssh setting: it has a line break in it", a)
		}
		words = append(words, strings.ReplaceAll(shellQuote(a), "%", "%%"))
	}
	return strings.Join(append(words, "sandbox", "ssh-proxy", "'%n'"), " "), nil
}

// sshHostPattern is the Host line's patterns. ssh puts the host name into
// ProxyCommand as it is, for a shell to run, and before OpenSSH 9.6 it
// doesn't refuse one with shell syntax in it (a submodule's URL, say):
// ProxyCommand quotes it, and a name with a quote in it isn't a session's.
const sshHostPattern = `*` + sshSuffix + ` "!*'*"`

// sshConfig is the ssh configuration for every session, for
// ~/.ssh/config.
func (g *globals) sshConfig() (string, error) {
	dir, err := sshDir()
	if err != nil {
		return "", err
	}
	proxy, err := g.proxyCommand()
	if err != nil {
		return "", err
	}
	opts, err := sshOptions(dir, proxy)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent sessions (mcloud sandbox): ssh SESSION%s for yours, SESSION.USER%s for USER's.\n", sshSuffix, sshSuffix)
	fmt.Fprintf(&b, "# Written by mcloud sandbox ssh-config. Nothing of yours is forwarded to a session.\n")
	fmt.Fprintf(&b, "Host %s\n", sshHostPattern)
	for _, o := range opts {
		fmt.Fprintf(&b, "  %s %s\n", o[0], o[1])
	}
	return b.String(), nil
}

func sandboxSSHCmd(g *globals, f *sandboxFlags) *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "ssh [SESSION] [-- SSH-OPTIONS...]",
		Short: "SSH into a session",
		Long: `Open a shell in a session over SSH: in the directory the session was
started --from, or its work directory (/work). What follows -- goes to ssh
as options, -L to open a port of the session's here, say. SESSION can be
left out when you have one session.

The connection goes through the cluster's API, like every other command
here: an exec of sshd in the session, with SSH over its input and output.
Nothing in the session listens, and you need nothing but your mcloud
login. mcloud logs in with a key of its own, and checks the session's
host key, which it reads from the session first. Your SSH agent, X11 and
remote forwarding (-R) are never forwarded: the session can't reach back
to your machine. -L reaches what listens on the session's own loopback,
nothing else.

For scp, rsync, sftp and editors that work over SSH, mcloud sandbox
ssh-config gives ssh a host name for every session.`,
		Example: `  mcloud sandbox ssh
  mcloud sandbox ssh refactor -- -L 3000:localhost:3000
  mcloud sandbox ssh -- -N -L 5173:localhost:5173     # only the port`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			session, rest, err := sessionAndArgs(cmd, args)
			if err != nil {
				return err
			}
			sshPath, err := exec.LookPath("ssh")
			if err != nil {
				return errors.New("no ssh here: install OpenSSH's client")
			}
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			in, err := s.session(ctx, session)
			if err != nil {
				return err
			}
			has, err := s.k.HasSSHServer(ctx, s.user, in.Name)
			if err != nil {
				return err
			}
			if !has {
				return kube.ErrNoSSHServer
			}
			keyDir, err := sshDir()
			if err != nil {
				return err
			}
			if _, _, err := sshKey(keyDir); err != nil {
				return err
			}
			proxy, err := g.proxyCommand()
			if err != nil {
				return err
			}
			opts, err := sshOptions(keyDir, proxy)
			if err != nil {
				return err
			}
			argv := make([]string, 0, 2*len(opts)+len(rest)+4)
			for _, o := range opts {
				argv = append(argv, "-o", o[0]+" "+o[1])
			}
			argv = append(argv, rest...)
			if !onlyForwards(rest) {
				if isTerminal(g.in) {
					argv = append(argv, "-t")
				}
				argv = append(argv, "--", sshHost(in.Name, s.user), "cd "+shellQuote(sessionPath(sessionDir(in, dir)))+" && exec bash")
			} else {
				argv = append(argv, "--", sshHost(in.Name, s.user))
			}
			run := exec.CommandContext(ctx, sshPath, argv...)
			run.Stdin, run.Stdout, run.Stderr = g.in, g.out, g.errOut
			if err := run.Run(); err != nil {
				return fmt.Errorf("ssh: %w", err)
			}
			return nil
		},
	}
	dirFlag(c, &dir)
	return c
}

// sessionPath is dir in a session: relative to its work directory.
func sessionPath(dir string) string {
	if path.IsAbs(dir) {
		return path.Clean(dir)
	}
	return path.Join(kube.SandboxWorkDir, dir)
}

// sshNoArgFlags is one or more of ssh's options that take no value, run
// together: -N, -fN, -NT.
var sshNoArgFlags = regexp.MustCompile(`^-[46AaCfGgKkMNnqsTtVvXxYy]+$`)

// onlyForwards: ssh's options say -N, no command (only forwarding).
func onlyForwards(opts []string) bool {
	for _, o := range opts {
		if sshNoArgFlags.MatchString(o) && strings.Contains(o, "N") {
			return true
		}
	}
	return false
}

func sandboxSSHConfigCmd(g *globals) *cobra.Command {
	var install bool
	c := &cobra.Command{
		Use:   "ssh-config",
		Short: "SSH settings that make ssh, scp, rsync and editors reach sessions",
		Long: `Print the ssh configuration that gives every session a host name:
SESSION.sbx for one of yours, SESSION.USER.sbx for USER's (an admin's
access). Then plain ssh works, and what runs over it: scp, rsync, sftp,
an editor's remote development over SSH, ssh -L.

  ssh refactor.sbx
  rsync -a refactor.sbx:my-repo/dist/ ./dist/
  ssh -N -L 3000:localhost:3000 refactor.sbx

--install writes it to a file of mcloud's and includes that at the top of
~/.ssh/config. Put it before any "Host *" section yourself otherwise:
ssh takes the first value it finds for a setting, and these say that
nothing of yours (your SSH agent, X11) is forwarded to a session.

Each connection is an exec of sshd in the session through the cluster's
API, as mcloud (this binary, logged in as you) makes it. A login lands in
the session's work directory (/work). The settings name mcloud's own key
and a host key file mcloud writes from what the session says before every
connection, so ssh asks nothing the first time and refuses any other key.

What comes out of a session is the agent's to choose: scp and rsync copy
links and git's own files (.git), which mcloud sandbox cp and pull leave
behind.`,
		Example: `  mcloud sandbox ssh-config --install
  mcloud sandbox ssh-config >> ~/.ssh/config.d/sandbox`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := sshDir()
			if err != nil {
				return err
			}
			if _, _, err := sshKey(dir); err != nil {
				return err
			}
			conf, err := g.sshConfig()
			if err != nil {
				return err
			}
			if !install {
				_, err := fmt.Fprint(g.out, conf)
				return err
			}
			file := filepath.Join(dir, "config")
			if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			userConfig := filepath.Join(home, ".ssh", "config")
			added, err := includeSSHConfig(userConfig, file)
			if err != nil {
				return err
			}
			u := g.UI()
			if added {
				u.Success("%s includes %s", userConfig, file)
			} else {
				u.Success("Updated %s, which %s includes", file, userConfig)
			}
			u.Hint("ssh SESSION%s", sshSuffix)
			return nil
		},
	}
	c.Flags().BoolVar(&install, "install", false, "write it, and include it at the top of ~/.ssh/config")
	return c
}

// includeSSHConfig puts an Include of file at the top of the ssh
// configuration userConfig, unless it's there already, and says whether it
// added it. At the top, it holds for every host, ahead of any Host *.
func includeSSHConfig(userConfig, file string) (bool, error) {
	quoted, err := sshConfigPath(file)
	if err != nil {
		return false, err
	}
	line := "Include " + quoted
	old, err := os.ReadFile(userConfig)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for _, l := range strings.Split(string(old), "\n") {
		if strings.TrimSpace(l) == line {
			return false, nil
		}
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(userConfig); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		return false, err
	}
	var b bytes.Buffer
	b.WriteString("# mcloud sandbox ssh-config --install: agent sessions, as SESSION" + sshSuffix + ".\n")
	b.WriteString(line + "\n\n")
	b.Write(old)
	tmp, err := os.CreateTemp(filepath.Dir(userConfig), ".config-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b.Bytes())
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), userConfig)
}

func sandboxSSHProxyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-proxy HOST",
		Short: "Carry one SSH connection to a session (ssh's ProxyCommand)",
		Long: `What ssh runs for a session's host name (mcloud sandbox ssh-config): it
writes the session's host key where ssh looks for it, then carries the
connection between its own input and output and an sshd in the session.`,
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// stdout is the connection: whatever mcloud has to say goes
			// to stderr, which ssh shows.
			conn := g.out
			g.out = g.errOut
			host := args[0]
			session, user, err := parseSSHHost(host)
			if err != nil {
				return err
			}
			dir, err := sshDir()
			if err != nil {
				return err
			}
			_, authorized, err := sshKey(dir)
			if err != nil {
				return err
			}
			s, err := g.sandbox(ctx, &sandboxFlags{user: user})
			if err != nil {
				return err
			}
			in, err := s.session(ctx, session)
			if err != nil {
				return err
			}
			if !in.Ready {
				return fmt.Errorf("session %s isn't up (%s): mcloud sandbox list", in.Name, sessionState(in))
			}
			err = s.k.ProxySSH(ctx, s.user, in.Name, authorized, func(key ssh.PublicKey) error {
				return writeKnownHost(dir, host, key)
			}, g.in, conn, g.errOut)
			if errors.Is(err, kube.ErrNoSSHServer) {
				return fmt.Errorf("session %s: %w", in.Name, err)
			}
			return err
		},
	}
}
