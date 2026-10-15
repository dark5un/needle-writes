# needle-writes

Apple-Writing-Tools-style text actions on the Linux desktop: select text,
press a hotkey, get it proofread / shortened / retone / summarized — served
exclusively by a **local** LLM (Strata, OpenAI-compatible endpoint on this
machine). No cloud provider, ever.

Built for **Hyprland/Wayland** in **pure Go**: the tool speaks the Wayland
wire protocols in-process (`ext_data_control_unstable_v1` for selection
read/claim, `zwp_virtual_keyboard_v1` for keystroke injection). It never
shells out to `wl-paste`, `wl-copy`, or `wtype`.

## Why

On Wayland there is no universal text API, so Apple's per-app context menu
is impossible. The equivalent outcome is achievable: global hotkey →
selection capture → local LLM → result written back. This project does that
with one small, auditable, static binary — no resident daemon, no
third-party runtime with keyboard-level privileges.

## Design decisions (hard requirements)

- **Local AI only.** Strata at `http://127.0.0.1:11434/v1`; the API key is
  read at runtime from the ai-lab-quadlets service env file or the
  environment — never compiled in, never logged.
- **No shell-out.** Wayland protocols spoken directly. The whole binary is
  readable Go; `CGO_ENABLED=0` static build.
- **One-shot, not a daemon.** Spawned by a Hyprland `bind`; nothing
  keyboard-capable stays resident.
- **Focus-less selection.** `ext-data-control` (the protocol wl-clipboard
  itself uses) — the classic `wl_data_device`/`zwp_primary_selection`
  protocols cannot be used by a background client (input-serial rule).
- **Unicode-safe delivery.** Result is claimed as CLIPBOARD and pasted via
  a single Ctrl+V combo (virtual keyboard); the user's clipboard is
  restored afterwards. Never pastes on an empty capture.

## Usage (target UX; being built)

```
SUPER+SHIFT+O        # palette (planned)
needle-writes proofread
needle-writes shorten
needle-writes professional
needle-writes casual
needle-writes summarize
needle-writes translate-en
needle-writes translate-el
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Result claimed (and pasted via Ctrl+V when injection is available) |
| 1 | Engine call failed or returned an empty result (nothing pasted) |
| 2 | Nothing selected — select or copy text first (nothing pasted) |
| 3 | Selection larger than the model budget |
| 4 | Wayland/compositor failure (selection, claim, or injection) |
| 64 | Usage error |

## Status

M0 (pure-Go selection read/claim), M1 (Strata round-trip) and M2
(virtual-keyboard Ctrl+C capture fallback + Ctrl+V delivery with
clipboard restore) are implemented. Roadmap:
`~/Documents/plans/writing-tools-plan.md`.

## Development

Strict TDD (RED→GREEN per behavior) with per-item gates:

```sh
./tools/test.sh   # gofmt + go vet + golangci-lint + gopls check + go test
```

Integration tests (`internal/waylandctl`) run against the live Hyprland
compositor and skip cleanly when no Wayland session is reachable.

## License

MIT
