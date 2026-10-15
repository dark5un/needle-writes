package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dark5un/needle-writes/internal/action"
)

// fake deps: capture seams instead of a live compositor/engine.
type deps struct {
	primary    string
	primaryErr error
	clipboard  string
	claimed    string
	claimErr   error
	chatOut    string
	chatErr    error
	chatMsgs   []action.Message
	chatCalled bool
}

func (d *deps) testDeps() Deps {
	return Deps{
		ReadPrimary: func() (string, error) { return d.primary, d.primaryErr },
		ReadClip:    func() (string, error) { return d.clipboard, nil },
		Claim:       func(s string) error { d.claimed = s; return d.claimErr },
		Chat: func(ctx context.Context, msgs []action.Message, temp float64) (string, error) {
			d.chatCalled = true
			d.chatMsgs = msgs
			return d.chatOut, d.chatErr
		},
	}
}

func run(t *testing.T, d *deps, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb, d.testDeps())
	return code, out.String(), errb.String()
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
	if d.claimed != "The quick brown fox." {
		t.Fatalf("claimed = %q", d.claimed)
	}
	// system prompt must come from the action registry
	if len(d.chatMsgs) != 2 || d.chatMsgs[0].Role != "system" || d.chatMsgs[1].Content != "teh quick brown fox" { //nolint:misspell // matches the input above
		t.Fatalf("messages = %+v", d.chatMsgs)
	}
}

func TestCleanAppliedToResult(t *testing.T) {
	d := &deps{primary: "x", chatOut: "Here is the corrected text:\n\nfixed x"}
	code, _, _ := run(t, d, "proofread")
	if code != ExitOK || d.claimed != "fixed x" {
		t.Fatalf("exit=%d claimed=%q", code, d.claimed)
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
	if d.claimed != "" || d.chatCalled {
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
	if d.claimed != "" {
		t.Fatal("must not claim on engine failure")
	}
	if !strings.Contains(errb, "engine down") {
		t.Fatalf("error should surface: %q", errb)
	}
}

func TestEmptyModelResultAborts(t *testing.T) {
	d := &deps{primary: "x", chatOut: "   "}
	code, _, _ := run(t, d, "proofread")
	if code != ExitEngine || d.claimed != "" {
		t.Fatalf("exit=%d claimed=%q; empty result must never paste", code, d.claimed)
	}
}

func TestTooLargeSelection(t *testing.T) {
	d := &deps{primary: strings.Repeat("x", action.MaxSelectionRunes+1)}
	code, _, _ := run(t, d, "proofread")
	if code != ExitTooLarge {
		t.Fatalf("exit = %d, want %d", code, ExitTooLarge)
	}
}
