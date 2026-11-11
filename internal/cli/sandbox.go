package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

func sandboxCmd(g *globals) *cobra.Command {
	var f sandboxFlags
	c := &cobra.Command{
		Use:   "sandbox",
		Short: "Agent sessions: Claude Code in a sandbox in the cluster",
		Long: `A session is Claude Code in a pod of its own in the cluster, under gVisor,
logged in with your Claude subscription. It can reach the internet, so it
can clone and install what it needs, and nothing in the cluster. Your own
files go in and out with mcloud sandbox cp.

Your sessions run in a sandbox that is yours alone: the first command here
makes it. It holds your Claude token (mcloud sandbox token set), which
only your own sessions read, and it limits how many sessions you can have
running.

  mcloud sandbox token set      # once: the token claude setup-token prints
  mcloud sandbox start          # a new session
  mcloud sandbox claude         # Claude Code in it
  mcloud sandbox stop NAME      # end it; its files go with it`,
	}
	c.PersistentFlags().StringVar(&f.user, "user", "", "whose sandbox (default: yours; someone else's takes an admin)")
	c.AddCommand(sandboxStartCmd(g, &f), sandboxListCmd(g, &f), sandboxClaudeCmd(g, &f), sandboxShellCmd(g, &f),
		sandboxCpCmd(g, &f), sandboxStopCmd(g, &f), sandboxTokenCmd(g, &f), sandboxRemoveCmd(g, &f))
	return c
}

type sandboxFlags struct {
	user string
}

// sandbox is the cluster and whose sandbox a command is about.
type sandbox struct {
	k    *kube.Client
	user string
	// other: named with --user, so maybe not the caller's own.
	other bool
}

func (g *globals) sandbox(ctx context.Context, f *sandboxFlags) (*sandbox, error) {
	k, err := g.kube()
	if err != nil {
		return nil, err
	}
	user := f.user
	if user == "" {
		if user, err = k.SandboxUser(ctx); err != nil {
			return nil, err
		}
	} else if err := kube.CheckSandboxUser(user); err != nil {
		return nil, err
	}
	return &sandbox{k: k, user: user, other: f.user != ""}, nil
}

// existing is the sandbox for commands that only make sense once there is
// one.
func (g *globals) existingSandbox(ctx context.Context, f *sandboxFlags) (*sandbox, error) {
	s, err := g.sandbox(ctx, f)
	if err != nil {
		return nil, err
	}
	has, err := s.k.HasSandbox(ctx, s.user)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, fmt.Errorf("%s has no sandbox yet: mcloud sandbox start makes one", s.user)
	}
	return s, nil
}

// ensure makes the sandbox if there's none, and waits until it can be
// used.
func (g *globals) ensureSandbox(ctx context.Context, s *sandbox) error {
	made, err := s.k.EnsureSandbox(ctx, s.user)
	if err != nil {
		return err
	}
	ready, err := s.k.SandboxReady(ctx, s.user)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	// Someone else's sandbox that was there already: waiting won't let a
	// user who isn't an admin in.
	if s.other && !made {
		return fmt.Errorf("the sandbox of %s isn't yours to use (an admin can)", s.user)
	}
	title := "Waiting for the sandbox of " + s.user
	if made {
		title = "Making a sandbox for " + s.user
	}
	step := g.UI().Step(title)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.k.WaitSandbox(ctx, s.user); err != nil {
		step.Fail("")
		return err
	}
	step.Done("Sandbox ready %s", g.UI().Dim("(namespace "+kube.SandboxNamespace(s.user)+")"))
	return nil
}

// session picks the session a command is about: the one named, or the only
// one there is.
func (s *sandbox) session(ctx context.Context, name string) (string, error) {
	if name != "" {
		if _, err := s.k.GetSession(ctx, s.user, name); err != nil {
			if errors.Is(err, kube.ErrNoSession) {
				return "", fmt.Errorf("no session %s: mcloud sandbox list shows what's running", name)
			}
			return "", err
		}
		return name, nil
	}
	all, err := s.k.Sessions(ctx, s.user)
	if err != nil {
		return "", err
	}
	switch len(all) {
	case 0:
		return "", errors.New("no session is running: mcloud sandbox start")
	case 1:
		return all[0].Name, nil
	}
	names := make([]string, len(all))
	for i, a := range all {
		names[i] = a.Name
	}
	return "", fmt.Errorf("which session? %s", strings.Join(names, ", "))
}

// tokenHelp says where a token comes from.
const tokenHelp = "Run `claude setup-token` on your own machine (logged in to your Claude subscription) and give what it prints: a token for model requests only, good for a year."

func readSandboxToken(g *globals) (string, error) {
	v, err := readSecretValue(g, "your Claude token")
	if err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	if strings.ContainsAny(v, " \t\r\n") {
		return "", errors.New("that isn't a token: it has spaces or line breaks in it")
	}
	return v, nil
}

func sandboxStartCmd(g *globals, f *sandboxFlags) *cobra.Command {
	var ttl time.Duration
	c := &cobra.Command{
		Use:   "start [NAME]",
		Short: "Start a session",
		Long: `Start a session: a pod with Claude Code in it, named NAME (or
session-xxxxx). The first time, this also makes your sandbox, and asks for
your Claude token if you haven't set one.

A session runs until you stop it, or for as long as --for says. Its files
(/work) exist as long as it does.`,
		Example: `  mcloud sandbox start
  mcloud sandbox start refactor --for 8h`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := ""
			if len(args) == 1 {
				name = args[0]
				if err := kube.CheckSessionName(name); err != nil {
					return err
				}
			}
			if ttl < 0 {
				return errors.New("--for: a session can't end in the past")
			}
			s, err := g.sandbox(ctx, f)
			if err != nil {
				return err
			}
			if err := g.ensureSandbox(ctx, s); err != nil {
				return err
			}
			u := g.UI()
			has, err := s.k.HasSandboxToken(ctx, s.user)
			if err != nil {
				return err
			}
			if !has {
				if in, ok := g.in.(*os.File); !ok || !term.IsTerminal(int(in.Fd())) {
					return errors.New("no Claude token yet: mcloud sandbox token set")
				}
				u.Note("No Claude token yet. %s", tokenHelp)
				token, err := readSandboxToken(g)
				if err != nil {
					return err
				}
				if err := s.k.SetSandboxToken(ctx, s.user, token); err != nil {
					return err
				}
				u.Success("Token set")
			}
			var until time.Time
			if ttl > 0 {
				until = time.Now().Add(ttl)
			}
			name, err = s.k.StartSession(ctx, s.user, name, until)
			if err != nil {
				return err
			}
			step := u.Step("Starting session " + name)
			wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := s.k.WaitSession(wait, s.user, name, func(w string) { step.Status("%s", w) }); err != nil {
				step.Fail("")
				// A session that can't start would only sit in the list.
				if errors.Is(err, kube.ErrSessionCantStart) {
					if stopErr := s.k.StopSession(ctx, s.user, name); stopErr != nil {
						u.Warn("Session %s is still listed: mcloud sandbox stop %s", name, name)
					}
				}
				return err
			}
			if until.IsZero() {
				step.Done("Session %s is up", u.Bold(name))
			} else {
				step.Done("Session %s is up, until %s", u.Bold(name), until.Local().Format("15:04 on 2 Jan"))
			}
			u.Hint("mcloud sandbox claude %s", name)
			return nil
		},
	}
	c.Flags().DurationVar(&ttl, "for", 0, "end the session after this long, e.g. 8h (default: when you stop it)")
	return c
}

func sandboxListCmd(g *globals, f *sandboxFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Your sessions",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			u := g.UI()
			all, err := s.k.Sessions(ctx, s.user)
			if err != nil {
				return err
			}
			if len(all) == 0 {
				u.Printf("No sessions. %s", u.Dim("mcloud sandbox start"))
			} else {
				rows := make([][]string, len(all))
				for i, a := range all {
					state := u.Green("up")
					if !a.Ready {
						state = u.Amber(sessionState(a))
					}
					ends := "-"
					if !a.Until.IsZero() {
						ends = a.Until.Local().Format("15:04 on 2 Jan")
					}
					rows[i] = []string{a.Name, state, sinceShort(a.Created), ends}
				}
				u.Table([]string{"Name", "State", "Age", "Ends"}, rows)
			}
			has, err := s.k.HasSandboxToken(ctx, s.user)
			if err != nil {
				return err
			}
			if !has {
				u.Warn("No Claude token set: sessions can't start. mcloud sandbox token set")
			}
			return nil
		},
	}
}

// sessionState names what a session that isn't up is doing.
func sessionState(s kube.Session) string {
	switch s.Reason {
	case "":
		return "starting"
	case "ReconcilerError":
		return "refused"
	case "SandboxExpired":
		return "ended"
	}
	return s.Reason
}

// sinceShort is how long ago t was, in one unit.
func sinceShort(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// sessionAndArgs splits a command's arguments at "--": an optional session
// before it, what to run after it.
func sessionAndArgs(cmd *cobra.Command, args []string) (session string, rest []string, err error) {
	before := args
	if at := cmd.ArgsLenAtDash(); at >= 0 {
		before, rest = args[:at], args[at:]
	}
	switch len(before) {
	case 0:
	case 1:
		session = before[0]
	default:
		return "", nil, fmt.Errorf("one session at a time: put what to run after --, as in %s NAME -- ...", cmd.CommandPath())
	}
	return session, rest, nil
}

// execIn runs argv in a session, wired to this terminal.
func (g *globals) execIn(cmd *cobra.Command, f *sandboxFlags, session string, argv []string) error {
	ctx := cmd.Context()
	s, err := g.existingSandbox(ctx, f)
	if err != nil {
		return err
	}
	name, err := s.session(ctx, session)
	if err != nil {
		return err
	}
	return s.k.SessionExec(ctx, s.user, name, argv, g.in, g.out, g.errOut)
}

func sandboxClaudeCmd(g *globals, f *sandboxFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "claude [SESSION] [-- ARGS...]",
		Short: "Claude Code in a session",
		Long: `Run Claude Code in a session, in its work directory (/work). On a terminal
it's the interactive interface; what follows -- goes to claude as it is.
SESSION can be left out when you have one session.

Claude Code keeps its conversations in the session, so claude --resume and
--continue work for as long as the session does.`,
		Example: `  mcloud sandbox claude
  mcloud sandbox claude refactor -- --continue
  mcloud sandbox claude -- -p 'Write fizzbuzz.js and run it' --permission-mode acceptEdits`,
		RunE: func(cmd *cobra.Command, args []string) error {
			session, rest, err := sessionAndArgs(cmd, args)
			if err != nil {
				return err
			}
			return g.execIn(cmd, f, session, append([]string{"claude"}, rest...))
		},
	}
}

func sandboxShellCmd(g *globals, f *sandboxFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "shell [SESSION] [-- COMMAND...]",
		Short: "A shell in a session, or one command",
		Long: `Open a shell in a session, in its work directory (/work), or run what
follows -- there. SESSION can be left out when you have one session.`,
		Example: `  mcloud sandbox shell
  mcloud sandbox shell refactor -- git status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			session, rest, err := sessionAndArgs(cmd, args)
			if err != nil {
				return err
			}
			if len(rest) == 0 {
				rest = []string{"bash"}
			}
			return g.execIn(cmd, f, session, rest)
		},
	}
}

// splitSessionPath reads SESSION:PATH. A Windows drive (C:\x) or anything
// with a slash before the colon is a local path.
func splitSessionPath(arg string) (session, path string, remote bool) {
	i := strings.IndexByte(arg, ':')
	if i <= 0 || strings.ContainsAny(arg[:i], `/\.`) || (i == 1 && runtime.GOOS == "windows") {
		return "", arg, false
	}
	return arg[:i], arg[i+1:], true
}

func sandboxCpCmd(g *globals, f *sandboxFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "cp SOURCE DEST",
		Short: "Copy files into or out of a session",
		Long: `Copy a file or a directory between this machine and a session. One of
SOURCE and DEST is SESSION:PATH, with PATH relative to the session's work
directory (/work). Like cp: the copy goes into DEST if that is a directory,
and is named DEST otherwise.

Coming out of a session, only files and directories are copied: links and
special files are left out, and nothing is written outside DEST.`,
		Example: `  mcloud sandbox cp ./my-repo refactor:my-repo
  mcloud sandbox cp refactor:my-repo ./my-repo-after
  mcloud sandbox cp notes.md refactor:`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			srcSession, src, srcRemote := splitSessionPath(args[0])
			dstSession, dst, dstRemote := splitSessionPath(args[1])
			if srcRemote == dstRemote {
				return errors.New("one of SOURCE and DEST is in a session (SESSION:PATH), the other on this machine")
			}
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			u := g.UI()
			if dstRemote {
				name, err := s.session(ctx, dstSession)
				if err != nil {
					return err
				}
				step := u.Step(fmt.Sprintf("Copying %s to %s", src, name))
				if err := s.k.CopyToSession(ctx, s.user, name, src, dst); err != nil {
					step.Fail("")
					return err
				}
				step.Done("Copied %s to %s:%s", src, name, dst)
				return nil
			}
			name, err := s.session(ctx, srcSession)
			if err != nil {
				return err
			}
			step := u.Step(fmt.Sprintf("Copying %s:%s", name, src))
			target, skipped, err := s.k.CopyFromSession(ctx, s.user, name, src, dst)
			if err != nil {
				step.Fail("")
				return err
			}
			step.Done("Copied %s:%s to %s", name, src, target)
			if skipped > 0 {
				u.Note("Left out %d links or special files.", skipped)
			}
			return nil
		},
	}
}

func sandboxStopCmd(g *globals, f *sandboxFlags) *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "stop SESSION...",
		Short: "End sessions; their files go with them",
		Long: `End sessions. A session's pod and everything in its work directory are
deleted: copy out what you want to keep first (mcloud sandbox cp).`,
		Example: `  mcloud sandbox stop refactor
  mcloud sandbox stop --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if all == (len(args) > 0) {
				return errors.New("name the sessions to stop, or --all")
			}
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			if all {
				sessions, err := s.k.Sessions(ctx, s.user)
				if err != nil {
					return err
				}
				for _, a := range sessions {
					args = append(args, a.Name)
				}
			}
			u := g.UI()
			if len(args) == 0 {
				u.Printf("No sessions.")
				return nil
			}
			var failed error
			for _, name := range args {
				if err := s.k.StopSession(ctx, s.user, name); err != nil {
					if errors.Is(err, kube.ErrNoSession) {
						err = fmt.Errorf("no session %s", name)
					}
					u.Error(err)
					failed = errors.New("not every session was stopped")
					continue
				}
				u.Success("Stopped %s", name)
			}
			return failed
		},
	}
	c.Flags().BoolVar(&all, "all", false, "every session of yours")
	return c
}

func sandboxTokenCmd(g *globals, f *sandboxFlags) *cobra.Command {
	c := &cobra.Command{
		Use:   "token",
		Short: "The Claude token your sessions log in with",
		Long: `Sessions log in to Claude with your subscription, through a token that
you set once and renew once a year. ` + tokenHelp + `

The token is kept in your sandbox in the cluster, where only your sessions
(and the cluster's admins) can read it. It is in no repository. A session
reads it when it starts: after a change, running sessions keep the old one.`,
	}
	set := &cobra.Command{
		Use:   "set",
		Short: "Set the token; it comes from stdin (or a hidden prompt)",
		Example: `  mcloud sandbox token set                 # asks for it
  op read op://vault/claude/token | mcloud sandbox token set`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := g.sandbox(ctx, f)
			if err != nil {
				return err
			}
			token, err := readSandboxToken(g)
			if err != nil {
				return err
			}
			if err := g.ensureSandbox(ctx, s); err != nil {
				return err
			}
			if err := s.k.SetSandboxToken(ctx, s.user, token); err != nil {
				return err
			}
			g.UI().Success("Token set. New sessions use it.")
			return nil
		},
	}
	unset := &cobra.Command{
		Use:   "unset",
		Short: "Remove the token; new sessions can't start without one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			had, err := s.k.UnsetSandboxToken(ctx, s.user)
			if err != nil {
				return err
			}
			if !had {
				g.UI().Printf("No token was set.")
				return nil
			}
			g.UI().Success("Token removed. Running sessions keep it until they stop.")
			return nil
		},
	}
	c.AddCommand(set, unset)
	return c
}

func sandboxRemoveCmd(g *globals, f *sandboxFlags) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "remove",
		Short: "Delete your sandbox: its sessions, their files and your token",
		Long: `Delete your sandbox: every session in it, their files, and your Claude
token. mcloud sandbox start makes a new, empty one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			if !yes {
				ok, err := g.typeToConfirm(fmt.Sprintf("This deletes every session of %s, their files and the token. Type the user's name (%s) to go on:", s.user, s.user), s.user)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("not removed")
				}
			}
			if err := s.k.RemoveSandbox(ctx, s.user); err != nil {
				return err
			}
			g.UI().Success("Removed the sandbox of %s", s.user)
			return nil
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "don't ask")
	return c
}
