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
		"agentify", "agentify+", "publisher-opinion", "book-reviewer",
	}
	got := List()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestAgentifyActionsRegistered(t *testing.T) {
	// User-requested agentic-work conversions: clarify, and clarify+expand.
	for _, name := range []string{"agentify", "agentify+"} {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		if a.SystemPrompt == "" || a.Description == "" {
			t.Fatalf("%s missing prompt or description", name)
		}
		if a.Temperature < 0 || a.Temperature > 2 {
			t.Fatalf("%s temperature out of range: %f", name, a.Temperature)
		}
	}
	// The expand variant must instruct expansion AND forbid
	// invention (reviewer round: pin the faithfulness clauses).
	plus, _ := Get("agentify+")
	lowPlus := strings.ToLower(plus.SystemPrompt)
	if !strings.Contains(lowPlus, "acceptance") || !strings.Contains(lowPlus, "never invent") {
		t.Fatalf("agentify+ prompt must ask for verifiable structure and forbid invention: %q", plus.SystemPrompt)
	}
	// The plain variant must stay faithful (no invented scope) and
	// pin the output language.
	plain, _ := Get("agentify")
	lowPlain := strings.ToLower(plain.SystemPrompt)
	if !strings.Contains(lowPlain, "do not add") {
		t.Fatalf("agentify prompt must forbid invented scope: %q", plain.SystemPrompt)
	}
	if !strings.Contains(lowPlain, "english") {
		t.Fatalf("agentify prompt must pin the output language: %q", plain.SystemPrompt)
	}
}

func TestReviewActionsRegistered(t *testing.T) {
	// mac parity (needle-mac): two critique actions that echo the
	// original text verbatim, then append the assessment. Pin the
	// two-part structure and the language-matching clause.
	for _, name := range []string{"publisher-opinion", "book-reviewer"} {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		if a.Description == "" {
			t.Fatalf("%s missing description", name)
		}
		low := strings.ToLower(a.SystemPrompt)
		if !strings.Contains(low, "verbatim") {
			t.Fatalf("%s prompt must pin a verbatim echo of the original: %q", name, a.SystemPrompt)
		}
		if !strings.Contains(low, "two parts") {
			t.Fatalf("%s prompt must pin the two-part reply shape: %q", name, a.SystemPrompt)
		}
		if !strings.Contains(low, "same language") {
			t.Fatalf("%s prompt must match the critique language to the passage: %q", name, a.SystemPrompt)
		}
	}
	// Exact temperatures and full prompt text are the cross-platform
	// parity contract with needle-mac: pin them literally so a one-word
	// drift in either repo fails a test instead of quietly diverging.
	pub, _ := Get("publisher-opinion")
	if pub.Temperature != 0.5 {
		t.Fatalf("publisher-opinion temperature = %v, want 0.5 (mac parity)", pub.Temperature)
	}
	rev, _ := Get("book-reviewer")
	if rev.Temperature != 0.6 {
		t.Fatalf("book-reviewer temperature = %v, want 0.6 (mac parity)", rev.Temperature)
	}

	// Each critic keeps its own persona and evidence discipline.
	pub, _ = Get("publisher-opinion")
	if !strings.Contains(strings.ToLower(pub.SystemPrompt), "acquire") ||
		!strings.Contains(strings.ToLower(pub.SystemPrompt), "market") {
		t.Fatalf("publisher-opinion prompt must carry the publisher persona (market fit, acquire/revise/pass verdict): %q", pub.SystemPrompt)
	}
	rev, _ = Get("book-reviewer")
	if !strings.Contains(strings.ToLower(rev.SystemPrompt), "specific quotes") ||
		!strings.Contains(strings.ToLower(rev.SystemPrompt), "poetry") {
		t.Fatalf("book-reviewer prompt must demand quote-grounded literary critique: %q", rev.SystemPrompt)
	}
}

func TestReviewPromptsVerbatimMacParity(t *testing.T) {
	// Full-text pin against Sources/needle-mac/main.swift (verbatim
	// port, commit affa7f4). If either repo rewords a review prompt,
	// this test is where the parity break surfaces.
	pub, _ := Get("publisher-opinion")
	wantPub := `You are an experienced book publisher's editor assessing a passage. Reply in exactly two parts:
1. The user's original text, reproduced verbatim (not one word changed).
2. A blank line, then a short publisher's opinion of the passage: its market fit and audience, editorial strengths, what weakens it, and a one-line verdict (acquire / revise / pass) with the reason. Honest and specific, 80-150 words, written in the same language as the passage.
Output ONLY those two parts, no preamble or code fences.`
	if pub.SystemPrompt != wantPub {
		t.Fatalf("publisher-opinion prompt drifted from the mac source:\n got %q\nwant %q", pub.SystemPrompt, wantPub)
	}
	rev, _ := Get("book-reviewer")
	wantRev := `You are a literary book reviewer who covers fiction and poetry. Reply in exactly two parts:
1. The user's original text, reproduced verbatim (not one word changed).
2. A blank line, then a reviewer's critique of the passage as a work of literature: voice and style, imagery and language, rhythm and sound (for poetry) or characterization, scene craft and tension (for prose/novels), what it does well, where it falls flat, and a closing one-line assessment. Judge it seriously as a reader would — specific quotes from the passage as evidence, no generic praise. 100-180 words, written in the same language as the passage.
Output ONLY those two parts, no preamble or code fences.`
	if rev.SystemPrompt != wantRev {
		t.Fatalf("book-reviewer prompt drifted from the mac source:\n got %q\nwant %q", rev.SystemPrompt, wantRev)
	}
}

func TestPaletteItemsFitWidth(t *testing.T) {
	// UX review constraint: fuzzel -w PaletteLineWidth must fit every
	// line without truncation — guard so new actions cannot silently
	// overflow (the current longest line uses the whole budget).
	for _, it := range PaletteItems() {
		if r := []rune(it); len(r) > PaletteLineWidth {
			t.Errorf("palette line %d runes exceeds width budget %d: %q", len(r), PaletteLineWidth, it)
		}
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
