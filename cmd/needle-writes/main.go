// Command needle-writes runs one writing action end to end: capture the
// selection over ext-data-control, transform it with the local Strata
// engine, and claim the result as the clipboard selection — all in-process,
// no wl-clipboard/wtype shell-out. Spawned one-shot by a Hyprland bind.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dark5un/needle-writes/internal/action"
	"github.com/dark5un/needle-writes/internal/command"
	"github.com/dark5un/needle-writes/internal/config"
	"github.com/dark5un/needle-writes/internal/palette"
	"github.com/dark5un/needle-writes/internal/strata"
	"github.com/dark5un/needle-writes/internal/waylandctl"
)

// Environment overrides (highest precedence after CLI flags); the rest
// of the settings live in the config file (internal/config): NO
// installation-specific paths are compiled in.
const (
	keyEnvVar   = "NEEDLE_API_KEY"
	urlEnvVar   = "NEEDLE_STRATA_URL"
	modelEnvVar = "NEEDLE_MODEL"
	// paletteWidthChars fits the longest padded item line without
	// mid-word truncation; pinned to the registry's own budget.
	paletteWidthChars = action.PaletteLineWidth
)

// Palette ink fallbacks (UX + design reviews): without a theme file the
// palette sits on fuzzel's built-in Solarized-light cream, so the
// fallback inks are Solarized values that clear WCAG AA and keep typed
// input distinct from the prompt. With a theme file the whole widget is
// themed from it (one design system, never a mix).
const (
	lightTextFallback  = "586e75"
	lightInputFallback = "073642"
	// Dark-set defaults, matching the Ryoku M3 ramp shape; used per
	// key when a theme file exists but lacks that key. Bare hex:
	// fuzzelColor adds the alpha.
	darkBackgroundFallback = "1d2024"
	darkTextFallback       = "c4c6cf"
	darkPromptFallback     = "e2e2e9"
	darkInputFallback      = "a9c7ff"
	darkBorderFallback     = "44474e"
	darkSelBgFallback      = "264777"
	darkSelTxFallback      = "d6e3ff"
)

func main() {
	args, hold := extractHold(os.Args[1:])
	os.Exit(run(args, hold))
}

// parseCombo parses "ctrl+shift+v"-style key specs into evdev keycodes
// (constants verified against /usr/include/linux/input-event-codes.h).
func parseCombo(s string) ([]uint32, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	if len(parts) < 2 {
		return nil, fmt.Errorf("combo %q needs a modifier and a key, e.g. ctrl+shift+v", s)
	}
	mods := map[string]uint32{"ctrl": 29, "shift": 42, "alt": 56}
	keys := map[string]uint32{
		"q": 16, "w": 17, "e": 18, "r": 19, "t": 20, "y": 21, "u": 22,
		"i": 23, "o": 24, "p": 25, "a": 30, "s": 31, "d": 32, "f": 33,
		"g": 34, "h": 35, "j": 36, "k": 37, "l": 38, "z": 44, "x": 45,
		"c": 46, "v": 47, "b": 48, "n": 49, "m": 50,
	}
	out := make([]uint32, 0, len(parts))
	for i, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("combo %q has an empty part", s)
		}
		if i == len(parts)-1 {
			code, ok := keys[p]
			if !ok {
				return nil, fmt.Errorf("combo %q: unsupported key %q (a-z)", s, p)
			}
			out = append(out, code)
			continue
		}
		code, ok := mods[p]
		if !ok {
			return nil, fmt.Errorf("combo %q: unsupported modifier %q (ctrl/shift/alt)", s, p)
		}
		out = append(out, code)
	}
	return out, nil
}

// extractCombos pulls --paste/--copy <combo> flags and the --no-paste
// flag off the args at any position (terminals need ctrl+shift+v to
// paste and ctrl+shift+c to copy: plain Ctrl+V is not paste there, and
// plain Ctrl+C is SIGINT to the running program; --no-paste skips the
// paste keystroke entirely and leaves the result on the clipboard).
func extractCombos(args []string) (rest []string, pasteCombo, copyCombo []uint32, noPaste bool, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--no-paste" {
			noPaste = true
			continue
		}
		if args[i] != "--paste" && args[i] != "--copy" {
			rest = append(rest, args[i])
			continue
		}
		val := ""
		if i+1 < len(args) {
			val = args[i+1]
		}
		if val == "" {
			return nil, nil, nil, false, fmt.Errorf("%s needs a value like ctrl+shift+v", args[i])
		}
		codes, cerr := parseCombo(val)
		if cerr != nil {
			return nil, nil, nil, false, cerr
		}
		if args[i] == "--paste" {
			pasteCombo = codes
		} else {
			copyCombo = codes
		}
		i++
	}
	return rest, pasteCombo, copyCombo, noPaste, nil
}

// themePalette reads a matugen-style colors.json (path from the user's
// config file) and returns the palette's full color set: surface,
// item text, prompt, typed input, border and selection row. Design
// review: one coherent system per widget — with a theme file the whole
// palette is themed (dark M3 set); without one (empty path, missing
// file, corrupt JSON) only the Solarized-light ink fallbacks apply on
// top of fuzzel's own cream defaults. Never a crash, never a half-
// themed box: the picker must work on any compositor.
func themePalette(path string) palette.Options {
	if path == "" {
		return lightPalette()
	}
	// #nosec G304 -- path comes from the user's own config file, the
	// trust boundary is the config itself.
	f, err := os.Open(path)
	if err != nil {
		return lightPalette()
	}
	defer func() { _ = f.Close() }()
	var colors map[string]string
	if derr := json.NewDecoder(f).Decode(&colors); derr != nil {
		return lightPalette()
	}
	pick := func(key, fb string) string {
		if v, ok := colors[key]; ok && v != "" {
			return themeColor(v)
		}
		return fb // dark-set fallbacks are already bare hex
	}
	return palette.Options{
		Background:          pick("surfaceContainer", darkBackgroundFallback),
		TextColor:           pick("onSurfaceVariant", darkTextFallback),
		PromptColor:         pick("onSurface", darkPromptFallback),
		InputColor:          pick("primary", darkInputFallback),
		BorderColor:         pick("outlineVariant", darkBorderFallback),
		SelectionBackground: pick("primaryContainer", darkSelBgFallback),
		SelectionText:       pick("onPrimaryContainer", darkSelTxFallback),
	}
}

// themeColor normalizes a matugen colors.json value for fuzzel:
// matugen emits #aarrggbb (ARGB), fuzzel wants rrggbbaa. Six-digit
// values pass through (fuzzelColor adds the alpha). Unrecognized
// shapes are returned as-is and dropped downstream by normalization.
func themeColor(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "#")
	if len(v) == 8 {
		return v[2:] + v[:2] // aarrggbb -> rrggbbaa
	}
	return v
}

// lightPalette keeps the AA/anti-fusion inks while leaving surface and
// selection at fuzzel's built-in Solarized-light defaults.
func lightPalette() palette.Options {
	return palette.Options{
		TextColor:  lightTextFallback,
		InputColor: lightInputFallback,
	}
}

// extractHold pulls a "-hold <duration>" pair off the args at any
// position (before or after the action name). While the process lives,
// its clipboard claim is served in-process; holding lets the user paste
// the result before M2's virtual-keyboard Ctrl+V delivery lands.
func extractHold(args []string) ([]string, time.Duration) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-hold" {
			if d, err := time.ParseDuration(args[i+1]); err == nil {
				out := append(append([]string{}, args[:i]...), args[i+2:]...)
				return out, d
			}
		}
	}
	return args, 0
}

// palettePrompt decorates the configured base prompt with the delivery
// mode, so the palette itself tells the user what picking an action
// will do: replace the selection (auto-paste) or leave the result on
// the clipboard (--no-paste, paste it yourself). The hint is
// parenthetical: a bare suffix after the colon reads as a typed filter
// query (UX review), parentheses read as an annotation.
func palettePrompt(base string, noPaste bool) string {
	base = strings.TrimRight(base, " ") // configured prompts carry a trailing space; do not double up
	if noPaste {
		return base + " (to clipboard)"
	}
	return base + " (replace selection)"
}

func run(args []string, hold time.Duration) int {
	args, pasteCombo, copyCombo, noPaste, cerr := extractCombos(args)
	if cerr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "needle-writes: %v\n", cerr)
		return command.ExitUsage
	}
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		// A malformed config (typo'd key, bad line) fails loudly and
		// before any Wayland contact: it controls where API keys are
		// read from.
		_, _ = fmt.Fprintf(os.Stderr, "needle-writes: config: %v\n", cfgErr)
		return command.ExitUsage
	}
	// Config-file combos apply only where no flag overrides them.
	if pasteCombo == nil && cfg.PasteCombo != "" {
		codes, perr := parseCombo(cfg.PasteCombo)
		if perr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "needle-writes: config paste_combo: %v\n", perr)
			return command.ExitUsage
		}
		pasteCombo = codes
	}
	if copyCombo == nil && cfg.CopyCombo != "" {
		codes, perr := parseCombo(cfg.CopyCombo)
		if perr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "needle-writes: config copy_combo: %v\n", perr)
			return command.ExitUsage
		}
		copyCombo = codes
	}
	if noPaste && pasteCombo != nil {
		// Reviewer nit: migrating a bind to --no-paste leaves a stale
		// --paste flag or paste_combo config behind; say so instead
		// of silently ignoring it. Checked after config resolution so
		// both sources warn.
		_, _ = fmt.Fprintln(os.Stderr, "needle-writes: --no-paste wins: the paste combo is unused this run")
	}
	wl, err := waylandctl.Connect()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "needle-writes: wayland: %v\n", err)
		return command.ExitWayland
	}
	defer wl.Close()

	endpoint := os.Getenv(urlEnvVar)
	if endpoint == "" {
		endpoint = cfg.Endpoint
	}
	key := os.Getenv(keyEnvVar)
	if key == "" && cfg.KeyFile != "" {
		// Missing file/variable is fine: some local engines need no
		// key; the engine call will surface any real auth problem.
		if v, kerr := strata.ReadEnvValue(cfg.KeyFile, cfg.KeyVar); kerr == nil {
			key = v
		}
	}
	model := os.Getenv(modelEnvVar)
	if model == "" {
		model = cfg.Model // local engines answer any name with the loaded model
	}
	client := strata.New(endpoint, key, model)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second) // one explicit budget for the whole one-shot
	defer cancel()

	deps := command.Deps{
		ReadPrimary: wl.ReadPrimary,
		ReadClip:    wl.ReadClipboard,
		Claim:       wl.ClaimClipboard,
		Inject:      wl.PressCombo,
		CopyCombo:   copyCombo,
		PasteCombo:  pasteCombo,
		NoPaste:     noPaste,
		Notify: func(body string) {
			// Best-effort desktop notice for invisible failures in
			// --no-paste mode (UX review). Own timeout: the run ctx
			// may already be spent. notify-send is a UI helper like
			// fuzzel, not a Wayland protocol we must speak in-process.
			nctx, ncancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ncancel()
			// #nosec G204 -- body is a compile-time constant string
			_ = exec.CommandContext(nctx, "notify-send", "-a", "needle-writes", "needle-writes", body).Run()
		},
		Served: func(d time.Duration) bool { return wl.WaitClaimServed(d) },
		Hold:   func(d time.Duration) { wl.PumpFor(d) },
		Owns:   wl.OwnsClipboard,
		Choose: func(items []string) (string, error) {
			opts := themePalette(cfg.ThemeFile)
			opts.Prompt = palettePrompt(cfg.PalettePrompt, noPaste)
			opts.WidthChars = paletteWidthChars
			return palette.Fuzzel(ctx, opts, items)
		},
		Chat: func(ctx context.Context, msgs []action.Message, temperature float64) (string, error) {
			sm := make([]strata.Message, len(msgs))
			for i, m := range msgs {
				sm[i] = strata.Message{Role: m.Role, Content: m.Content}
			}
			return client.Chat(ctx, sm, temperature)
		},
	}
	code := command.Run(ctx, args, os.Stdout, os.Stderr, deps)
	if code == command.ExitOK && hold > 0 {
		// Stay alive serving the claim so a manual Ctrl+V can land it.
		time.Sleep(hold)
	}
	return code
}
