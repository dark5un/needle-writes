package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestThemeSelection(t *testing.T) {
	// Point HOME at a temp tree with a matugen-style colors.json.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if bg, tx := themeSelection(); bg != "" || tx != "" {
		t.Fatalf("missing colors file must yield fuzzel defaults, got %q %q", bg, tx)
	}
	path := filepath.Join(home, ryokuColorsPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	colors := map[string]string{"primaryContainer": "#264777", "onPrimaryContainer": "#d6e3ff"}
	raw, _ := json.Marshal(colors)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if bg, tx := themeSelection(); bg != "#264777" || tx != "#d6e3ff" {
		t.Fatalf("theme colors not read: %q %q", bg, tx)
	}
	// Corrupt file: defaults, not a crash.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if bg, tx := themeSelection(); bg != "" || tx != "" {
		t.Fatalf("corrupt colors file must yield defaults, got %q %q", bg, tx)
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
