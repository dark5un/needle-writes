package palette

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner returns a canned fuzzel outcome and records the call.
func fakeRunner(stdout string, code int, err error, gotArgs *[]string, gotStdin *string) runner {
	return func(_ context.Context, args []string, stdin string) (string, int, error) {
		if gotArgs != nil {
			*gotArgs = args
		}
		if gotStdin != nil {
			*gotStdin = stdin
		}
		return stdout, code, err
	}
}

func TestFuzzelArgsDmenuMode(t *testing.T) {
	args := fuzzelArgs(Options{Prompt: "rewrite selection: "}, 7)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-d") {
		t.Fatalf("args must enable dmenu mode: %v", args)
	}
	if !strings.Contains(joined, "-p rewrite selection: ") {
		t.Fatalf("args must pass the prompt: %v", args)
	}
	// UX review: every item visible — fuzzel draws no scroll indicator.
	if !strings.Contains(joined, "-l 7") {
		t.Fatalf("lines must default to the item count: %v", args)
	}
}

func TestFuzzelArgsWidthAndSelectionColors(t *testing.T) {
	// UX review findings: no mid-word truncation (width) and a visible
	// selection row (fuzzel's default selection bg is ~1.06:1 contrast).
	o := Options{
		Prompt:              "p: ",
		WidthChars:          70,
		SelectionBackground: "#264777",
		SelectionText:       "#d6e3ff",
	}
	joined := strings.Join(fuzzelArgs(o, 7), " ")
	for _, want := range []string{"-w 70", "-s 264777ff", "-S d6e3ffff"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args must contain %q: %v", want, joined)
		}
	}
}

func TestFuzzelColorNormalization(t *testing.T) {
	// fuzzel wants rrggbbaa WITHOUT '#'; a bad value makes fuzzel exit
	// 1 ("invalid color") which we would misread as a user cancel.
	cases := map[string]string{
		"#264777":   "264777ff",
		"264777":    "264777ff",
		"#264777FF": "264777ff",
		"#rgb":      "", // rejected: not a form fuzzel accepts
		"zzzzzz":    "",
		"":          "",
	}
	for in, want := range cases {
		if got := fuzzelColor(in); got != want {
			t.Errorf("fuzzelColor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFuzzelArgsExplicitLines(t *testing.T) {
	joined := strings.Join(fuzzelArgs(Options{Prompt: "p: ", Lines: 10}, 7), " ")
	if !strings.Contains(joined, "-l 10") {
		t.Fatalf("explicit Lines must win over item count: %v", joined)
	}
}

func TestChooseReturnsSelection(t *testing.T) {
	var args []string
	var stdin string
	r := fakeRunner("casual — Rewrite in a friendly conversational tone\n", 0, nil, &args, &stdin)
	line, err := chooseWith(context.Background(), r, Options{Prompt: "p: "},
		[]string{"proofread — a", "casual — b"})
	if err != nil || line != "casual — Rewrite in a friendly conversational tone" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	if !strings.Contains(stdin, "proofread — a\ncasual — b") {
		t.Fatalf("items must be fed newline-separated: %q", stdin)
	}
}

func TestChooseCancelIsNotError(t *testing.T) {
	// fuzzel exits 1 when the user dismisses the palette (Esc).
	r := fakeRunner("", 1, nil, nil, nil)
	line, err := chooseWith(context.Background(), r, Options{Prompt: "p: "}, []string{"a"})
	if err != nil {
		t.Fatalf("cancel must not be an error: %v", err)
	}
	if line != "" {
		t.Fatalf("line=%q, want empty", line)
	}
}

func TestChooseEmptyStdoutIsCancel(t *testing.T) {
	r := fakeRunner("  \n", 0, nil, nil, nil)
	line, err := chooseWith(context.Background(), r, Options{Prompt: "p: "}, []string{"a"})
	if err != nil || line != "" {
		t.Fatalf("line=%q err=%v; empty output must read as cancel", line, err)
	}
}

func TestChooseFailure(t *testing.T) {
	r := fakeRunner("partial", 2, nil, nil, nil)
	_, err := chooseWith(context.Background(), r, Options{Prompt: "p: "}, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "code 2") {
		t.Fatalf("err=%v, want non-zero exit surfaced", err)
	}
}

func TestChooseExecError(t *testing.T) {
	// Runner itself fails (fuzzel not installed): error must surface.
	r := fakeRunner("", -1, errors.New("exec: no such file"), nil, nil)
	_, err := chooseWith(context.Background(), r, Options{Prompt: "p: "}, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err=%v, want exec error surfaced", err)
	}
}

func TestChooseCtxTimeoutIsErrorNotCancel(t *testing.T) {
	// Reviewer finding (round 3): when the shared ctx expires,
	// exec.CommandContext kills fuzzel -> empty stdout, non-zero code.
	// That must surface as a failure, not masquerade as a user cancel.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := fakeRunner("", 1, nil, nil, nil)
	_, err := chooseWith(ctx, r, Options{Prompt: "p: "}, []string{"a"})
	if err == nil {
		t.Fatal("expired ctx must be an error, not a cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want wrapped context.Canceled", err)
	}
}
