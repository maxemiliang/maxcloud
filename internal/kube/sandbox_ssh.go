package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// SSH into a session goes over the same exec as everything else: each
// connection is an exec of sshd -i in the session, as the session's user,
// with the SSH protocol on its stdin and stdout. Nothing listens in the
// session and nothing new reaches it: whoever may exec into it may SSH
// into it, and nobody else.

// SandboxSSHUser is who an SSH login in a session is: the image's user,
// the only one sshd can log in as without root.
const SandboxSSHUser = "node"

// noSSHServer is the exit status sshScript gives when the session's image
// has no sshd.
const noSSHServer = 42

// ErrNoSSHServer: the session's image has no SSH server (it predates SSH).
var ErrNoSSHServer = errors.New("this session's image has no SSH server: it is from before SSH came to sessions. Sessions started on an image with SSH have one")

// sshScript serves one SSH connection on stdin and stdout, logging in with
// the public key in $1. It keeps the host key and authorized_keys in
// ~/.ssh on the work volume, makes the host key the first time, and writes
// the host's public key as the first line, before sshd says anything.
//
// The settings are on sshd's command line, not in a file in the session,
// so the agent there can't change them for the next connection. No
// forwarding back to the client's side (agent, X11, -R, sockets), and
// -L only to the session's own loopback. sshd gives a login a fresh
// environment, so the session's (its token included, as a shell there has
// it) goes in with SetEnv: values with spaces or quotes would break the
// option and stay out. PATH goes in twice: a login shell's /etc/profile
// replaces it, and the image's profile.d puts MCLOUD_SSH_PATH back.
// StrictModes is off because the work volume, the user's home, is
// world-writable (an emptyDir).
const sshScript = `[ -x /usr/sbin/sshd ] || exit 42
d="$HOME/.ssh"
k="$d/ssh_host_ed25519_key"
mkdir -p "$d" && chmod 700 "$d" || exit 1
if [ ! -s "$k" ]; then
  ssh-keygen -q -t ed25519 -N '' -C '' -f "$k.$$" </dev/null >/dev/null && ln "$k.$$" "$k" 2>/dev/null
  rm -f "$k.$$" "$k.$$.pub"
fi
grep -qxF "$1" "$d/authorized_keys" 2>/dev/null || printf '%s\n' "$1" >>"$d/authorized_keys" || exit 1
ssh-keygen -y -f "$k" || exit 1
env=""
for v in $(env | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p'); do
  case "$v" in HOME|PWD|OLDPWD|SHLVL|_|TERM) continue ;; esac
  eval "x=\${$v}"
  case "$x" in "" | *[!A-Za-z0-9_./:@%+,=-]*) continue ;; esac
  env="$env $v=$x"
  [ "$v" = PATH ] && env="$env MCLOUD_SSH_PATH=$x"
done
exec /usr/sbin/sshd -i -e -f /dev/null \
  -o "HostKey $k" -o "AuthorizedKeysFile $d/authorized_keys" -o "AllowUsers $(id -un)" \
  -o "AuthenticationMethods publickey" -o "PasswordAuthentication no" -o "KbdInteractiveAuthentication no" \
  -o "UsePAM no" -o "StrictModes no" -o "PidFile none" -o "LogLevel ERROR" -o "UseDNS no" \
  -o "PrintMotd no" -o "PrintLastLog no" -o "PermitUserEnvironment no" \
  -o "AllowAgentForwarding no" -o "X11Forwarding no" -o "AllowStreamLocalForwarding no" -o "PermitTunnel no" \
  -o "AllowTcpForwarding local" -o "PermitOpen localhost:* 127.0.0.1:* [::1]:*" \
  -o "Subsystem sftp internal-sftp" \
  ${env:+-o "SetEnv$env"}`

// SSHCommand is the command that serves one SSH connection on its stdin
// and stdout in a session, logging in with authorizedKey (an
// authorized_keys line). Its first line out is the host's public key.
func SSHCommand(authorizedKey string) []string {
	return []string{"sh", "-c", sshScript, "sh", authorizedKey}
}

// HasSSHServer: user's session has an SSH server in its image.
func (c *Client) HasSSHServer(ctx context.Context, user, name string) (bool, error) {
	var stderr bytes.Buffer
	err := c.SessionExec(ctx, user, name, []string{"test", "-x", "/usr/sbin/sshd"}, nil, io.Discard, &stderr)
	if exitStatus(err) == 1 {
		return false, nil
	}
	if err != nil {
		return false, execError(err, &stderr)
	}
	return true, nil
}

// ProxySSH carries one SSH connection between stdin and stdout and sshd
// in user's session, logging in with authorizedKey. hostKey gets the
// session's host key before anything of sshd's reaches stdout, so it can
// be made the only key the client accepts.
func (c *Client) ProxySSH(ctx context.Context, user, name, authorizedKey string, hostKey func(ssh.PublicKey) error, stdin io.Reader, stdout, stderr io.Writer) error {
	return proxySSH(ctx, func(ctx context.Context, stdout io.Writer) error {
		// Never a terminal: the stream is SSH's, byte for byte.
		return c.SessionExec(ctx, user, name, SSHCommand(authorizedKey), struct{ io.Reader }{stdin}, stdout, stderr)
	}, hostKey, stdout)
}

// proxySSH runs serve (sshScript, wired to stdin) with its stdout going
// through a hostKeyWriter to stdout.
func proxySSH(ctx context.Context, serve func(ctx context.Context, stdout io.Writer) error, hostKey func(ssh.PublicKey) error, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &hostKeyWriter{w: stdout, hostKey: hostKey, cancel: cancel}
	err := serve(ctx, w)
	if w.err != nil {
		return w.err
	}
	if exitStatus(err) == noSSHServer {
		return ErrNoSSHServer
	}
	if err != nil {
		return err
	}
	if !w.passing {
		return errors.New("the session's SSH server didn't start")
	}
	return nil
}

// maxHostKeyLine bounds the host key line: an ed25519 key's is under 100
// bytes.
const maxHostKeyLine = 1024

// hostKeyWriter takes the first line written to it as the host's public
// key, hands it to hostKey, and passes everything after it on to w. A
// first line that isn't a host key stops the connection (cancel) before
// any of it gets to w.
type hostKeyWriter struct {
	w       io.Writer
	hostKey func(ssh.PublicKey) error
	cancel  func()
	line    []byte
	passing bool
	err     error
}

func (h *hostKeyWriter) Write(p []byte) (int, error) {
	n := len(p)
	if h.err != nil {
		return n, nil
	}
	if !h.passing {
		i := bytes.IndexByte(p, '\n')
		end := i
		if i < 0 {
			end = len(p)
		}
		h.line = append(h.line, p[:end]...)
		if len(h.line) > maxHostKeyLine {
			return n, h.fail(errors.New("the session sent no host key"))
		}
		if i < 0 {
			return n, nil
		}
		key, err := ParseHostKey(h.line)
		if err != nil {
			return n, h.fail(err)
		}
		if err := h.hostKey(key); err != nil {
			return n, h.fail(err)
		}
		h.passing, h.line, p = true, nil, p[i+1:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if _, err := h.w.Write(p); err != nil {
		return n, h.fail(err)
	}
	return n, nil
}

// fail ends the connection with err. The write itself succeeds: the
// stream's copier would only log an error, and the context ends it.
func (h *hostKeyWriter) fail(err error) error {
	h.err = err
	h.cancel()
	return nil
}

// ParseHostKey reads a session's host public key, as ssh-keygen -y prints
// it. It comes from the session, so it isn't trusted: one ed25519 key, and
// nothing else on the line but a comment.
func ParseHostKey(line []byte) (ssh.PublicKey, error) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	key, _, options, rest, err := ssh.ParseAuthorizedKey(line)
	if err != nil || len(options) > 0 || len(bytes.TrimSpace(rest)) > 0 || key.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("the session sent something other than one ed25519 host key, so no connection: %q", truncate(line, 80))
	}
	return key, nil
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// exitStatus is the exit status of a command that ended with err (-1: it
// didn't run, or didn't exit).
func exitStatus(err error) int {
	var e interface{ ExitStatus() int }
	if errors.As(err, &e) {
		return e.ExitStatus()
	}
	return -1
}
