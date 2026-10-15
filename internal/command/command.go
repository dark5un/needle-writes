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
	"time"

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
	// Inject sends a key combo (evdev keycodes) via the virtual keyboard;
	// nil disables M2 auto-capture/auto-paste (v0 hold mode).
	Inject func(combo ...uint32) error
	// Served reports whether the last claim was fetched by a reader
	// (the paste target), waiting up to timeout. nil = assume served.
	Served func(timeout time.Duration) bool
}

// evdev keycodes for the injection combos.
var (
	CtrlC = []uint32{29, 46}
	CtrlV = []uint32{29, 47}
)

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

	// Save the user's clipboard BEFORE anything can clobber it (the
	// Ctrl+C capture fallback overwrites CLIPBOARD), so the delivery
	// restore puts back the ORIGINAL content. Best-effort: a read error
	// just means there is nothing to restore.
	var savedClip string
	if d.Inject != nil {
		savedClip, _ = d.ReadClip()
	}

	sel, capErr := d.ReadPrimary()
	if capErr != nil {
		_, _ = fmt.Fprintf(stderr, "read primary selection: %v\n", capErr)
		return ExitWayland
	}
	if sel == "" && d.Inject != nil {
		// M2: no PRIMARY — ask the focused app for a copy, then read
		// the clipboard (which now carries the selection).
		if ierr := d.Inject(CtrlC...); ierr != nil {
			_, _ = fmt.Fprintf(stderr, "needle-writes: Ctrl+C injection failed: %v (falling back to current clipboard)\n", ierr)
		}
	}
	if sel == "" {
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
	if clErr := d.Claim(res); clErr != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: clipboard claim failed: %v\n", clErr)
		return ExitWayland
	}
	if d.Inject != nil {
		if ierr := d.Inject(CtrlV...); ierr != nil {
			_, _ = fmt.Fprintf(stderr, "needle-writes: Ctrl+V injection failed: %v (result stays on the clipboard for manual paste)\n", ierr)
		} else {
			if d.Served != nil && !d.Served(2*time.Second) {
				_, _ = fmt.Fprintln(stderr, "needle-writes: paste target did not fetch the result within 2s")
			}
			// The paste has been served (or the target declined it): put
			// the user's original clipboard back.
			if savedClip != "" {
				if rerr := d.Claim(savedClip); rerr != nil {
					_, _ = fmt.Fprintf(stderr, "needle-writes: clipboard restore failed: %v\n", rerr)
				}
			}
		}
	}
	_, _ = fmt.Fprint(stdout, res)
	return ExitOK
}
