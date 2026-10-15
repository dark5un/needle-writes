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
	"path/filepath"
	"strings"
	"time"

	"github.com/dark5un/needle-writes/internal/action"
	"github.com/dark5un/needle-writes/internal/command"
	"github.com/dark5un/needle-writes/internal/palette"
	"github.com/dark5un/needle-writes/internal/strata"
	"github.com/dark5un/needle-writes/internal/waylandctl"
)

const (
	defaultEndpoint = "http://127.0.0.1:11434/v1"
	// serviceEnvPath holds the local engine API key; read at runtime,
	// never embedded, never echoed.
	serviceEnvPath = ".config/containers/config/strata/service.env"
	keyEnvVar      = "NEEDLE_API_KEY"
	urlEnvVar      = "NEEDLE_STRATA_URL"
	modelEnvVar    = "NEEDLE_MODEL"
	// ryokuColorsPath is the live desktop palette (matugen, written by
	// the ryoku-shell daemon). Read at runtime for the palette's
	// selection colors; absence just means fuzzel's own defaults.
	ryokuColorsPath = ".cache/ryoku/colors.json"
	// palettePrompt states the operation and its target (UX review:
	// naming the tool is not enough).
	palettePrompt = "rewrite selection: "
	// paletteWidthChars fits the longest padded item line without
	// mid-word truncation; pinned to the registry's own budget.
	paletteWidthChars = action.PaletteLineWidth
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

// extractCombos pulls --paste/--copy <combo> flags off the args at any
// position (terminals need ctrl+shift+v to paste and ctrl+shift+c to
// copy: plain Ctrl+V is not paste there, and plain Ctrl+C is SIGINT
// to the running program).
func extractCombos(args []string) (rest []string, pasteCombo, copyCombo []uint32, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "--paste" && args[i] != "--copy" {
			rest = append(rest, args[i])
			continue
		}
		val := ""
		if i+1 < len(args) {
			val = args[i+1]
		}
		if val == "" {
			return nil, nil, nil, fmt.Errorf("%s needs a value like ctrl+shift+v", args[i])
		}
		codes, cerr := parseCombo(val)
		if cerr != nil {
			return nil, nil, nil, cerr
		}
		if args[i] == "--paste" {
			pasteCombo = codes
		} else {
			copyCombo = codes
		}
		i++
	}
	return rest, pasteCombo, copyCombo, nil
}

// themeSelection returns the live desktop palette's selection colors
// (background, text) for the fuzzel palette, read at runtime from the
// matugen colors file the ryoku-shell daemon writes. Any failure
// (missing file, unknown shape) yields empty strings: fuzzel's own
// defaults, never a crash — the picker must work on any compositor.
func themeSelection() (bg, text string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	// #nosec G304 -- ryokuColorsPath is a compile-time constant under
	// $HOME; only the home directory itself is variable.
	f, err := os.Open(filepath.Join(home, ryokuColorsPath))
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()
	var colors map[string]string
	if json.NewDecoder(f).Decode(&colors) != nil {
		return "", ""
	}
	return colors["primaryContainer"], colors["onPrimaryContainer"]
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

func run(args []string, hold time.Duration) int {
	args, pasteCombo, copyCombo, cerr := extractCombos(args)
	if cerr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "needle-writes: %v\n", cerr)
		return command.ExitUsage
	}
	wl, err := waylandctl.Connect()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "needle-writes: wayland: %v\n", err)
		return command.ExitWayland
	}
	defer wl.Close()

	endpoint := os.Getenv(urlEnvVar)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	key := os.Getenv(keyEnvVar)
	if key == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			// Missing file is fine: some Strata setups need no key; the
			// engine call will surface any real auth problem.
			if v, kerr := strata.ReadEnvValue(filepath.Join(home, serviceEnvPath), "API_KEY"); kerr == nil {
				key = v
			}
		}
	}
	model := os.Getenv(modelEnvVar)
	if model == "" {
		model = "needle-writes" // Strata answers any name with the loaded model
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
		Served:      func(d time.Duration) bool { return wl.WaitClaimServed(d) },
		Hold:        func(d time.Duration) { wl.PumpFor(d) },
		Owns:        wl.OwnsClipboard,
		Choose: func(items []string) (string, error) {
			selBg, selTx := themeSelection()
			return palette.Fuzzel(ctx, palette.Options{
				Prompt:              palettePrompt,
				WidthChars:          paletteWidthChars,
				SelectionBackground: selBg,
				SelectionText:       selTx,
			}, items)
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
