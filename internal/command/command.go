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
	ExitCancelled   = 5 // palette dismissed by the user (nothing pasted)
	ExitUsage       = 64
)

// Deps are the side-effecting seams Run drives.
type Deps struct {
	ReadPrimary func() (string, error)
	ReadClip    func() (string, error)
	Claim       func(string) error
	Chat        func(ctx context.Context, msgs []action.Message, temperature float64) (string, error)
	// Choose shows the M3 action palette over the given display lines
	// and returns the picked one; "" means cancelled. nil disables
	// the "pick" subcommand.
	Choose func(items []string) (string, error)
	// Inject sends a key combo (evdev keycodes) via the virtual keyboard;
	// nil disables M2 auto-capture/auto-paste (v0 hold mode).
	Inject func(combo ...uint32) error
	// Served reports whether the last claim was fetched by a reader
	// (the paste target), waiting up to timeout. nil = assume served.
	Served func(timeout time.Duration) bool
	// Hold keeps the claim alive and services fetches for the given
	// duration (the paste-target grace window). nil = no hold.
	Hold func(d time.Duration)
	// Owns reports whether our latest clipboard claim is still the
	// compositor's current one (not superseded by a newer run). nil =
	// assume still owned.
	Owns func() bool
}

// evdev keycodes for the injection combos.
var (
	CtrlC = []uint32{29, 46}
	CtrlV = []uint32{29, 47}
)

// Delivery timing: how long the result claim is kept alive around the
// paste (serve window per Ctrl+V, then a hold window that keeps serving
// late fetches). Package vars so tests run fast; the one-shot must
// never exit while a fetch may still be pending (dead-owner empty
// paste = the clear-text bug).
var (
	pasteServeWindow = 2 * time.Second
	pasteHoldWindow  = 2500 * time.Millisecond
)

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: needle-writes <action> | pick | -list")
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
	// Resolve the action. "pick" is the M3 palette pseudo-command: its
	// real action is chosen AFTER capture (below), so act is assigned
	// only once the palette resolves.
	picking := args[0] == "pick"
	var act action.Action
	if picking {
		if d.Choose == nil {
			_, _ = fmt.Fprintln(stderr, "needle-writes: palette unavailable: no chooser wired")
			return ExitUsage
		}
	} else {
		var gerr error
		act, gerr = action.Get(args[0])
		if gerr != nil {
			_, _ = fmt.Fprintf(stderr, "%v\n", gerr)
			usage(stderr)
			return ExitUsage
		}
	}

	// Save the user's clipboard BEFORE anything can clobber it (the
	// Ctrl+C capture fallback overwrites CLIPBOARD), so the restore
	// puts back the ORIGINAL content. Best-effort: a read error just
	// means there is nothing to restore.
	var savedClip string
	clobbered := false
	if d.Inject != nil {
		savedClip, _ = d.ReadClip()
	}

	// restoreClip undoes the run's clipboard clobber on any abort
	// AFTER the capture and BEFORE delivery (delivery has its own
	// restore). Reviewer finding (round 3): a palette cancel after a
	// clobbering capture must leave the user's clipboard as it was.
	//
	// expected is the text this run last put on CLIPBOARD (the Ctrl+C
	// copy on abort paths, the result on the delivery path). The
	// Ryoku history watcher re-owns every selection change with the
	// SAME text, so a superseded source is normal; only a DIFFERENT
	// text means a newer needle-writes run took over, and restoring
	// over its result would clobber it and re-open the dead-owner
	// empty-paste race for its paste target.
	restoreClip := func(expected string) {
		if !clobbered || savedClip == "" {
			return
		}
		if d.Owns != nil && !d.Owns() {
			cur, rerr := d.ReadClip()
			if rerr != nil || cur != expected {
				return
			}
		}
		if rerr := d.Claim(savedClip); rerr != nil {
			_, _ = fmt.Fprintf(stderr, "needle-writes: clipboard restore failed: %v\n", rerr)
		}
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
		} else {
			clobbered = true
		}
	}
	if sel == "" {
		sel, capErr = d.ReadClip()
		if capErr != nil {
			_, _ = fmt.Fprintf(stderr, "read clipboard: %v\n", capErr)
			restoreClip(sel) // round-4 finding 1: post-Ctrl+C abort
			return ExitWayland
		}
	}
	if sel == "" {
		_, _ = fmt.Fprintln(stderr, "needle-writes: no text selected — select text (or copy it) and try again")
		restoreClip(sel)
		return ExitNoSelection
	}

	// M3: the palette opens only after a successful capture — the user
	// should not pick an action just to be told to select text.
	if picking {
		line, cerr := d.Choose(action.PaletteItems())
		if cerr != nil {
			_, _ = fmt.Fprintf(stderr, "needle-writes: palette: %v\n", cerr)
			restoreClip(sel)
			return ExitWayland
		}
		if line == "" {
			restoreClip(sel)
			return ExitCancelled
		}
		picked, perr := action.ParsePaletteItem(line)
		if perr != nil {
			_, _ = fmt.Fprintf(stderr, "needle-writes: %v\n", perr)
			restoreClip(sel)
			return ExitUsage
		}
		act = picked
	}

	msgs, err := action.BuildMessages(act.Name, sel)
	if errors.Is(err, action.ErrTooLarge) {
		_, _ = fmt.Fprintf(stderr, "needle-writes: %v\n", err)
		restoreClip(sel)
		return ExitTooLarge
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: %v\n", err)
		restoreClip(sel)
		return ExitUsage
	}

	res, err := d.Chat(ctx, msgs, act.Temperature)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: engine call failed: %v\n", err)
		restoreClip(sel)
		return ExitEngine
	}
	res = action.Clean(res)
	if res == "" {
		// Never paste nothing: an empty model result aborts the action.
		_, _ = fmt.Fprintln(stderr, "needle-writes: engine returned an empty result, aborting")
		restoreClip(sel)
		return ExitEngine
	}
	if clErr := d.Claim(res); clErr != nil {
		_, _ = fmt.Fprintf(stderr, "needle-writes: clipboard claim failed: %v\n", clErr)
		restoreClip(sel)
		return ExitWayland
	}
	// The result claim itself clobbers CLIPBOARD: from here the same
	// restore rule (put savedClip back on any abort) covers delivery.
	clobbered = true
	if d.Inject != nil {
		if ierr := d.Inject(CtrlV...); ierr != nil {
			_, _ = fmt.Fprintf(stderr, "needle-writes: Ctrl+V injection failed: %v (result stays on the clipboard for manual paste)\n", ierr)
		} else {
			// The clear-text bug: the served signal cannot prove the
			// PASTE TARGET fetched — a clipboard-history watcher (the
			// Ryoku shell fetches every claim) satisfies it instantly.
			// Restoring and exiting right then leaves a DEAD clipboard
			// owner; the app's late fetch pastes an EMPTY string over
			// the selection. So: retry the keystroke only when NOBODY
			// fetched at all, and always hold the claim (keep serving)
			// for a grace window before restoring.
			served := d.Served == nil || d.Served(pasteServeWindow)
			if !served {
				// Silent: the keystroke most likely landed during a
				// focus transition (palette close). One retry.
				if rerr := d.Inject(CtrlV...); rerr == nil {
					served = d.Served == nil || d.Served(pasteServeWindow)
				}
			}
			if d.Hold != nil {
				d.Hold(pasteHoldWindow)
			}
			if !served {
				_, _ = fmt.Fprintln(stderr, "needle-writes: paste target never fetched the result; nothing was pasted, restoring your clipboard")
			}
			restoreClip(res)
		}
	}
	_, _ = fmt.Fprint(stdout, res)
	return ExitOK
}
