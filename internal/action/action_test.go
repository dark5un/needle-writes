package action

import (
	"errors"
	"strings"
	"testing"
)

func TestListReturnsBuiltinActions(t *testing.T) {
	want := []string{
		"proofread", "shorten", "professional", "casual",
		"summarize", "translate-en", "translate-el",
	}
	got := List()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestGetKnownActionHasPromptAndTemperature(t *testing.T) {
	a, err := Get("proofread")
	if err != nil {
		t.Fatalf("Get(proofread): %v", err)
	}
	if a.SystemPrompt == "" {
		t.Fatal("proofread has empty SystemPrompt")
	}
	if a.Temperature < 0 || a.Temperature > 2 {
		t.Fatalf("proofread temperature out of range: %f", a.Temperature)
	}
}

func TestGetUnknownAction(t *testing.T) {
	_, err := Get("nope")
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("Get(nope) err = %v, want ErrUnknownAction", err)
	}
}

func TestPaletteItems(t *testing.T) {
	// M3: one display line per built-in, padded name + separator +
	// description, same order, columns aligned (UX review).
	items := PaletteItems()
	names := List()
	if len(items) != len(names) {
		t.Fatalf("items=%d names=%d", len(items), len(names))
	}
	for i, it := range items {
		if !strings.HasPrefix(it, names[i]) {
			t.Errorf("item %d = %q, want name prefix %q", i, it, names[i])
		}
		if got := strings.Index(it, PaletteSeparator); got != paletteNameWidth {
			t.Errorf("item %d: separator at column %d, want %d (aligned columns): %q", i, got, paletteNameWidth, it)
		}
		a, _ := Get(names[i])
		if !strings.HasSuffix(it, a.Description) {
			t.Errorf("item %d = %q, want suffix description %q", i, it, a.Description)
		}
	}
}

func TestParsePaletteItem(t *testing.T) {
	a, err := ParsePaletteItem("casual" + PaletteSeparator + "Rewrite in a friendly conversational tone")
	if err != nil || a.Name != "casual" || a.SystemPrompt == "" {
		t.Fatalf("full item: a=%+v err=%v", a, err)
	}
	// Padded display line (what PaletteItems actually emits).
	a, err = ParsePaletteItem("casual      " + PaletteSeparator + "Rewrite in a friendly conversational tone")
	if err != nil || a.Name != "casual" {
		t.Fatalf("padded item: a=%+v err=%v", a, err)
	}
	// A bare valid name is accepted too (user echo of a filtered line).
	a, err = ParsePaletteItem("proofread")
	if err != nil || a.Name != "proofread" {
		t.Fatalf("bare name: a=%+v err=%v", a, err)
	}
	if _, perr := ParsePaletteItem("bogus" + PaletteSeparator + "whatever"); !errors.Is(perr, ErrUnknownAction) {
		t.Fatalf("unknown item: err=%v, want ErrUnknownAction", perr)
	}
	if _, perr := ParsePaletteItem("   "); !errors.Is(perr, ErrUnknownAction) {
		t.Fatalf("blank item: err=%v, want ErrUnknownAction", perr)
	}
}

func TestBuildMessagesEmptySelectionRejected(t *testing.T) {
	for _, s := range []string{"", "   ", "\n\t "} {
		_, err := BuildMessages("proofread", s)
		if !errors.Is(err, ErrEmptySelection) {
			t.Fatalf("BuildMessages(%q) err = %v, want ErrEmptySelection", s, err)
		}
	}
}

func TestBuildMessagesOversizeRejected(t *testing.T) {
	big := strings.Repeat("a", MaxSelectionRunes+1)
	_, err := BuildMessages("proofread", big)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v, want ErrTooLarge", err)
	}
}

func TestBuildMessagesShape(t *testing.T) {
	msgs, err := BuildMessages("shorten", "some selected text")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want system+user, got %d messages", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("roles = %q,%q want system,user", msgs[0].Role, msgs[1].Role)
	}
	if msgs[1].Content != "some selected text" {
		t.Fatalf("user content = %q, want the selection verbatim", msgs[1].Content)
	}
}

func TestCleanStripsPreambleChatter(t *testing.T) {
	in := "Here is the proofread text:\n\nThe quick brown fox jumps."
	got := Clean(in)
	if got != "The quick brown fox jumps." {
		t.Fatalf("Clean preamble = %q", got)
	}
}

func TestCleanStripsCodeFences(t *testing.T) {
	in := "```\nThe quick brown fox.\nA second line.\n```"
	got := Clean(in)
	if got != "The quick brown fox.\nA second line." {
		t.Fatalf("Clean fences = %q", got)
	}
}

func TestCleanFenceWithLanguageTag(t *testing.T) {
	in := "```go\nfmt.Println(\"hi\")\n```"
	got := Clean(in)
	if got != "fmt.Println(\"hi\")" {
		t.Fatalf("Clean fence+tag = %q", got)
	}
}

func TestCleanSingleLineFenceKeepsContent(t *testing.T) {
	// Regression (reviewer-found): content on the same line as the opening
	// fence must NOT be treated as a language tag and dropped.
	in := "```The quick brown fox.\n```"
	got := Clean(in)
	if got != "The quick brown fox." {
		t.Fatalf("Clean single-line fence = %q, want content preserved", got)
	}
}

func TestCleanOneWordFenceContentKept(t *testing.T) {
	// Regression (reviewer-found, round 2): a fenced first line that is a
	// single short word is CONTENT unless it is a known language tag;
	// the old token-regex guard dropped it.
	in := "```\nHello\nWorld\n```"
	got := Clean(in)
	if got != "Hello\nWorld" {
		t.Fatalf("Clean one-word fence lines = %q, want content preserved", got)
	}
}

func TestCleanFenceTagOnlyNeverEmpties(t *testing.T) {
	// Revert-guard (reviewer round 2): stripping a tag line must never
	// destroy the whole output; a lone tag word survives as content.
	in := "```go\n```"
	got := Clean(in)
	if got != "go" {
		t.Fatalf("Clean tag-only fence = %q, want %q", got, "go")
	}
}

func TestCleanProseStartingWithInterjectionKept(t *testing.T) {
	// Regression (reviewer round 2): "Certainly is the word:" is prose,
	// not a standalone chatter line; only bare interjection lines strip.
	in := "Certainly is the word:\nnext line"
	got := Clean(in)
	if got != in {
		t.Fatalf("Clean prose 'Certainly...' = %q, want unchanged", got)
	}
}

func TestCleanStandaloneInterjectionStripped(t *testing.T) {
	in := "Of course!\n\nThe corrected text."
	got := Clean(in)
	if got != "The corrected text." {
		t.Fatalf("Clean standalone interjection = %q", got)
	}
}

func TestCleanStripsWrappingQuotes(t *testing.T) {
	in := `"The quick brown fox."`
	got := Clean(in)
	if got != "The quick brown fox." {
		t.Fatalf("Clean quotes = %q", got)
	}
}

func TestCleanKeepsLegitimateContent(t *testing.T) {
	in := "Bullet one:\n- first point\n- second point\n\nNote: keep \"quoted\" words."
	got := Clean(in)
	if got != strings.TrimSpace(in) {
		t.Fatalf("Clean changed legitimate content:\n got %q\nwant %q", got, strings.TrimSpace(in))
	}
}
