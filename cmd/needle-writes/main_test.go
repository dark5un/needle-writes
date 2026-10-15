package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestThemeSelection(t *testing.T) {
	// "" disables theming: fuzzel defaults, never a crash.
	if bg, tx := themeSelection(""); bg != "" || tx != "" {
		t.Fatalf("empty path must yield defaults, got %q %q", bg, tx)
	}
	if bg, tx := themeSelection(filepath.Join(t.TempDir(), "missing.json")); bg != "" || tx != "" {
		t.Fatalf("missing file must yield defaults, got %q %q", bg, tx)
	}
	path := filepath.Join(t.TempDir(), "colors.json")
	colors := map[string]string{"primaryContainer": "#264777", "onPrimaryContainer": "#d6e3ff"}
	raw, _ := json.Marshal(colors)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if bg, tx := themeSelection(path); bg != "#264777" || tx != "#d6e3ff" {
		t.Fatalf("theme colors not read: %q %q", bg, tx)
	}
	// Corrupt file: defaults, not a crash.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if bg, tx := themeSelection(path); bg != "" || tx != "" {
		t.Fatalf("corrupt colors file must yield defaults, got %q %q", bg, tx)
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

func TestExtractCombos(t *testing.T) {
	rest, paste, cp, err := extractCombos([]string{"--paste", "ctrl+shift+v", "pick", "--copy", "ctrl+shift+c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0] != "pick" {
		t.Fatalf("rest=%v", rest)
	}
	if len(paste) != 3 || paste[2] != 47 || len(cp) != 3 || cp[2] != 46 {
		t.Fatalf("paste=%v copy=%v", paste, cp)
	}
	if _, _, _, e2 := extractCombos([]string{"--paste", "bogus", "pick"}); e2 == nil {
		t.Fatal("bad combo must error")
	}
	if _, _, _, e3 := extractCombos([]string{"--paste"}); e3 == nil {
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
