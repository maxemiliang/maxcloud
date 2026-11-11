package kube

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func hostKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// What a session says its host key is, it says as the agent there likes.
func TestParseHostKey(t *testing.T) {
	good := hostKeyLine(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, err := ssh.NewPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		line string
		ok   bool
	}{
		{good, true},
		{good + " a comment", true},
		{good + "\r", true},
		{"", false},
		{"not a key", false},
		{`command="evil" ` + good, false},
		{"@cert-authority * " + good, false},
		{good + "\n" + good, false},
		{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(rsaPub))), false},
		{"ssh-ed25519 AAAA", false},
	} {
		key, err := ParseHostKey([]byte(c.line))
		if (err == nil) != c.ok {
			t.Errorf("%q: %v", c.line, err)
		}
		if c.ok && key.Type() != ssh.KeyAlgoED25519 {
			t.Errorf("%q: %s", c.line, key.Type())
		}
	}
}

type exitErr int

func (e exitErr) Error() string   { return "exit" }
func (e exitErr) ExitStatus() int { return int(e) }

// serveBytes is a session that writes out, a byte at a time, then ends
// with err.
func serveBytes(out string, err error) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		for i := range len(out) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.Write([]byte{out[i]})
		}
		return err
	}
}

// The host key is taken before anything reaches ssh, and isn't passed on.
func TestProxySSH(t *testing.T) {
	line := hostKeyLine(t)
	var out bytes.Buffer
	var got ssh.PublicKey
	err := proxySSH(context.Background(), serveBytes(line+"\nSSH-2.0-OpenSSH_9.2\r\nrest", nil), func(k ssh.PublicKey) error {
		if out.Len() > 0 {
			t.Error("output before the host key")
		}
		got = k
		return nil
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || strings.TrimSpace(string(ssh.MarshalAuthorizedKey(got))) != line {
		t.Errorf("host key %v", got)
	}
	if out.String() != "SSH-2.0-OpenSSH_9.2\r\nrest" {
		t.Errorf("passed on %q", out.String())
	}
}

func TestProxySSHRefuses(t *testing.T) {
	line := hostKeyLine(t)
	for _, c := range []struct {
		name  string
		serve func(context.Context, io.Writer) error
		want  string
	}{
		{"not a key", serveBytes("SSH-2.0-OpenSSH_9.2\r\n", nil), "other than one ed25519 host key"},
		{"no line end", serveBytes(strings.Repeat("x", 2*maxHostKeyLine), nil), "no host key"},
		{"nothing", serveBytes("", nil), "didn't start"},
		{"no sshd", serveBytes("", exitErr(noSSHServer)), "no SSH server"},
		{"failed", serveBytes("", exitErr(1)), "exit"},
		{"key refused", serveBytes(line+"\nSSH-2.0-x\r\n", nil), "can't write"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			err := proxySSH(context.Background(), c.serve, func(ssh.PublicKey) error { return errors.New("can't write") }, &out)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %v, want %q", err, c.want)
			}
			if out.Len() > 0 {
				t.Errorf("passed on %q", out.String())
			}
		})
	}
	if err := proxySSH(context.Background(), serveBytes("", exitErr(noSSHServer)), nil, io.Discard); !errors.Is(err, ErrNoSSHServer) {
		t.Errorf("no sshd: %v", err)
	}
}
