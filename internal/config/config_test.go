package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "needle-writes")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFileYieldsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "http://127.0.0.1:11434/v1" || cfg.Model != "needle-writes" || cfg.KeyVar != "API_KEY" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.KeyFile != "" || cfg.ThemeFile != "" {
		t.Fatalf("no installation-specific paths by default: %+v", cfg)
	}
}

func TestFileOverrides(t *testing.T) {
	writeCfg(t, `# local install
endpoint = http://127.0.0.1:8080/v1
model = my-model
key_file = ~/.config/svc/service.env
key_var = MY_KEY
theme_file = ~/.cache/theme/colors.json
palette_prompt = "writing tool:"
paste_combo = ctrl+shift+v
copy_combo = ctrl+shift+c
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "http://127.0.0.1:8080/v1" || cfg.Model != "my-model" || cfg.KeyVar != "MY_KEY" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	home, _ := os.UserHomeDir()
	if cfg.KeyFile != filepath.Join(home, ".config/svc/service.env") {
		t.Fatalf("key_file tilde not expanded: %q", cfg.KeyFile)
	}
	if cfg.ThemeFile != filepath.Join(home, ".cache/theme/colors.json") {
		t.Fatalf("theme_file tilde not expanded: %q", cfg.ThemeFile)
	}
	if cfg.PalettePrompt != "writing tool:" || cfg.PasteCombo != "ctrl+shift+v" || cfg.CopyCombo != "ctrl+shift+c" {
		t.Fatalf("string overrides not applied: %+v", cfg)
	}
}

func TestUnknownKeyFails(t *testing.T) {
	// A typo in the key that points at the API-key file must be loud.
	writeCfg(t, "endpoint = x\nkeyfile = ~/.evil\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("err=%v, want unknown-key error", err)
	}
}

func TestMalformedLineFails(t *testing.T) {
	writeCfg(t, "endpoint http://no-equals\n")
	if _, err := Load(); err == nil {
		t.Fatal("KEY=VALUE violation must error")
	}
}

func TestEmptyPromptKeepsDefault(t *testing.T) {
	writeCfg(t, "palette_prompt =\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PalettePrompt != Defaults().PalettePrompt {
		t.Fatalf("empty prompt must fall back to default, got %q", cfg.PalettePrompt)
	}
}

func TestInlineCommentsAndQuotedValues(t *testing.T) {
	// The README example uses trailing comments; copy-paste must work.
	writeCfg(t, `endpoint = http://x/v1  # trailing comment
model = "spaced model "
key_file = "~/.env"  # comment after quotes
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "http://x/v1" {
		t.Fatalf("inline comment not stripped: %q", cfg.Endpoint)
	}
	if cfg.Model != "spaced model " {
		t.Fatalf("quoted value must keep inner spaces verbatim: %q", cfg.Model)
	}
	home, _ := os.UserHomeDir()
	if cfg.KeyFile != filepath.Join(home, ".env") {
		t.Fatalf("comment after quoted value not handled: %q", cfg.KeyFile)
	}
	// URL fragments (no space before #) must survive.
	writeCfg(t, "endpoint = http://x/v1#frag\n")
	cfg, _ = Load()
	if cfg.Endpoint != "http://x/v1#frag" {
		t.Fatalf("URL fragment mangled: %q", cfg.Endpoint)
	}
}

func TestUnterminatedQuoteFails(t *testing.T) {
	writeCfg(t, "model = \"oops\n")
	if _, err := Load(); err == nil {
		t.Fatal("unterminated quoted value must error")
	}
}

func TestNoHomeYieldsDefaults(t *testing.T) {
	// No XDG_CONFIG_HOME and no HOME: Path() is empty, Load falls back
	// to defaults, never fatal.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if p := Path(); p != "" {
		t.Fatalf("no XDG/HOME must yield empty path, got %q", p)
	}
	cfg, err := Load()
	if err != nil || cfg.Endpoint != Defaults().Endpoint {
		t.Fatalf("Load without home: %+v err=%v", cfg, err)
	}
}

func TestKeyFileWithoutKeyVarKeepsDefault(t *testing.T) {
	writeCfg(t, "key_file = ~/x.env\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeyVar != "API_KEY" {
		t.Fatalf("KeyVar default lost when only key_file is set: %q", cfg.KeyVar)
	}
}
