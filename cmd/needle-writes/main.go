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
	// mid-word truncation (12 name + 3 separator + 53 description).
	paletteWidthChars = 70
)

func main() {
	args, hold := extractHold(os.Args[1:])
	os.Exit(run(args, hold))
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
		Served:      func(d time.Duration) bool { return wl.WaitClaimServed(d) },
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
