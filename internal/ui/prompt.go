package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"golang.org/x/term"
)

// ErrAborted: you said no, or left a prompt.
var ErrAborted = errors.New("aborted")

// ErrNoTerminal: a question needs a terminal to be asked on.
var ErrNoTerminal = errors.New("not a terminal: pass --yes to go ahead")

// interactive: prompts can be shown (In and Err are terminals).
func (u *UI) interactive() bool { return isTerminal(u.In) && isTerminal(u.Err) }

func (u *UI) run(fields ...huh.Field) error {
	in, _ := u.In.(*os.File)
	f := huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huh.ThemeFunc(huh.ThemeBase16)).
		WithShowHelp(false).
		WithInput(in).
		WithOutput(u.Err)
	u.mu.Lock()
	if u.live != nil {
		u.live.clear()
	}
	u.mu.Unlock()
	err := f.Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	return err
}

// Confirm asks a yes/no question; no is the default.
func (u *UI) Confirm(question string) (bool, error) {
	if !u.interactive() {
		return false, ErrNoTerminal
	}
	if !u.fancy {
		fmt.Fprintf(u.Out, "%s [y/N] ", question)
		ans, err := bufio.NewReader(u.In).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		ans = strings.ToLower(strings.TrimSpace(ans))
		return ans == "y" || ans == "yes", nil
	}
	var ok bool
	if err := u.run(huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&ok)); err != nil {
		return false, err
	}
	return ok, nil
}

// TypeToConfirm makes you type want.
func (u *UI) TypeToConfirm(prompt, want string) (bool, error) {
	if !u.interactive() {
		return false, ErrNoTerminal
	}
	if !u.fancy {
		fmt.Fprintf(u.Out, "%s ", prompt)
		ans, err := bufio.NewReader(u.In).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		return strings.TrimSpace(ans) == want, nil
	}
	var ans string
	if err := u.run(huh.NewInput().Title(prompt).Placeholder(want).Value(&ans)); err != nil {
		return false, err
	}
	return strings.TrimSpace(ans) == want, nil
}

// Secret asks for a value without showing it.
func (u *UI) Secret(prompt string) (string, error) {
	f, ok := u.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", ErrNoTerminal
	}
	if !u.fancy {
		fmt.Fprintf(u.Err, "%s: ", prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(u.Err)
		return string(b), err
	}
	var v string
	err := u.run(huh.NewInput().Title(prompt).EchoMode(huh.EchoModePassword).Value(&v).
		Validate(func(s string) error {
			if s == "" {
				return errors.New("empty")
			}
			return nil
		}))
	return v, err
}

// Option is one choice for Select.
type Option struct{ Label, Value string }

// Select asks to pick one; def is preselected.
func (u *UI) Select(title string, options []Option, def string) (string, error) {
	if !u.interactive() || !u.fancy {
		return def, nil
	}
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value)
	}
	v := def
	err := u.run(huh.NewSelect[string]().Title(title).Options(opts...).Value(&v))
	return v, err
}

// Input asks for text; def is filled in.
func (u *UI) Input(title, def string, validate func(string) error) (string, error) {
	if !u.interactive() || !u.fancy {
		return def, nil
	}
	v := def
	in := huh.NewInput().Title(title).Value(&v)
	if validate != nil {
		in = in.Validate(validate)
	}
	err := u.run(in)
	return strings.TrimSpace(v), err
}
