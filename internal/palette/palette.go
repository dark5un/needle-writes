// Package palette shows the M3 action picker. v0 renders it with fuzzel
// in dmenu mode — the plan's sanctioned v0 palette. The no-shell-out
// rule is about the Wayland protocols (selection, clipboard, injection),
// which this binary speaks in-process; the picker is a plain UI helper.
package palette

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Options tune the palette's rendered UX (UX/design review round 3):
// a prompt that states the operation, a width that fits the longest
// item without truncation, one line per item (fuzzel draws no scroll
// indicator), and selection colors strong enough to see (fuzzel's
// built-in defaults measure ~1.06:1 against its own background).
type Options struct {
	Prompt              string
	SelectionBackground string // "#rrggbb[aa]"; "" = fuzzel default
	SelectionText       string // "" = fuzzel default
	WidthChars          int    // <=0: fuzzel default
	Lines               int    // <=0: derived from the item count
}

// Fuzzel shows items in a fuzzel palette and returns the chosen line.
// An empty line with a nil error means the user cancelled (Esc).
func Fuzzel(ctx context.Context, opts Options, items []string) (string, error) {
	return chooseWith(ctx, execRunner, opts, items)
}

// fuzzelArgs builds the dmenu-mode invocation.
func fuzzelArgs(opts Options, items int) []string {
	args := []string{"-d", "-p", opts.Prompt}
	if opts.WidthChars > 0 {
		args = append(args, "-w", strconv.Itoa(opts.WidthChars))
	}
	lines := opts.Lines
	if lines <= 0 {
		lines = items // every action visible
	}
	args = append(args, "-l", strconv.Itoa(lines))
	if bg := fuzzelColor(opts.SelectionBackground); bg != "" {
		args = append(args, "-s", bg)
	}
	if tx := fuzzelColor(opts.SelectionText); tx != "" {
		args = append(args, "-S", tx)
	}
	return args
}

// fuzzelColor normalizes a CSS-style hex color to fuzzel's expected
// rrggbbaa form (no '#', alpha required): "#264777" -> "264777ff".
// Anything unrecognized is dropped (fuzzel's own default wins over a
// rejected argument — fuzzel exits 1 on "invalid color").
func fuzzelColor(c string) string {
	c = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(c, "#")))
	switch len(c) {
	case 6:
		if _, err := strconv.ParseUint(c, 16, 32); err != nil {
			return ""
		}
		return c + "ff"
	case 8:
		if _, err := strconv.ParseUint(c, 16, 64); err != nil {
			return ""
		}
		return c
	default:
		return ""
	}
}

// runner executes a command and reports stdout, exit code, and error.
// An exit code (non-zero, err nil) is how fuzzel signals "no selection".
type runner func(ctx context.Context, args []string, stdin string) (string, int, error)

func execRunner(ctx context.Context, args []string, stdin string) (string, int, error) {
	// #nosec G204 -- args come from fuzzelArgs with compile-time
	// constant colors; palette items travel via stdin, never argv.
	cmd := exec.CommandContext(ctx, "fuzzel", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// fuzzel's own diagnostics ride on stderr: fold them into
			// the error for real failures (code >= 2); exit 1 is the
			// documented "no selection" and stays silent.
			if ee.ExitCode() >= 2 {
				return "", ee.ExitCode(), fmt.Errorf("fuzzel exited with code %d: %s",
					ee.ExitCode(), strings.TrimSpace(errb.String()))
			}
			return out.String(), ee.ExitCode(), nil
		}
		return "", -1, fmt.Errorf("fuzzel: %w", err)
	}
	return out.String(), 0, nil
}

// chooseWith is the testable core: it feeds items to the runner and
// maps fuzzel's outcome to (line, error).
func chooseWith(ctx context.Context, r runner, opts Options, items []string) (string, error) {
	stdout, code, err := r(ctx, fuzzelArgs(opts, len(items)), strings.Join(items, "\n")+"\n")
	if err != nil {
		return "", err
	}
	// Reviewer finding (round 3): a killed fuzzel (shared ctx expired)
	// looks exactly like a cancel — empty stdout, non-zero exit. Check
	// the ctx first so a timeout surfaces as a failure, not a dismissal.
	if ctx.Err() != nil {
		return "", fmt.Errorf("palette: %w", ctx.Err())
	}
	line := strings.TrimSpace(stdout)
	if line == "" {
		// Esc / empty output: a cancel, not a failure.
		return "", nil
	}
	if code != 0 {
		return "", fmt.Errorf("fuzzel exited with code %d", code)
	}
	return line, nil
}
