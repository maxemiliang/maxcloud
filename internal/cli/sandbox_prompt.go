package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func sandboxPromptCmd(g *globals, f *sandboxFlags) *cobra.Command {
	var dir, mode string
	var cont bool
	c := &cobra.Command{
		Use:   "prompt [SESSION] PROMPT [-- ARGS...]",
		Short: "Give Claude Code in a session one task, and watch it work",
		Long: `Give Claude Code in a session one task. What it does shows as it goes, its
answer comes last (on stdout, so it can be piped), and the command ends
when the task does. SESSION can be left out when you have one session.

Nobody is there to answer questions, so --permission-mode says what Claude
Code may do unasked. The default, auto, lets it edit files and run
commands, with its own check on each action; bypassPermissions takes that
check away, and acceptEdits allows edits only. What follows -- goes to
claude as it is.

--continue carries on from the session's last conversation.`,
		Example: `  mcloud sandbox prompt 'Run the tests and fix what fails'
  mcloud sandbox prompt refactor 'Now commit that' --continue
  mcloud sandbox prompt 'Summarise this repository' > summary.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, rest := args, []string(nil)
			if at := cmd.ArgsLenAtDash(); at >= 0 {
				before, rest = args[:at], args[at:]
			}
			var session, prompt string
			switch len(before) {
			case 1:
				prompt = before[0]
			case 2:
				session, prompt = before[0], before[1]
			default:
				return fmt.Errorf("give the task as one argument, in quotes: %s [SESSION] 'what to do'", cmd.CommandPath())
			}
			if strings.TrimSpace(prompt) == "" {
				return errors.New("the task is empty")
			}
			ctx := cmd.Context()
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			in, err := s.session(ctx, session)
			if err != nil {
				return err
			}
			argv := []string{"claude", "-p", prompt, "--output-format", "stream-json", "--verbose", "--permission-mode", mode}
			if cont {
				argv = append(argv, "--continue")
			}
			argv = append(argv, rest...)
			r := &promptRender{out: g.out, progress: g.errOut, dim: g.UI().Dim}
			execErr := s.k.SessionExecIn(ctx, s.user, in.Name, sessionDir(in, dir), argv, nil, r, g.errOut)
			r.Close()
			if r.failed != "" {
				return fmt.Errorf("the task didn't finish: %s", r.failed)
			}
			return execErr
		},
	}
	dirFlag(c, &dir)
	c.Flags().StringVar(&mode, "permission-mode", "auto", "what Claude Code may do unasked: auto, acceptEdits, bypassPermissions, plan")
	c.Flags().BoolVarP(&cont, "continue", "c", false, "carry on from the session's last conversation")
	return c
}

// promptRender turns Claude Code's stream-json output (a JSON object per
// line) into something to read: what the agent says and which tools it
// uses go to progress as they come, and its final answer goes to out.
type promptRender struct {
	out, progress io.Writer
	dim           func(string) string
	buf           []byte
	// pending is the agent's latest text, held back until the next event
	// shows it isn't the final answer (which comes again as the result).
	pending string
	// failed is why the task ended in an error, if it did.
	failed string
}

type promptEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result     string `json:"result"`
	IsError    bool   `json:"is_error"`
	NumTurns   int    `json:"num_turns"`
	DurationMS int64  `json:"duration_ms"`
}

type promptBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

func (r *promptRender) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		r.line(r.buf[:i])
		r.buf = r.buf[i+1:]
	}
}

// Close shows what is left when the output ends.
func (r *promptRender) Close() {
	r.line(r.buf)
	r.buf = nil
	r.flush()
}

func (r *promptRender) flush() {
	if r.pending != "" {
		fmt.Fprintln(r.progress, r.pending)
		r.pending = ""
	}
}

func (r *promptRender) line(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var e promptEvent
	if err := json.Unmarshal(line, &e); err != nil || e.Type == "" {
		// Not an event: something claude or the shell said. Show it.
		r.flush()
		fmt.Fprintln(r.progress, string(line))
		return
	}
	switch e.Type {
	case "assistant":
		var blocks []promptBlock
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					r.flush()
					r.pending = strings.TrimSpace(b.Text)
				}
			case "tool_use":
				r.flush()
				fmt.Fprintln(r.progress, r.dim(strings.TrimSpace("→ "+b.Name+"  "+toolSummary(b.Input))))
			}
		}
	case "result":
		if e.IsError || e.Subtype != "success" {
			r.flush()
			r.failed = firstNonEmpty(strings.TrimSpace(e.Result), e.Subtype, "an error")
			return
		}
		answer := firstNonEmpty(strings.TrimSpace(e.Result), r.pending)
		r.pending = ""
		if answer != "" {
			fmt.Fprintln(r.out, answer)
		}
		took := (time.Duration(e.DurationMS) * time.Millisecond).Round(time.Second)
		fmt.Fprintln(r.progress, r.dim(fmt.Sprintf("done in %s, %d turns", took, e.NumTurns)))
	}
}

// toolSummary is the one thing worth showing of a tool's input: the
// command, the file, the pattern.
func toolSummary(input map[string]any) string {
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if v, ok := input[key].(string); ok && strings.TrimSpace(v) != "" {
			s := []rune(strings.Join(strings.Fields(v), " "))
			if len(s) > 100 {
				s = append(s[:99], '…')
			}
			return string(s)
		}
	}
	return ""
}
