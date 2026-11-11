package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

func sandboxForwardCmd(g *globals, f *sandboxFlags) *cobra.Command {
	c := &cobra.Command{
		Use:   "forward [SESSION] PORT[:SESSION-PORT]...",
		Short: "Open a session's ports on this machine",
		Long: `Open ports of a session on this machine, until you stop it with Ctrl-C:
a dev server running in the session, in your own browser. PORT is the port
here; it reaches the same port in the session, or SESSION-PORT. SESSION
can be left out when you have one session.

Only this machine can use the ports (they listen on localhost), and they
reach only what listens on the session's own localhost. It is SSH's local
forwarding, set up for you: mcloud sandbox ssh has the details.`,
		Example: `  mcloud sandbox forward 5173              # http://localhost:5173 is the session's 5173
  mcloud sandbox forward 8080:3000         # http://localhost:8080 is the session's 3000
  mcloud sandbox forward refactor 3000 9229`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if _, _, err := parseForward(args[0]); err != nil && !startsWithDigit(args[0]) {
				session, args = args[0], args[1:]
			}
			if len(args) == 0 {
				return errors.New("which port? mcloud sandbox forward [SESSION] PORT[:SESSION-PORT]")
			}
			forwards, err := parseForwards(args)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			to, err := g.sshTo(ctx, f, session)
			if err != nil {
				return err
			}
			argv := append(to.options, forwardOptions(forwards)...)
			argv = append(argv, "--", to.host)
			u := g.UI()
			u.Printf("Forwarding to %s. Ctrl-C to stop.", u.Bold(to.session.Name))
			for _, fw := range forwards {
				if fw.local == fw.session {
					u.Printf("  http://localhost:%d", fw.local)
				} else {
					u.Printf("  http://localhost:%d  %s", fw.local, u.Dim(fmt.Sprintf("(port %d in the session)", fw.session)))
				}
			}
			run := exec.CommandContext(ctx, to.ssh, argv...)
			run.Stdin, run.Stdout, run.Stderr = nil, g.out, g.errOut
			// Ctrl-C reaches ssh by itself, as part of this terminal's
			// foreground: it isn't a failure.
			if err := run.Run(); err != nil && ctx.Err() == nil {
				return fmt.Errorf("the forward ended: %w", err)
			}
			return nil
		},
	}
	return c
}

// forward is one port of this machine that reaches a port of a session.
type forward struct{ local, session int }

func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// parseForward reads PORT or PORT:SESSION-PORT.
func parseForward(spec string) (local, session int, err error) {
	l, s, both := strings.Cut(spec, ":")
	if local, err = parsePort(l); err != nil {
		return 0, 0, fmt.Errorf("%q: %w", spec, err)
	}
	if !both {
		return local, local, nil
	}
	if session, err = parsePort(s); err != nil {
		return 0, 0, fmt.Errorf("%q: %w", spec, err)
	}
	return local, session, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || strings.TrimLeft(s, "0123456789") != "" {
		return 0, errors.New("a port is a number from 1 to 65535, as PORT or PORT:SESSION-PORT")
	}
	return n, nil
}

func parseForwards(specs []string) ([]forward, error) {
	seen := map[int]bool{}
	out := make([]forward, 0, len(specs))
	for _, spec := range specs {
		local, session, err := parseForward(spec)
		if err != nil {
			return nil, err
		}
		if seen[local] {
			return nil, fmt.Errorf("port %d is here twice", local)
		}
		seen[local] = true
		out = append(out, forward{local, session})
	}
	return out, nil
}

// forwardOptions are ssh's options for forwards and nothing else: no
// command, and no session at all if a port here is taken. The ports
// listen on this machine's loopback only, whatever the user's own ssh
// configuration says about GatewayPorts.
func forwardOptions(forwards []forward) []string {
	argv := []string{"-N", "-T", "-o", "ExitOnForwardFailure yes", "-o", "GatewayPorts no"}
	for _, fw := range forwards {
		argv = append(argv, "-L", fmt.Sprintf("%d:localhost:%d", fw.local, fw.session))
	}
	return argv
}
