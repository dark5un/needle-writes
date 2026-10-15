// Package command is the testable core of the needle-writes binary: it wires
// capture (Wayland selections) -> transform (action prompts + Strata chat)
// -> deliver (clipboard claim) behind an injectable Deps seam so the whole
// pipeline is unit-testable without a compositor or engine.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dark5un/needle-writes/internal/action"
)

// Process exit codes (documented in README; stable contract for binds).
const (
	ExitOK          = 0
	ExitEngine      = 1 // engine unreachable/bad result
	ExitNoSelection = 2 // nothing selected: user must select text first
	ExitTooLarge    = 3 // selection exceeds MaxSelectionRunes
	ExitWayland     = 4 // compositor/protocol failure
	ExitUsage       = 64
)

// Deps are the side-effecting seams Run drives.
type Deps struct {
	ReadPrimary func() (string, error)
	ReadClip    func() (string, error)
	Claim       func(string) error
	Chat        func(ctx context.Context, msgs []action.Message, temperature float64) (string, error)
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: needle-writes <action> | -list")
	_, _ = fmt.Fprint(w, "actions:\n")
	for _, name := range action.List() {
		a, _ := action.Get(name)
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", name, a.Description)
	}
}

// Run executes one writing action end to end and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, d Deps) int {
	if d.ReadPrimary == nil || d.ReadClip == nil || d.Claim == nil || d.Chat == nil {
		_, _ = fmt.Fprintln(stderr, "needle-writes: internal error: unwired deps")
		return ExitUsage
	}
	if len(args) == 1 && args[0] == "-list" {
		usage(stdout)
		return ExitOK
	}
	if len(args) != 1 {
		usage(stderr)
		return ExitUsage
	}
	act, err := action.Get(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		usage(stderr)
		return ExitUsage
	}

	sel, capErr := d.ReadPrimary()
	if capErr != nil {
		_, _ = fmt.Fprintf(stderr, "read primary selection: %v\n", capErr)
		return ExitWayland
	}
	if sel == "" {
		// No PRIMARY (nothing highlighted): fall back to CLIPBOARD, which
		// the user may have filled with Ctrl+C just before the hotkey.
		sel, capErr = d.ReadClip()
		if capErr != nil {
			_, _ = fmt.Fprintf(stderr, "read clipboard: %v\n", capErr)
			return ExitWayland
		}
	}
	if sel == "" {
		_, _ = fmt.Fprintln(stderr, "needle-writes: no text selected — select text (or copy it) and try again")
		return ExitNoSelection
	}

	msgs, err := action.BuildMessages(args[0], sel)
	if errors.Is(err, action.ErrTooLarge) {
		_, _ = fmt.Fprintf(stderr, "needle-writes: %v\n", err)
		return ExitTooLarge
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: %v\n", err)
		return ExitUsage
	}

	res, err := d.Chat(ctx, msgs, act.Temperature)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: engine call failed: %v\n", err)
		return ExitEngine
	}
	res = action.Clean(res)
	if res == "" {
		// Never paste nothing: an empty model result aborts the action.
		_, _ = fmt.Fprintln(stderr, "needle-writes: engine returned an empty result, aborting")
		return ExitEngine
	}
	if cerr := d.Claim(res); cerr != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: clipboard claim failed: %v\n", cerr)
		return ExitWayland
	}
	_, _ = fmt.Fprint(stdout, res)
	return ExitOK
}
