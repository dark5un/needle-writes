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
	primaryErr error
	claimErr   error
	chatErr    error
	injectErr  error
	primary    string
	clipboard  string
	chatOut    string
	afterCtrlC string
	claims     []string
	chatMsgs   []action.Message
	combos     [][]uint32
	chatCalled bool
	servedYes  bool
	servedCall bool
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
		Served: func(time.Duration) bool { d.servedCall = true; return d.servedYes },
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
	d := &deps{primary: "", clipboard: "copied via ctrl c", chatOut: "c"}
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
	d := &deps{primary: "teh quick brown fox", chatOut: "The quick brown fox."} //nolint:misspell // intentional typo: proofread input
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
	d := &deps{primary: "x", chatOut: "Here is the corrected text:\n\nfixed x"}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK || d.lastClaim() != "fixed x" {
		t.Fatalf("exit=%d claimed=%q", code, d.lastClaim())
	}
}

func TestEmptySelectionFallsBackToClipboard(t *testing.T) {
	d := &deps{primary: "", clipboard: "from clipboard", chatOut: "c"}
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
