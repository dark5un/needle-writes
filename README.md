# needle-writes

Apple-Writing-Tools-style text actions on the Linux desktop: select text,
press a hotkey, get it proofread / shortened / retoned / summarized /
rewritten as a clean agent prompt — served exclusively by a **local**
LLM (any OpenAI-compatible endpoint: Strata, llama.cpp, Ollama-style
servers). No cloud provider, ever.

Built for **Hyprland/Wayland** in **pure Go**: the tool speaks the Wayland
wire protocols in-process (`ext_data_control` for selection read/claim,
`zwp_virtual_keyboard_v1` for keystroke injection). It never shells out
to `wl-paste`, `wl-copy`, or `wtype`. One static binary, `CGO_ENABLED=0`,
~7 MB, no cgo, no resident daemon.

## Why

On Wayland there is no universal text API, so Apple's per-app context
menu is impossible. The equivalent outcome is achievable: global hotkey →
selection capture → local LLM → result written back. This project does
that with one small, auditable binary — nothing keyboard-capable stays
resident, and the whole tree is readable Go behind `go vet` +
`golangci-lint` gates.

## Design decisions (hard requirements)

- **Local AI only.** Default endpoint `http://127.0.0.1:11434/v1`; the
  API key is read at runtime from the environment (`NEEDLE_API_KEY`) or
  a local service env file — never compiled in, never logged.
- **No shell-out.** Wayland protocols spoken directly over the compositor
  socket.
- **One-shot, not a daemon.** Spawned by a compositor keybind; the claim
  lives exactly as long as the delivery needs (and is held briefly longer
  so a slow paste target never meets a dead clipboard owner).
- **Focus-less selection.** `ext-data-control` (the protocol wl-clipboard
  itself uses) — the classic `wl_data_device`/`zwp_primary_selection`
  protocols cannot be used by a background client (input-serial rule).
- **Unicode-safe delivery.** Result is claimed as CLIPBOARD and pasted
  via a single Ctrl+V combo (virtual keyboard); the user's clipboard is
  restored afterwards — also on cancel. Never pastes on an empty capture,
  never restores over a newer run's claim.

## Install

Requirements: a Wayland compositor that implements `ext-data-control`
and `virtual-keyboard` (Hyprland ≥ 0.52 does; wlroots-based compositors
generally do), Go ≥ 1.27 to build, `fuzzel` for the action palette.

```sh
make build                       # -> bin/needle-writes (static)
install -m 755 bin/needle-writes ~/.local/bin/
```

Configuration (all optional):

| Var | Default | Meaning |
|---|---|---|
| `NEEDLE_STRATA_URL` | config `endpoint`, else `http://127.0.0.1:11434/v1` | OpenAI-compatible endpoint |
| `NEEDLE_API_KEY` | config `key_file`, else none | bearer key, read at runtime, never logged |
| `NEEDLE_MODEL` | config `model`, else `needle-writes` | model name sent to the endpoint |

### Config file

Persistent settings go in `$XDG_CONFIG_HOME/needle-writes/config`
(`~/.config/needle-writes/config`): simple `KEY = VALUE` lines, `#`
comments.

```ini
endpoint       = http://127.0.0.1:11434/v1  # OpenAI-compatible base URL
model          = needle-writes              # name sent to the endpoint
key_file       = ~/.config/svc/engine.env   # env-format file holding the API key
key_var        = API_KEY                    # variable name inside key_file
theme_file     = ~/.cache/theme/colors.json # matugen-style colors for the palette
palette_prompt = rewrite selection:
paste_combo    = ctrl+shift+v               # set these for terminal targets
copy_combo     = ctrl+shift+c
```

Precedence: CLI flag > environment > config file > built-in default. An
unknown key in the config file is a startup error — it controls where
the API key is read from, and silent typos are worse than failing loud.
Keys are case-insensitive; values may be quoted to keep trailing
spaces, and ` #` (whitespace-preceded) starts a comment on a value
line — a value that itself begins with `#` must be quoted.

Hook it to a key. On Hyprland with the Lua config (e.g. `user.lua`):

```lua
hl.bind("SUPER + SHIFT + O", hl.dsp.exec_cmd("needle-writes pick"))
```

or legacy `hyprland.conf`:

```
bind = SUPER SHIFT, O, exec, needle-writes pick
```

## Usage

```
SUPER+SHIFT+O        # palette: capture selection, pick an action, result pasted back
needle-writes pick   # the palette subcommand
needle-writes proofread
needle-writes shorten
needle-writes professional
needle-writes casual
needle-writes summarize
needle-writes translate-en
needle-writes translate-el
needle-writes agentify    # rough text or draft prompt -> clear agent instruction
needle-writes agentify+   # ...expanded: context, steps, acceptance criteria
needle-writes -list       # list actions
```

Flags: `-hold <dur>` (keep the result claim alive for a manual paste),
`--paste <combo>` / `--copy <combo>` (override the injected combos),
`--no-paste` (never inject a paste: the result stays on the clipboard
for you to paste yourself — see Terminal targets). The palette prompt
states the active delivery mode: `(replace selection)` vs `(to clipboard)`.

Direct action binds are still possible (one `bind` per action); the
palette is the one-key path. The palette is fuzzel (v0); a native
layer-shell palette is planned.

The `agentify` pair doubles as AI-harness tooling: dictate or scribble a
half-formed prompt, select it, and get back the same intent worded the
way LLMs follow best — `agentify+` turns it into a full brief with
verifiable acceptance criteria.

## Ryoku & portability notes

This tool was built and tested on **Ryoku** (Hyprland + the Quickshell
shell), and it leans on a few Ryoku conveniences — all strictly
best-effort, so it works on any Hyprland (≥ 0.52) that provides
`ext-data-control` and `virtual-keyboard`:

- No installation-specific paths are compiled in: the engine endpoint,
  the API-key file, the palette theme file, the prompt and the injection
  combos all come from the config file, and every one of them has a
  sane default or a graceful fallback (no `theme_file` leaves the
  surface at fuzzel's own colors, with AA-contrast ink fallbacks
  applied on top).
- The clipboard-restore logic is content-aware: it behaves correctly
  whether or not a clipboard history watcher (the Ryoku shell,
  `wl-copy -w`, and similar managers re-own every selection) is
  running.

Other compositors: anything implementing both protocols works in
principle; only Hyprland is tested.

### Terminal targets

In terminal emulators plain Ctrl+V is **not** paste (kitty and many
others use Ctrl+Shift+V), and the Ctrl+C capture fallback would send
SIGINT to the running program. Terminals that set PRIMARY on selection
(kitty, foot, alacritty…) still capture fine; only the paste combo
differs.

The robust mode for terminals and modal editors (vim changes mode on
injected keystrokes) is **`--no-paste`**: the result is claimed as
CLIPBOARD and left there — no keystrokes are injected at all, nothing
is auto-pasted, and the clipboard is NOT restored (the result is the
point). A desktop notice closes the loop: `proofread → clipboard in
1.9s` on success, `rewrite failed — clipboard unchanged` on failure —
so silence means the engine is still working. Paste it yourself where
you want it:

```lua
-- Lua config (e.g. Ryoku's user.lua)
hl.bind("SUPER + SHIFT + I", hl.dsp.exec_cmd("needle-writes pick --no-paste --copy ctrl+shift+c"))
```

```
# legacy hyprland.conf
bind = SUPER SHIFT, I, exec, needle-writes pick --no-paste --copy ctrl+shift+c
```

(`--copy ctrl+shift+c` keeps the empty-PRIMARY capture fallback usable
in terminals; it is capture, not delivery.) Know the consequences: your
previous clipboard is gone, and with a clipboard-history manager
running the result is persisted into that history like any copy.
Without one, the claim is served live for ~10 s after the result is
claimed — paste inside that window. If your terminal does take injected
Ctrl+Shift+V well, the auto-paste form works too:

```
needle-writes pick --paste ctrl+shift+v --copy ctrl+shift+c
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Result claimed (pasted via the paste combo in default mode; left on the clipboard with `--no-paste`) |
| 1 | Engine call failed or returned an empty result (nothing pasted) |
| 2 | Nothing selected — select or copy text first (nothing pasted) |
| 3 | Selection larger than the model budget |
| 4 | Wayland/compositor failure (selection, claim, injection, or palette) |
| 5 | Palette dismissed (Esc) — nothing pasted |
| 64 | Usage error |

## Status

Done: pure-Go selection read/claim, local-engine round-trip,
virtual-keyboard capture fallback + paste delivery with clipboard
restore (including cancel and supersede races), and the fuzzel action
palette. Next: a native palette, per-user custom actions from a config
file, and separate deadlines for palette think-time vs the engine
budget.

## Development

Strict TDD (RED→GREEN per behavior) with per-item gates and an
independent review pass before every `[verified]` commit:

```sh
./tools/test.sh   # gofmt + go vet + golangci-lint + gopls check + race tests
```

Integration tests (`internal/waylandctl`) run against the live
compositor and skip cleanly when no Wayland session is reachable.

## License

MIT
