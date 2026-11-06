package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Step is one stage of a longer command. On a terminal it's a spinner with
// the time so far, a status after its title, and (with a tail) the last
// lines of what it's running, which disappear when it's done; it ends as
// "✓ title  12s" or "✗ title". Plain, it's "title ...", its status and log
// lines indented, and "  done (12s)".
type Step struct {
	u      *UI
	title  string
	status string
	tail   []string
	keep   int
	start  time.Time
	frame  int
	drawn  int // lines on screen
	stop   chan struct{}
	ended  sync.Once
	ticker sync.WaitGroup
}

// Step starts a step that shows no output of its own.
func (u *UI) Step(title string) *Step { return u.StepTail(title, 0) }

// StepTail starts a step that shows the last n lines it's given (Log, or
// Writer) while it runs.
func (u *UI) StepTail(title string, n int) *Step {
	s := &Step{u: u, title: title, keep: n, start: time.Now(), stop: make(chan struct{})}
	if !u.fancy {
		u.write(u.Out, title+" ...\n")
		return s
	}
	u.mu.Lock()
	if u.live != nil { // one live step at a time: the older one stays as it is
		u.live.clear()
		u.live.drawn = 0
	}
	u.live = s
	s.draw()
	u.mu.Unlock()
	s.ticker.Add(1)
	go func() {
		defer s.ticker.Done()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				u.mu.Lock()
				if u.live == s {
					s.frame++
					s.clear()
					s.draw()
				}
				u.mu.Unlock()
			}
		}
	}()
	return s
}

// Status shows what the step is at, after its title (plain: a line when
// it changes).
func (s *Step) Status(format string, a ...any) {
	st := fmt.Sprintf(format, a...)
	if !s.u.fancy {
		if st != s.status && st != "" {
			s.u.write(s.u.Out, "  "+st+"\n")
		}
		s.status = st
		return
	}
	s.u.mu.Lock()
	s.status = st
	s.u.mu.Unlock()
}

// Log is a line of output under the step: kept in its tail on a terminal
// (or printed above it, quietly, if it has none), printed indented plain.
func (s *Step) Log(line string) {
	line = strings.TrimRight(stripANSI(line), " \t")
	if strings.TrimSpace(line) == "" {
		return
	}
	if !s.u.fancy {
		s.u.write(s.u.Out, "  "+line+"\n")
		return
	}
	if s.keep == 0 {
		s.u.write(s.u.Out, "  "+s.u.Dim(line)+"\n")
		return
	}
	s.u.mu.Lock()
	s.tail = append(s.tail, line)
	if len(s.tail) > 200 {
		s.tail = s.tail[len(s.tail)-200:]
	}
	s.u.mu.Unlock()
}

// Writer is an io.Writer whose lines go to Log (carriage returns too, as
// progress bars use them).
func (s *Step) Writer() io.Writer { return &lineWriter{s: s} }

type lineWriter struct {
	s   *Step
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexAny(string(w.buf), "\n\r")
		if i < 0 {
			return len(p), nil
		}
		w.s.Log(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
}

// Done ends the step well; msg (optional) replaces its title.
func (s *Step) Done(format string, a ...any) {
	s.end(s.u.Green("✓"), format, a, false)
}

// Fail ends the step badly; the last lines of its output stay on screen.
func (s *Step) Fail(format string, a ...any) {
	s.end(s.u.Red("✗"), format, a, true)
}

// Skip ends the step as not needed.
func (s *Step) Skip(format string, a ...any) {
	s.end(s.u.Dim("–"), format, a, false)
}

func (s *Step) end(mark, format string, a []any, failed bool) {
	s.ended.Do(func() {
		close(s.stop)
		s.ticker.Wait()
		msg := s.title
		if format != "" {
			msg = fmt.Sprintf(format, a...)
		}
		took := time.Since(s.start).Round(100 * time.Millisecond)
		if !s.u.fancy {
			switch {
			case failed:
				s.u.write(s.u.Out, "  failed: "+msg+"\n")
			case format != "":
				s.u.write(s.u.Out, "  "+msg+" ("+took.String()+")\n")
			default:
				s.u.write(s.u.Out, "  done ("+took.String()+")\n")
			}
			return
		}
		s.u.mu.Lock()
		defer s.u.mu.Unlock()
		other := s.u.live
		if other == s {
			s.clear()
			s.u.live, other = nil, nil
		} else if other != nil {
			other.clear()
		}
		defer func() {
			if other != nil {
				other.draw()
			}
		}()
		out := mark + " " + msg + "  " + s.u.Dim(short(took)) + "\n"
		if failed && len(s.tail) > 0 {
			from := max(0, len(s.tail)-20)
			for _, l := range s.tail[from:] {
				out += "    " + s.u.Dim(fit(l, s.u.width()-4)) + "\n"
			}
		}
		io.WriteString(s.u.Err, out)
	})
}

// short is a duration as people say it: 0.4s, 12s, 2m31s.
func short(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// draw writes the step's lines (spinner, tail) at the cursor; the cursor
// ends at the end of the last line. Called with u.mu held.
func (s *Step) draw() {
	w := s.u.width()
	line := s.u.Amber(frames[s.frame%len(frames)]) + " " + fit(s.title, w/2)
	if s.status != "" {
		line += "  " + s.u.Dim(fit(s.status, w-len([]rune(s.title))-16))
	}
	line += "  " + s.u.Dim(short(time.Since(s.start)))
	lines := []string{line}
	if s.keep > 0 {
		from := max(0, len(s.tail)-s.keep)
		for _, l := range s.tail[from:] {
			lines = append(lines, "    "+s.u.Dim(fit(l, w-5)))
		}
	}
	io.WriteString(s.u.Err, strings.Join(lines, "\n"))
	s.drawn = len(lines)
}

// clear erases what draw wrote and puts the cursor where it started.
// Called with u.mu held.
func (s *Step) clear() {
	if s.drawn == 0 {
		return
	}
	io.WriteString(s.u.Err, "\r\x1b[2K"+strings.Repeat("\x1b[1A\x1b[2K", s.drawn-1))
	s.drawn = 0
}
