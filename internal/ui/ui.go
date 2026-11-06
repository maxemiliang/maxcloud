// Package ui is how mcloud talks to you: colors, spinners, tables and
// prompts on a terminal, and plain lines everywhere else (pipes, CI,
// NO_COLOR, TERM=dumb, --plain), so logs and scripts read as before.
//
// Decoration goes to the error stream; what a command produces for
// pipes (an image, a URL, names) stays on the output stream.
package ui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

// UI writes to Out (results) and Err (progress, notes), and reads In.
type UI struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	fancy bool
	mu    sync.Mutex
	live  *Step // the step drawing its spinner, if any
}

// New decides once whether to be fancy: both streams are terminals, and
// nothing asks for plain output.
func New(in io.Reader, out, err io.Writer, plain bool) *UI {
	u := &UI{In: in, Out: out, Err: err}
	u.fancy = !plain && isTerminal(out) && isTerminal(err) &&
		os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" &&
		os.Getenv("CI") == "" && os.Getenv("GITHUB_ACTIONS") == "" && os.Getenv("MCLOUD_PLAIN") == ""
	return u
}

// Fancy reports whether colors and spinners are on.
func (u *UI) Fancy() bool { return u.fancy }

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// The palette: the terminal's own basic colors, so light and dark themes
// both read well.
var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	amber  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	dim    = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
	header = lipgloss.NewStyle().Faint(true).Bold(true)
)

func (u *UI) style(s lipgloss.Style, text string) string {
	if !u.fancy || text == "" {
		return text
	}
	return s.Render(text)
}

func (u *UI) Green(s string) string  { return u.style(green, s) }
func (u *UI) Amber(s string) string  { return u.style(amber, s) }
func (u *UI) Red(s string) string    { return u.style(red, s) }
func (u *UI) Accent(s string) string { return u.style(cyan, s) }
func (u *UI) Dim(s string) string    { return u.style(dim, s) }
func (u *UI) Bold(s string) string   { return u.style(bold, s) }

// Link is a URL, clickable in terminals that know OSC 8.
func (u *UI) Link(url string) string {
	if !u.fancy || url == "" {
		return url
	}
	return "\x1b]8;;" + url + "\x1b\\" + cyan.Render(url) + "\x1b]8;;\x1b\\"
}

// Mark is a one-character summary of a condition's status: True, False,
// anything else (waiting), or - when there's no such thing.
func (u *UI) Mark(status string, exists bool) string {
	switch {
	case !exists:
		return u.Dim("-")
	case status == "True":
		return u.Green("✓")
	case status == "False":
		return u.Red("✗")
	}
	return u.Amber("…")
}

// write prints text on w, keeping any live spinner below it.
func (u *UI) write(w io.Writer, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.live != nil {
		u.live.clear()
	}
	io.WriteString(w, text)
	if u.live != nil {
		u.live.draw()
	}
}

// Printf writes a line to Out.
func (u *UI) Printf(format string, a ...any) {
	u.write(u.Out, strings.TrimSuffix(fmt.Sprintf(format, a...), "\n")+"\n")
}

// Println writes lines to Out.
func (u *UI) Println(a ...any) { u.write(u.Out, fmt.Sprintln(a...)) }

// Success is a done thing: "✓ ...".
func (u *UI) Success(format string, a ...any) {
	u.write(u.Out, u.Green("✓")+" "+fmt.Sprintf(format, a...)+"\n")
}

// Warn is something to look at: "! ..." (plain: "warning: ...").
func (u *UI) Warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if u.fancy {
		u.write(u.Err, u.Amber("!")+" "+msg+"\n")
		return
	}
	u.write(u.Err, "warning: "+msg+"\n")
}

// Note is good to know: "· ..." (plain: "note: ...").
func (u *UI) Note(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if u.fancy {
		u.write(u.Err, u.Dim("· "+msg)+"\n")
		return
	}
	u.write(u.Err, "note: "+msg+"\n")
}

// Hint is an indented, quiet line under what came before.
func (u *UI) Hint(format string, a ...any) {
	u.write(u.Out, "  "+u.Dim(fmt.Sprintf(format, a...))+"\n")
}

// Error shows a command's error: its first line in red, the rest quiet
// (plain: "mcloud: ..." as always).
func (u *UI) Error(err error) {
	u.mu.Lock()
	live := u.live
	u.mu.Unlock()
	if live != nil { // a step the error interrupted
		live.Fail("")
	}
	if !u.fancy {
		fmt.Fprintln(u.Err, "mcloud:", err)
		return
	}
	first, rest, _ := strings.Cut(err.Error(), "\n")
	out := u.Red("✗ "+first) + "\n"
	for _, l := range strings.Split(rest, "\n") {
		if strings.TrimSpace(l) != "" {
			out += "  " + u.Dim(l) + "\n"
		}
	}
	u.write(u.Err, out)
}

// Heading is a section title.
func (u *UI) Heading(s string) {
	u.write(u.Out, "\n"+u.style(header, strings.ToUpper(s))+"\n")
}

// KV prints aligned "key  value" lines.
func (u *UI) KV(pairs [][2]string) {
	w := 0
	for _, p := range pairs {
		w = max(w, lipgloss.Width(p[0]))
	}
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(u.Dim(p[0]) + strings.Repeat(" ", w-lipgloss.Width(p[0])+2) + p[1] + "\n")
	}
	u.write(u.Out, b.String())
}

// Table prints rows in aligned columns under a header; cells may hold
// colors.
func (u *UI) Table(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], lipgloss.Width(c))
			}
		}
	}
	line := func(cells []string, style func(string) string) string {
		var b strings.Builder
		for i, c := range cells {
			if i == len(cells)-1 {
				b.WriteString(style(c))
				break
			}
			b.WriteString(style(c) + strings.Repeat(" ", widths[i]-lipgloss.Width(c)+2))
		}
		return strings.TrimRight(b.String(), " ") + "\n"
	}
	upper := make([]string, len(headers))
	for i, h := range headers {
		upper[i] = strings.ToUpper(h)
	}
	out := line(upper, func(s string) string { return u.style(header, s) })
	for _, r := range rows {
		out += line(r, func(s string) string { return s })
	}
	u.write(u.Out, out)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// stripANSI removes escape sequences (from other tools' output).
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// fit cuts s to n columns.
func fit(s string, n int) string {
	if n <= 1 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// width of the terminal on Err (80 if unknown).
func (u *UI) width() int {
	if f, ok := u.Err.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// Box is a framed panel with a title and aligned rows (plain: the rows).
func (u *UI) Box(title string, rows [][2]string) {
	w := 0
	for _, r := range rows {
		w = max(w, lipgloss.Width(r[0]))
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(u.Dim(r[0]) + strings.Repeat(" ", w-lipgloss.Width(r[0])+2) + r[1])
	}
	if !u.fancy {
		u.write(u.Out, title+"\n"+b.String()+"\n")
		return
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(0, 1).
		Render(u.Bold(title) + "\n\n" + b.String())
	u.write(u.Out, box+"\n")
}
