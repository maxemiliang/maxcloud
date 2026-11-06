package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
)

// PortForward forwards 127.0.0.1:localPort (0: any free port) to port of
// pod, until ctx is done. Returns the local port once it listens.
func (c *Client) PortForward(ctx context.Context, ns, pod string, localPort, port int) (int, error) {
	if c.config == nil {
		return 0, errors.New("no cluster connection")
	}
	transport, upgrader, err := spdy.RoundTripperFor(c.config)
	if err != nil {
		return 0, err
	}
	u := c.Core.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, (*url.URL)(u))
	ready := make(chan struct{})
	stop := make(chan struct{})
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("%d:%d", localPort, port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return 0, err
	}
	errc := make(chan error, 1)
	go func() { errc <- fw.ForwardPorts() }()
	go func() { <-ctx.Done(); close(stop) }()
	select {
	case <-ready:
	case err := <-errc:
		return 0, fmt.Errorf("port-forward to %s/%s: %w", ns, pod, err)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		return 0, fmt.Errorf("port-forward to %s/%s: no local port", ns, pod)
	}
	return int(ports[0].Local), nil
}

// DatabaseURL is a postgres:// URL for a CloudNativePG cluster's app user,
// reached on 127.0.0.1:localPort (a port-forward), from its Secret
// <cluster>-app.
func (c *Client) DatabaseURL(ctx context.Context, ns, cluster string, localPort int) (string, error) {
	s, err := c.Core.CoreV1().Secrets(ns).Get(ctx, cluster+"-app", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("the database's credentials (Secret %s/%s-app): %w", ns, cluster, err)
	}
	db := string(s.Data["dbname"])
	if db == "" {
		db = appDatabase
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(string(s.Data["username"]), string(s.Data["password"])),
		Host:     fmt.Sprintf("127.0.0.1:%d", localPort),
		Path:     "/" + db,
		RawQuery: "sslmode=require",
	}
	return u.String(), nil
}

// ProxyDatabase port-forwards to the primary of a CloudNativePG cluster
// until ctx is done, and returns the URL to reach it with.
func (c *Client) ProxyDatabase(ctx context.Context, ns, cluster string, localPort int) (string, error) {
	pod, err := c.PrimaryPod(ctx, ns, cluster)
	if err != nil {
		return "", err
	}
	port, err := c.PortForward(ctx, ns, pod, localPort, 5432)
	if err != nil {
		return "", err
	}
	return c.DatabaseURL(ctx, ns, cluster, port)
}

// Exec runs cmd in a container, wired to stdin/stdout/stderr. On a
// terminal it's interactive (a TTY, raw mode, window size follows).
func (c *Client) Exec(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if c.config == nil {
		return errors.New("no cluster connection")
	}
	in, isFile := stdin.(*os.File)
	tty := isFile && term.IsTerminal(int(in.Fd()))
	req := c.Core.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    !tty,
			TTY:       tty,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(c.config, "POST", req.URL())
	if err != nil {
		return err
	}
	opts := remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: tty}
	if !tty {
		opts.Stderr = stderr
	} else {
		state, err := term.MakeRaw(int(in.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(in.Fd()), state)
		sizes := &termSizes{fd: int(in.Fd()), ch: make(chan *remotecommand.TerminalSize, 1)}
		sizes.push()
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-winch:
					sizes.push()
				}
			}
		}()
		opts.TerminalSizeQueue = sizes
	}
	return exec.StreamWithContext(ctx, opts)
}

type termSizes struct {
	fd int
	ch chan *remotecommand.TerminalSize
}

func (t *termSizes) push() {
	w, h, err := term.GetSize(t.fd)
	if err != nil {
		return
	}
	select {
	case t.ch <- &remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}:
	default:
	}
}

func (t *termSizes) Next() *remotecommand.TerminalSize { return <-t.ch }
