package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark5un/needle-writes/internal/config"
	"github.com/dark5un/needle-writes/internal/palette"
)

func TestBuildEnginePrimary(t *testing.T) {
	clearNeedleEnv(t)
	cfg := config.Defaults()
	e := buildEngine(cfg)
	if e.Fallback != nil {
		t.Fatal("local-only default: no fallback endpoint unless configured")
	}
	if e.Primary.BaseURL != cfg.Endpoint || e.Primary.Model != cfg.Model {
		t.Fatalf("primary wiring wrong: %+v", e.Primary)
	}
	if e.Primary.MaxTokens != cfg.MaxTokens || e.Primary.HTTP.Timeout != cfg.Timeout {
		t.Fatalf("engine budget not applied: max_tokens=%d timeout=%v", e.Primary.MaxTokens, e.Primary.HTTP.Timeout)
	}
	// Env beats config (the repo's documented precedence).
	t.Setenv(urlEnvVar, "http://envhost:9/v1")
	t.Setenv(modelEnvVar, "env-model")
	e = buildEngine(cfg)
	if e.Primary.BaseURL != "http://envhost:9/v1" || e.Primary.Model != "env-model" {
		t.Fatalf("env overrides lost: %+v", e.Primary)
	}
}

func TestBuildEngineFallbackWiring(t *testing.T) {
	clearNeedleEnv(t)
	cfg := config.Defaults()
	cfg.Model = "m1"
	cfg.FallbackEndpoint = "http://ai.tailnet.ts.net:8080/v1"
	e := buildEngine(cfg)
	if e.Fallback == nil {
		t.Fatal("fallback_endpoint set: engine must carry a fallback client")
	}
	if e.Fallback.BaseURL != cfg.FallbackEndpoint {
		t.Fatalf("fallback URL = %q", e.Fallback.BaseURL)
	}
	// No fallback_model anywhere: inherit the primary model name.
	if e.Fallback.Model != "m1" {
		t.Fatalf("fallback model should default to the primary's: %q", e.Fallback.Model)
	}
	if e.Fallback.MaxTokens != cfg.MaxTokens || e.Fallback.HTTP.Timeout != cfg.Timeout {
		t.Fatal("engine budget must apply to the fallback too")
	}
	// Config fallback_model, then env NEEDLE_FALLBACK_MODEL wins over it.
	cfg.FallbackModel = "fb-config"
	e = buildEngine(cfg)
	if e.Fallback.Model != "fb-config" {
		t.Fatalf("fallback_model config lost: %q", e.Fallback.Model)
	}
	t.Setenv(fbModelEnvVar, "fb-env")
	e = buildEngine(cfg)
	if e.Fallback.Model != "fb-env" {
		t.Fatalf("fallback env precedence wrong: %q", e.Fallback.Model)
	}
	// Env fallback URL beats config.
	t.Setenv(fbURLEnvVar, "http://envfb/v1")
	e = buildEngine(cfg)
	if e.Fallback.BaseURL != "http://envfb/v1" {
		t.Fatalf("fallback URL env override lost: %q", e.Fallback.BaseURL)
	}
}

func TestBuildEngineFallbackKeyResolution(t *testing.T) {
	clearNeedleEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "fallback.env")
	if err := os.WriteFile(envFile, []byte("# key for the secondary endpoint\nFB_KEY=sekret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.FallbackEndpoint = "http://x/v1"
	cfg.FallbackKeyFile = envFile
	cfg.FallbackKeyVar = "FB_KEY"
	e := buildEngine(cfg)
	if e.Fallback.APIKey != "sekret-value" {
		t.Fatalf("fallback key not read from key_file/key_var")
	}
	// NEEDLE_FALLBACK_API_KEY env wins over the file.
	t.Setenv(fbKeyEnvVar, "from-env")
	e = buildEngine(cfg)
	if e.Fallback.APIKey != "from-env" {
		t.Fatalf("fallback key env precedence wrong")
	}
	// Missing file variable is fine (some engines need no key).
	t.Setenv(fbKeyEnvVar, "")
	cfg.FallbackKeyVar = "ABSENT"
	e = buildEngine(cfg)
	if e.Fallback.APIKey != "" {
		t.Fatal("absent key var must yield no key, not an error path")
	}
}

// clearNeedleEnv blanks every needle env override so config-only tests
// are not polluted by the developer's shell.
func clearNeedleEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		urlEnvVar, modelEnvVar, keyEnvVar,
		fbURLEnvVar, fbModelEnvVar, fbKeyEnvVar,
	} {
		t.Setenv(name, "")
	}
}

func TestThemePalette(t *testing.T) {
	// Design review: one coherent color system per widget. With a
	// theme file the WHOLE palette (surface, text, prompt, input,
	// border, selection) comes from the M3 colors.json; without one
	// (empty path, missing file, corrupt JSON) we fall back to the
	// Solarized-light set matching fuzzel's built-in cream surface —
	// never a dark selection bar floating on a cream box.
	light := themePalette("")
	if light.Background != "" || light.SelectionBackground != "" {
		t.Fatalf("no theme must leave surface/selection at fuzzel defaults: %+v", light)
	}
	if light.TextColor != lightTextFallback || light.InputColor != lightInputFallback {
		t.Fatalf("no theme must still carry the AA/anti-fusion inks: %+v", light)
	}
	if got := themePalette(filepath.Join(t.TempDir(), "missing.json")); got != light {
		t.Fatalf("missing file must equal the light fallback: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "colors.json")
	colors := map[string]string{
		"surfaceContainer": "#1d2024", "onSurfaceVariant": "#c4c6cf",
		"onSurface": "#e2e2e9", "primary": "#a9c7ff", "outlineVariant": "#44474e",
		"primaryContainer": "#264777", "onPrimaryContainer": "#d6e3ff",
	}
	raw, _ := json.Marshal(colors)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got := themePalette(path)
	// themeColor normalizes to bare hex (fuzzelColor accepts either).
	want := palette.Options{
		Background: "1d2024", TextColor: "c4c6cf", PromptColor: "e2e2e9",
		InputColor: "a9c7ff", BorderColor: "44474e",
		SelectionBackground: "264777", SelectionText: "d6e3ff",
	}
	if got != want {
		t.Fatalf("theme colors not read: %+v want %+v", got, want)
	}
	// Corrupt file: light fallback, not a crash, not a half-themed box.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cg := themePalette(path); cg != light {
		t.Fatalf("corrupt colors file must yield the light fallback: %+v", cg)
	}
	// Partial theme: known keys themed, unknown keys keep the dark-set
	// defaults (the file's presence declares a dark system).
	if err := os.WriteFile(path, []byte(`{"primaryContainer":"#ff0000"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got = themePalette(path)
	if got.SelectionBackground != "ff0000" || got.Background != darkBackgroundFallback {
		t.Fatalf("partial theme wrong: %+v", got)
	}
}

func TestThemeColor(t *testing.T) {
	// matugen emits #aarrggbb; fuzzel wants rrggbbaa.
	if got := themeColor("#ff1d2024"); got != "1d2024ff" {
		t.Errorf("ARGB = %q, want 1d2024ff", got)
	}
	if got := themeColor("#1d2024"); got != "1d2024" {
		t.Errorf("RGB = %q, want 1d2024", got)
	}
	if got := themeColor("  #264777 "); got != "264777" {
		t.Errorf("padded = %q", got)
	}
}

func TestParseCombo(t *testing.T) {
	// Keycodes verified against /usr/include/linux/input-event-codes.h:
	// LEFTCTRL=29 LEFTSHIFT=42 LEFTALT=56; C=46 V=47.
	got, err := parseCombo("ctrl+shift+v")
	if err != nil || len(got) != 3 || got[0] != 29 || got[1] != 42 || got[2] != 47 {
		t.Fatalf("ctrl+shift+v = %v err=%v", got, err)
	}
	got, err = parseCombo("ctrl+c")
	if err != nil || len(got) != 2 || got[1] != 46 {
		t.Fatalf("ctrl+c = %v err=%v", got, err)
	}
	for _, bad := range []string{"v", "ctrl+", "ctrl+f13", "super+v", "", "ctrl+shift"} {
		if _, perr := parseCombo(bad); perr == nil {
			t.Errorf("parseCombo(%q) must fail", bad)
		}
	}
}

func TestPalettePromptMentionsDeliveryMode(t *testing.T) {
	cases := []struct {
		base     string
		mustHave string
		noPaste  bool
	}{
		{"rewrite selection:", "selection", false}, // auto-delivery: says the selection is replaced in place
		{"rewrite selection:", "clipboard", true},  // --no-paste: says the result lands on the clipboard
	}
	for _, c := range cases {
		got := palettePrompt(c.base, c.noPaste)
		if !strings.Contains(strings.ToLower(got), c.mustHave) {
			t.Errorf("palettePrompt(%q, noPaste=%v) = %q, must mention %q", c.base, c.noPaste, got, c.mustHave)
		}
		if !strings.HasPrefix(got, c.base) {
			t.Errorf("palettePrompt must keep the configured base prompt: %q", got)
		}
		// The mode hint must read as an annotation, not as typed filter
		// text: parenthesized, and never starting with an arrow glyph
		// (UX review round: "-> clipboard" was misread as a query).
		hint := strings.TrimPrefix(got, c.base)
		if !strings.HasPrefix(hint, " (") || !strings.HasSuffix(hint, ")") {
			t.Errorf("mode hint %q must be a parenthetical annotation", hint)
		}
	}
	// The shipped default prompt carries a trailing space: the composed
	// line must not double up on it.
	got := palettePrompt("rewrite selection: ", true)
	if strings.Contains(got, "  ") {
		t.Errorf("palettePrompt must collapse the base trailing space: %q", got)
	}
	if got != "rewrite selection: (to clipboard)" {
		t.Errorf("composed = %q", got)
	}
}

func TestExtractCombos(t *testing.T) {
	rest, paste, cp, noPaste, err := extractCombos([]string{"--paste", "ctrl+shift+v", "pick", "--copy", "ctrl+shift+c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0] != "pick" {
		t.Fatalf("rest=%v", rest)
	}
	if len(paste) != 3 || paste[2] != 47 || len(cp) != 3 || cp[2] != 46 {
		t.Fatalf("paste=%v copy=%v", paste, cp)
	}
	if noPaste {
		t.Fatal("noPaste must default false")
	}
	rest, _, _, noPaste, err = extractCombos([]string{"pick", "--no-paste"})
	if err != nil || noPaste != true || len(rest) != 1 || rest[0] != "pick" {
		t.Fatalf("--no-paste: rest=%v noPaste=%v err=%v", rest, noPaste, err)
	}
	if _, _, _, _, e2 := extractCombos([]string{"--paste", "bogus", "pick"}); e2 == nil {
		t.Fatal("bad combo must error")
	}
	if _, _, _, _, e3 := extractCombos([]string{"--paste"}); e3 == nil {
		t.Fatal("missing value must error")
	}
}

func TestExtractHold(t *testing.T) {
	cases := []struct {
		wantDur string
		in      []string
		want    []string
	}{
		{wantDur: "5s", in: []string{"-hold", "5s", "proofread"}, want: []string{"proofread"}},
		{wantDur: "5s", in: []string{"proofread", "-hold", "5s"}, want: []string{"proofread"}},
		{wantDur: "0s", in: []string{"proofread"}, want: []string{"proofread"}},
		{wantDur: "0s", in: []string{"-hold"}, want: []string{"-hold"}},
		{wantDur: "0s", in: []string{"-hold", "notaduration", "proofread"}, want: []string{"-hold", "notaduration", "proofread"}},
	}
	for _, tc := range cases {
		got, dur := extractHold(tc.in)
		if dur.String() != tc.wantDur {
			t.Errorf("extractHold(%v) dur = %v, want %v", tc.in, dur, tc.wantDur)
		}
		if len(got) != len(tc.want) {
			t.Errorf("extractHold(%v) args = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("extractHold(%v) args = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}
