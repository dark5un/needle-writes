package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dark5un/needle-writes/internal/action"
)

// fake deps: capture seams instead of a live compositor/engine.
type deps struct {
	primaryErr     error
	claimErr       error
	chatErr        error
	injectErr      error
	chooseErr      error
	primary        string
	clipboard      string
	chatOut        string
	afterCtrlC     string
	afterClaimClip string // if set: next Claim makes the clipboard hold this (newer-run sim)
	chooseLine     string
	claims         []string
	chatMsgs       []action.Message
	combos         [][]uint32
	chooseItems    []string
	holds          []time.Duration
	chatCalled     bool
	chooseCalled   bool
	servedYes      bool
	servedCall     bool
	notOwns        bool // true: our claim was superseded by a newer run
	servedCalls    int
}

func (d *deps) lastClaim() string {
	if len(d.claims) == 0 {
		return ""
	}
	return d.claims[len(d.claims)-1]
}

func (d *deps) testDeps() Deps {
	return Deps{
		ReadPrimary: func() (string, error) { return d.primary, d.primaryErr },
		ReadClip:    func() (string, error) { return d.clipboard, nil },
		Claim: func(s string) error {
			d.claims = append(d.claims, s)
			// Realistic: after a claim the clipboard (and the
			// watcher's re-owned copy) holds the claimed text,
			// unless the test simulates a newer run replacing it.
			if d.afterClaimClip != "" {
				d.clipboard = d.afterClaimClip
				d.afterClaimClip = ""
			} else {
				d.clipboard = s
			}
			return d.claimErr
		},
		Chat: func(ctx context.Context, msgs []action.Message, temp float64) (string, error) {
			d.chatCalled = true
			d.chatMsgs = msgs
			return d.chatOut, d.chatErr
		},
		Inject: func(combo ...uint32) error {
			d.combos = append(d.combos, combo)
			if d.injectErr == nil && d.afterCtrlC != "" && len(combo) == 2 && combo[0] == 29 && combo[1] == 46 {
				d.clipboard = d.afterCtrlC // simulate the app copying the selection
			}
			return d.injectErr
		},
		Served: func(time.Duration) bool {
			d.servedCall = true
			d.servedCalls++
			return d.servedYes
		},
		Hold: func(dur time.Duration) { d.holds = append(d.holds, dur) },
		Owns: func() bool { return !d.notOwns },
		Choose: func(items []string) (string, error) {
			d.chooseCalled = true
			d.chooseItems = items
			return d.chooseLine, d.chooseErr
		},
	}
}

func run(t *testing.T, d *deps, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb, d.testDeps())
	return code, out.String(), errb.String()
}

// --- M2 injection flow ---

func TestM2CaptureFallbackInjectsCtrlC(t *testing.T) {
	d := &deps{primary: "", clipboard: "copied via ctrl c", chatOut: "c", servedYes: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if len(d.combos) < 1 || d.combos[0][0] != 29 || d.combos[0][1] != 46 {
		t.Fatalf("expected Ctrl+C injection first, combos=%v", d.combos)
	}
	if d.chatMsgs[1].Content != "copied via ctrl c" {
		t.Fatalf("chat input = %q", d.chatMsgs[1].Content)
	}
}

func TestM2DeliveryInjectsCtrlVAndRestores(t *testing.T) {
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "RESULT", servedYes: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if len(d.claims) != 2 || d.claims[0] != "RESULT" || d.claims[1] != "user clip" {
		t.Fatalf("claims = %v, want [RESULT user-clip-restore]", d.claims)
	}
	if len(d.combos) != 1 || d.combos[0][1] != 47 {
		t.Fatalf("combos = %v, want one Ctrl+V", d.combos)
	}
	if !d.servedCall {
		t.Fatal("Served seam must be consulted before restoring")
	}
}

func TestM2InjectFailureFallsBackToClipboard(t *testing.T) {
	// Reviewer finding 2: when capture-path injection fails (no vk on
	// the compositor), Run must fall through to the plain clipboard
	// fallback instead of aborting with ExitWayland.
	d := &deps{primary: "", clipboard: "user clip", chatOut: "R", injectErr: errors.New("no vk")}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if d.chatMsgs[1].Content != "user clip" {
		t.Fatalf("chat input = %q, want clipboard fallback", d.chatMsgs[1].Content)
	}
}

func TestM2SavedClipCapturedBeforeCtrlC(t *testing.T) {
	// Reviewer finding 3: Ctrl+C clobbers CLIPBOARD; the restore must put
	// back the ORIGINAL clipboard, so savedClip must be read first.
	d := &deps{primary: "", clipboard: "user original", afterCtrlC: "selection copy",
		chatOut: "RESULT", servedYes: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if d.chatMsgs[1].Content != "selection copy" {
		t.Fatalf("chat input = %q, want post-Ctrl+C clipboard", d.chatMsgs[1].Content)
	}
	if len(d.claims) != 2 || d.claims[0] != "RESULT" || d.claims[1] != "user original" {
		t.Fatalf("claims = %v, want [RESULT user original]", d.claims)
	}
}

func TestWaylandFailurePaths(t *testing.T) {
	// Reviewer gap: the three ExitWayland paths.
	var out, errb bytes.Buffer
	d := (&deps{primaryErr: errors.New("wl dead")}).testDeps()
	if code := Run(context.Background(), []string{"proofread"}, &out, &errb, d); code != ExitWayland {
		t.Errorf("ReadPrimary error: exit=%d", code)
	}
	d2 := &deps{clipboard: "x", chatOut: "R", claimErr: errors.New("claim failed")}
	dd := d2.testDeps()
	if code := Run(context.Background(), []string{"proofread"}, &out, &errb, dd); code != ExitWayland {
		t.Errorf("Claim error (primary empty -> clip -> claim fails): exit=%d", code)
	}
	// ReadClip error with no injection seam.
	plain := Deps{
		ReadPrimary: func() (string, error) { return "", nil },
		ReadClip:    func() (string, error) { return "", errors.New("clip dead") },
		Claim:       func(string) error { return nil },
		Chat:        func(context.Context, []action.Message, float64) (string, error) { return "", nil },
	}
	if code := Run(context.Background(), []string{"proofread"}, &out, &errb, plain); code != ExitWayland {
		t.Errorf("ReadClip error: exit=%d", code)
	}
}

func TestM2NoRestoreWhenInjectFails(t *testing.T) {
	// Ctrl+V injection failure: the claim must stay up for a manual
	// paste and the user's clipboard must NOT be restored over it.
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "R", injectErr: errors.New("vk dead")}
	code, _, errb := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	if len(d.claims) != 1 || d.claims[0] != "R" {
		t.Fatalf("claims = %v, want just the result", d.claims)
	}
}

func TestNilDepsGuard(t *testing.T) {
	// Reviewer nit: Run with an unwired Deps seam must fail cleanly, not panic.
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"proofread"}, &out, &errb, Deps{})
	if code != ExitUsage || !strings.Contains(errb.String(), "unwired") {
		t.Fatalf("exit=%d stderr=%q", code, errb.String())
	}
}

func TestHappyPath(t *testing.T) {
	d := &deps{primary: "teh quick brown fox", chatOut: "The quick brown fox.", servedYes: true} //nolint:misspell // intentional typo: proofread input
	code, _, errb := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, errb)
	}
	if !d.chatCalled {
		t.Fatal("chat not called")
	}
	if d.lastClaim() != "The quick brown fox." {
		t.Fatalf("claimed = %q", d.lastClaim())
	}
	// system prompt must come from the action registry
	if len(d.chatMsgs) != 2 || d.chatMsgs[0].Role != "system" || d.chatMsgs[1].Content != "teh quick brown fox" { //nolint:misspell // matches the input above
		t.Fatalf("messages = %+v", d.chatMsgs)
	}
}

func TestCleanAppliedToResult(t *testing.T) {
	d := &deps{primary: "x", chatOut: "Here is the corrected text:\n\nfixed x", servedYes: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK || d.lastClaim() != "fixed x" {
		t.Fatalf("exit=%d claimed=%q", code, d.lastClaim())
	}
}

func TestEmptySelectionFallsBackToClipboard(t *testing.T) {
	d := &deps{primary: "", clipboard: "from clipboard", chatOut: "c", servedYes: true}
	code, _, _ := run(t, d, "shorten")
	if code != ExitOK || !d.chatCalled {
		t.Fatalf("exit=%d chatCalled=%v", code, d.chatCalled)
	}
}

func TestNothingSelectedAborts(t *testing.T) {
	d := &deps{primary: "", clipboard: ""}
	code, _, errb := run(t, d, "proofread")
	if code != ExitNoSelection {
		t.Fatalf("exit = %d, want %d", code, ExitNoSelection)
	}
	if d.lastClaim() != "" || d.chatCalled {
		t.Fatal("must not chat or claim on empty capture")
	}
	if !strings.Contains(errb, "select") {
		t.Fatalf("stderr should tell the user to select text, got %q", errb)
	}
}

func TestUnknownAction(t *testing.T) {
	d := &deps{primary: "x"}
	code, _, errb := run(t, d, "no-such-action")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errb, "proofread") {
		t.Fatalf("usage error should list actions, got %q", errb)
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), nil, &out, &errb, (&deps{}).testDeps())
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
}

func TestListActions(t *testing.T) {
	d := &deps{}
	code, out, _ := run(t, d, "-list")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	for _, name := range action.List() {
		if !strings.Contains(out, name) {
			t.Errorf("-list output missing %q: %q", name, out)
		}
	}
}

func TestChatErrorExitsEngineFailure(t *testing.T) {
	d := &deps{primary: "x", chatErr: errors.New("engine down")}
	code, _, errb := run(t, d, "proofread")
	if code != ExitEngine {
		t.Fatalf("exit = %d, want %d", code, ExitEngine)
	}
	if d.lastClaim() != "" {
		t.Fatal("must not claim on engine failure")
	}
	if !strings.Contains(errb, "engine down") {
		t.Fatalf("error should surface: %q", errb)
	}
}

func TestEmptyModelResultAborts(t *testing.T) {
	d := &deps{primary: "x", chatOut: "   "}
	code, _, _ := run(t, d, "proofread")
	if code != ExitEngine || d.lastClaim() != "" {
		t.Fatalf("exit=%d claimed=%q; empty result must never paste", code, d.lastClaim())
	}
}

func TestTooLargeSelection(t *testing.T) {
	d := &deps{primary: strings.Repeat("x", action.MaxSelectionRunes+1)}
	code, _, _ := run(t, d, "proofread")
	if code != ExitTooLarge {
		t.Fatalf("exit = %d, want %d", code, ExitTooLarge)
	}
}

// --- M3 palette (pick) ---

func TestPickRunsChosenAction(t *testing.T) {
	d := &deps{primary: "x", chatOut: "R", servedYes: true,
		chooseLine: "casual" + action.PaletteSeparator + "Rewrite in a friendly conversational tone"}
	code, _, errb := run(t, d, "pick")
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(d.chatMsgs[0].Content, "friendly, conversational") {
		t.Fatalf("system prompt is not the casual one: %q", d.chatMsgs[0].Content)
	}
	if d.lastClaim() != "R" {
		t.Fatalf("claims=%v", d.claims)
	}
}

func TestPickListsAllActions(t *testing.T) {
	d := &deps{primary: "x", chatOut: "R", servedYes: true,
		chooseLine: "proofread" + action.PaletteSeparator + "Fix spelling, grammar and punctuation, keep the voice"}
	code, _, _ := run(t, d, "pick")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if len(d.chooseItems) != len(action.List()) {
		t.Fatalf("palette items=%d, want %d", len(d.chooseItems), len(action.List()))
	}
}

func TestPickCapturesBeforePalette(t *testing.T) {
	// Nothing selected: abort BEFORE opening the palette — the user
	// should not pick an action only to be told to select text.
	d := &deps{primary: "", clipboard: ""}
	code, _, _ := run(t, d, "pick")
	if code != ExitNoSelection {
		t.Fatalf("exit=%d, want %d", code, ExitNoSelection)
	}
	if d.chooseCalled {
		t.Fatal("palette must not open on empty capture")
	}
}

func TestPickCancelExitsCancelled(t *testing.T) {
	d := &deps{primary: "x", chooseLine: ""}
	code, _, _ := run(t, d, "pick")
	if code != ExitCancelled {
		t.Fatalf("exit=%d, want %d", code, ExitCancelled)
	}
	if d.chatCalled || d.lastClaim() != "" {
		t.Fatal("cancel must not chat or claim")
	}
}

func TestPickUnknownItem(t *testing.T) {
	d := &deps{primary: "x", chooseLine: "bogus" + action.PaletteSeparator + "nope"}
	code, _, _ := run(t, d, "pick")
	if code != ExitUsage {
		t.Fatalf("exit=%d, want %d", code, ExitUsage)
	}
	if d.chatCalled {
		t.Fatal("unknown item must not reach the engine")
	}
}

func TestPickChooseError(t *testing.T) {
	d := &deps{primary: "x", chooseErr: errors.New("fuzzel not installed")}
	code, _, errb := run(t, d, "pick")
	if code != ExitWayland {
		t.Fatalf("exit=%d, want %d", code, ExitWayland)
	}
	if !strings.Contains(errb, "fuzzel not installed") {
		t.Fatalf("palette error must surface: %q", errb)
	}
}

func TestPickWithoutChooseSeam(t *testing.T) {
	// A Deps set without Choose (palette unavailable): clean usage
	// error, not a panic.
	d := (&deps{primary: "x"}).testDeps()
	d.Choose = nil
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"pick"}, &out, &errb, d)
	if code != ExitUsage || !strings.Contains(errb.String(), "palette") {
		t.Fatalf("exit=%d stderr=%q", code, errb.String())
	}
}

func TestPickCancelAfterCtrlCRestoresClipboard(t *testing.T) {
	// Reviewer finding 1 (round 3): the Ctrl+C fallback clobbers
	// CLIPBOARD; a palette cancel must put the user's original
	// clipboard back — cancel is exactly where clean undo is expected.
	d := &deps{primary: "", clipboard: "user clip", afterCtrlC: "selection copy", chooseLine: ""}
	code, _, _ := run(t, d, "pick")
	if code != ExitCancelled {
		t.Fatalf("exit=%d, want %d", code, ExitCancelled)
	}
	if len(d.claims) != 1 || d.claims[0] != "user clip" {
		t.Fatalf("claims=%v, want the original clipboard restored", d.claims)
	}
}

func TestEngineFailureAfterCtrlCRestoresClipboard(t *testing.T) {
	// Same gap on the pre-existing abort paths: engine failure after
	// a Ctrl+C capture must restore the clipboard, not leave the copy
	// of the selection in its place.
	d := &deps{primary: "", clipboard: "user clip", afterCtrlC: "selection copy", chatErr: errors.New("engine down")}
	code, _, _ := run(t, d, "proofread")
	if code != ExitEngine {
		t.Fatalf("exit=%d, want %d", code, ExitEngine)
	}
	if len(d.claims) != 1 || d.claims[0] != "user clip" {
		t.Fatalf("claims=%v, want the original clipboard restored", d.claims)
	}
}

func TestNoRestoreWhenNothingClobbered(t *testing.T) {
	// PRIMARY present: no Ctrl+C, no clobber, no restore claim on cancel.
	d := &deps{primary: "x", clipboard: "user clip", chooseLine: ""}
	code, _, _ := run(t, d, "pick")
	if code != ExitCancelled {
		t.Fatalf("exit=%d", code)
	}
	if len(d.claims) != 0 {
		t.Fatalf("claims=%v, want none (clipboard was never touched)", d.claims)
	}
}

// --- delivery race: never leave a dead owner behind a pending fetch ---
//
// The clear-text bug: the app's clipboard fetch lands after the one-shot
// exits -> the compositor has a dead offer -> the late paste writes an
// EMPTY string over the selection. Fix: keep the claim alive (serving)
// until the fetch is observed, retry the keystroke once (palette-close
// focus transitions swallow the first Ctrl+V), and only then restore.

func withTinyDeliveryWaits(t *testing.T) {
	t.Helper()
	s1, s2 := pasteServeWindow, pasteHoldWindow
	pasteServeWindow, pasteHoldWindow = time.Millisecond, time.Millisecond
	t.Cleanup(func() { pasteServeWindow, pasteHoldWindow = s1, s2 })
}

func TestDeliveryRetriesCtrlVWhenNeverServed(t *testing.T) {
	withTinyDeliveryWaits(t)
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "R", servedYes: false}
	code, _, errb := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	// Two Ctrl+V injections: original + one retry when nobody fetched.
	nv := 0
	for _, c := range d.combos {
		if len(c) == 2 && c[1] == 47 {
			nv++
		}
	}
	if nv != 2 {
		t.Fatalf("combos=%v, want two Ctrl+V", d.combos)
	}
	if d.servedCalls != 2 {
		t.Fatalf("served waits=%d, want 2 (initial + post-retry)", d.servedCalls)
	}
	if len(d.holds) != 1 {
		t.Fatalf("holds=%v, want one grace hold", d.holds)
	}
	if len(d.claims) != 2 || d.claims[1] != "user clip" {
		t.Fatalf("claims=%v, want restore only after the hold", d.claims)
	}
	if !strings.Contains(errb, "clipboard") {
		t.Fatalf("unserved delivery must warn the user: %q", errb)
	}
}

func TestDeliveryNoRetryWhenServedFirstWindow(t *testing.T) {
	withTinyDeliveryWaits(t)
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "R", servedYes: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if d.servedCalls != 1 {
		t.Fatalf("served waits=%d, want 1 (served immediately, no retry)", d.servedCalls)
	}
	nv := 0
	for _, c := range d.combos {
		if len(c) == 2 && c[1] == 47 {
			nv++
		}
	}
	if nv != 1 {
		t.Fatalf("combos=%v, want exactly one Ctrl+V (no double paste)", d.combos)
	}
}

func TestDeliveryAlwaysHoldsClaimBeforeRestore(t *testing.T) {
	// The clear-text regression test: even when served=true (which a
	// history watcher satisfies instantly, NOT the paste target), the
	// claim must be held for the grace window BEFORE the restore claim
	// and before exit, so a slow app's late fetch still gets the
	// result instead of a dead owner.
	withTinyDeliveryWaits(t)
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "R", servedYes: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if len(d.holds) != 1 || d.holds[0] != pasteHoldWindow {
		t.Fatalf("holds=%v, want one grace hold of %v", d.holds, pasteHoldWindow)
	}
	if len(d.claims) != 2 || d.claims[0] != "R" || d.claims[1] != "user clip" {
		t.Fatalf("claims=%v, want result then restore", d.claims)
	}
}

func TestRestoreSkippedWhenSuperseded(t *testing.T) {
	// Repeated actions on the same selection: a NEWER needle-writes
	// claimed the clipboard while this run held. Restoring the old
	// clipboard now would clobber the newer result and re-open the
	// dead-owner window for its paste target. Skip the restore.
	withTinyDeliveryWaits(t)
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "R",
		servedYes: true, notOwns: true, afterClaimClip: "newer-run-result"}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if len(d.claims) != 1 || d.claims[0] != "R" {
		t.Fatalf("claims=%v, want the result only — no restore over a newer claim", d.claims)
	}
}

func TestDeliveryWatcherReOwnsStillRestores(t *testing.T) {
	// The normal path on this machine: the Ryoku history watcher
	// re-owns our claim (source cancelled, SAME text). The content
	// check must recognize our own text and restore as contracted.
	withTinyDeliveryWaits(t)
	d := &deps{primary: "x", clipboard: "user clip", chatOut: "R", servedYes: true, notOwns: true}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	if len(d.claims) != 2 || d.claims[0] != "R" || d.claims[1] != "user clip" {
		t.Fatalf("claims=%v, want result then restore (watcher re-owns our own text)", d.claims)
	}
}

func TestCancelRestoreProceedsWhenWatcherReOwns(t *testing.T) {
	// Abort path with the watcher re-owning the Ctrl+C copy: content
	// equals our captured selection, so the restore must proceed.
	d := &deps{primary: "", clipboard: "user clip", afterCtrlC: "selection copy",
		chooseLine: "", notOwns: true}
	code, _, _ := run(t, d, "pick")
	if code != ExitCancelled {
		t.Fatalf("exit=%d", code)
	}
	if len(d.claims) != 1 || d.claims[0] != "user clip" {
		t.Fatalf("claims=%v, want the original clipboard restored", d.claims)
	}
}
