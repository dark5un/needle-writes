// Package config loads needle-writes settings from a simple KEY=VALUE
// file at $XDG_CONFIG_HOME/needle-writes/config (~/.config by default).
// Precedence everywhere: CLI flag > environment > config file > built-in
// default. The code carries NO installation-specific paths: local engine
// key files, theme files and combo overrides all live in the config file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved settings surface.
type Config struct {
	Endpoint      string // OpenAI-compatible base URL
	Model         string // model name sent to the endpoint
	KeyFile       string // env-format file holding the API key ("" = none)
	KeyVar        string // variable name inside KeyFile
	ThemeFile     string // matugen-style colors.json for palette colors ("" = fuzzel defaults)
	PalettePrompt string // palette prompt text
	PasteCombo    string // e.g. "ctrl+shift+v" ("" = default Ctrl+V)
	CopyCombo     string // e.g. "ctrl+shift+c" ("" = default Ctrl+C)

	// Optional secondary endpoint, used ONLY when the primary fails
	// (mac parity). Empty FallbackEndpoint keeps the local-only
	// shipped default: nothing ever leaves the machine.
	FallbackEndpoint string // any OpenAI-compatible URL (e.g. a tailscale peer)
	FallbackModel    string // model name for the fallback ("" = inherit the primary's)
	FallbackKeyFile  string // env-format file holding the fallback key ("" = none)
	FallbackKeyVar   string // variable name inside FallbackKeyFile

	// Value fields last (fieldalignment): engine budget per request.
	MaxTokens int           // generated-token budget per request
	Timeout   time.Duration // per-request engine deadline
}

// Defaults for a stock install: llama.cpp-style local endpoint, no key
// file, no theme file (fuzzel defaults), standard combos, and the
// mac-parity engine budget (4096 output tokens, 120 s per request).
func Defaults() Config {
	return Config{
		Endpoint:      "http://127.0.0.1:11434/v1",
		Model:         "needle-writes",
		KeyVar:        "API_KEY",
		PalettePrompt: "rewrite selection: ",
		MaxTokens:     4096,
		Timeout:       120 * time.Second,
	}
}

// Path returns the config file location: $XDG_CONFIG_HOME/needle-writes/config,
// falling back to ~/.config; "" when neither exists (no home: no config).
func Path() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "needle-writes", "config")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "needle-writes", "config")
	}
	return ""
}

// known keys of the config file. Setters may fail on malformed values
// (a non-numeric budget or a zero timeout is a typo worth failing
// loud on, same philosophy as unknown keys).
var known = map[string]func(*Config, string) error{
	"endpoint":       func(c *Config, v string) error { c.Endpoint = v; return nil },
	"model":          func(c *Config, v string) error { c.Model = v; return nil },
	"key_file":       func(c *Config, v string) error { c.KeyFile = ExpandHome(v); return nil },
	"key_var":        func(c *Config, v string) error { c.KeyVar = v; return nil },
	"theme_file":     func(c *Config, v string) error { c.ThemeFile = ExpandHome(v); return nil },
	"palette_prompt": func(c *Config, v string) error { c.PalettePrompt = v; return nil },
	"paste_combo":    func(c *Config, v string) error { c.PasteCombo = v; return nil },
	"copy_combo":     func(c *Config, v string) error { c.CopyCombo = v; return nil },
	"max_tokens":     setMaxTokens,
	"timeout":        setTimeout,
	"fallback_endpoint": func(c *Config, v string) error {
		c.FallbackEndpoint = v
		return nil
	},
	"fallback_model": func(c *Config, v string) error {
		c.FallbackModel = v
		return nil
	},
	"fallback_key_file": func(c *Config, v string) error {
		c.FallbackKeyFile = ExpandHome(v)
		return nil
	},
	"fallback_key_var": func(c *Config, v string) error {
		c.FallbackKeyVar = v
		return nil
	},
}

// setMaxTokens parses the generated-token budget; out-of-range values
// clamp (bounds mirror the mac parser: 1..32768), garbage errors.
func setMaxTokens(c *Config, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("max_tokens: expected an integer, got %q", v)
	}
	c.MaxTokens = min(max(n, 1), 32768)
	return nil
}

// setTimeout parses the per-request engine deadline in seconds.
// Seconds keep parity with the mac config's `timeout` key; the value
// must be positive — a zero deadline kills every request outright.
func setTimeout(c *Config, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("timeout: expected whole seconds, got %q", v)
	}
	if n <= 0 {
		return fmt.Errorf("timeout: must be a positive number of seconds, got %d", n)
	}
	c.Timeout = time.Duration(n) * time.Second
	return nil
}

// Load reads the config file over the defaults. A missing file is fine.
// An unknown key is an error: silent typos in a config that controls
// where API keys are read from are worse than a startup failure.
func Load() (Config, error) {
	cfg := Defaults()
	path := Path()
	if path == "" {
		return cfg, nil // no home: defaults, never fatal
	}
	// #nosec G304 -- path is XDG_CONFIG_HOME or $HOME joined with a
	// fixed relative component; only the base is variable.
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return cfg, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, i+1, line)
		}
		k = strings.ToLower(strings.TrimSpace(k)) // keys are case-insensitive
		set, knownKey := known[k]
		if !knownKey {
			return cfg, fmt.Errorf("%s:%d: unknown key %q", path, i+1, k)
		}
		val, verr := parseValue(strings.TrimSpace(v))
		if verr != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, i+1, verr)
		}
		if serr := set(&cfg, val); serr != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, i+1, serr)
		}
	}
	if cfg.KeyVar == "" {
		cfg.KeyVar = "API_KEY"
	}
	if cfg.FallbackKeyVar == "" {
		cfg.FallbackKeyVar = "API_KEY"
	}
	if cfg.PalettePrompt == "" {
		cfg.PalettePrompt = Defaults().PalettePrompt
	}
	return cfg, nil
}

// parseValue resolves one VALUE token: quoted strings keep their inner
// spaces verbatim (and may be followed by a comment); unquoted values
// end at a whitespace-preceded # (so URL fragments survive).
func parseValue(v string) (string, error) {
	if strings.HasPrefix(v, `"`) {
		end := strings.Index(v[1:], `"`)
		if end < 0 {
			return "", fmt.Errorf("unterminated quoted value %q", v)
		}
		val := v[1 : 1+end]
		if trail := strings.TrimSpace(v[2+end:]); trail != "" && !strings.HasPrefix(trail, "#") {
			return "", fmt.Errorf("trailing %q after quoted value", trail)
		}
		return val, nil
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v), nil
}

// ExpandHome resolves a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}
