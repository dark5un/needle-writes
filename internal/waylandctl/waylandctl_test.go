// Integration tests for the pure-Go ext-data-control client. They run
// against the live Hyprland compositor on this machine and skip cleanly
// when none is reachable. wl-copy/wl-paste are used ONLY as the external
// verification oracle — the implementation under test never shells out.
package waylandctl

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireCompositor(t *testing.T) *Client {
	t.Helper()
	c, err := Connect()
	if err != nil {
		t.Skipf("no wayland compositor reachable: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func sh(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), args[0], args[1:]...) //nolint:gosec // test helper, literal args only
	if cmd.Args[0] == "wl-copy" {
		// wl-copy daemonizes to serve the selection and the daemon
		// inherits our output pipes; piping would block until it exits.
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return ""
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestReadClipboardMatchesWlPaste(t *testing.T) {
	c := requireCompositor(t)
	want := "needle integration clipboard 12345"
	sh(t, "wl-copy", want)
	got, err := c.ReadClipboard()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ReadClipboard = %q, want %q", got, want)
	}
}

func TestReadPrimaryMatchesWlPastePrimary(t *testing.T) {
	c := requireCompositor(t)
	want := "needle integration primary 67890"
	sh(t, "wl-copy", "--primary", want)
	got, err := c.ReadPrimary()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ReadPrimary = %q, want %q", got, want)
	}
}

func TestEmptyPrimaryReturnsEmptyString(t *testing.T) {
	c := requireCompositor(t)
	sh(t, "wl-copy", "--primary", "") // clear primary
	got, err := c.ReadPrimary()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != "" {
		t.Fatalf("expected empty primary, got %q", got)
	}
}

func TestClaimClipboardReadableByWlPaste(t *testing.T) {
	c := requireCompositor(t)
	want := "claimed by pure go data-control"
	if err := c.ClaimClipboard(want); err != nil {
		t.Fatal(err)
	}
	got := sh(t, "wl-paste", "--no-newline")
	if got != want {
		t.Fatalf("wl-paste after claim = %q, want %q", got, want)
	}
}

func TestUnicodeRoundTrip(t *testing.T) {
	c := requireCompositor(t)
	want := "Γειά σου κόσme 🦜 — accents: äöü"
	if err := c.ClaimClipboard(want); err != nil {
		t.Fatal(err)
	}
	got := sh(t, "wl-paste", "--no-newline")
	if got != want {
		t.Fatalf("unicode round trip = %q, want %q", got, want)
	}
}

func TestConnectFindsCompositorSocket(t *testing.T) {
	// Connect must work with WAYLAND_DISPLAY unset by probing
	// $XDG_RUNTIME_DIR/wayland-* (Hyprland on this machine uses wayland-1).
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		t.Skip("no XDG_RUNTIME_DIR")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	c, err := Connect()
	if err != nil {
		t.Skipf("no compositor to probe: %v", err)
	}
	defer c.Close()
}

// --- reviewer round-1 regressions (deleg_1fe5f4f1/task-1) ---

func TestClaimThenReadOwnClaim(t *testing.T) {
	// Self-serve: reading back a selection this same client claimed must
	// not deadlock the loop (the send request arrives on our own
	// connection). Regression for the reproduced 5s-timeout bug.
	c := requireCompositor(t)
	want := "self-serve round trip 42"
	if err := c.ClaimClipboard(want); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadClipboard()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("read-back of own claim = %q, want %q", got, want)
	}
}

func TestCloseBoundedWithStalledReader(t *testing.T) {
	// A reader that never drains the transfer pipe must not park the loop
	// goroutine: send servicing runs off-loop with a write deadline, so
	// Close() returns bounded. Regression for the reproduced CLOSE HUNG bug.
	c := requireCompositor(t)
	big := strings.Repeat("x", 1<<20) // >64KiB pipe capacity
	if err := c.ClaimClipboard(big); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "wl-paste | sleep 30") //nolint:gosec // fixed test command
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	time.Sleep(500 * time.Millisecond) // let wl-paste connect and stall
	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("Close took %v with a stalled reader; loop must stay free", d)
	}
}

// copyStdin feeds wl-copy via stdin (argv payloads hit the 128KiB
// MAX_ARG_STRLEN wall; big transfers widen the race window the churn
// test needs).
func copyStdin(t *testing.T, s string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "wl-copy") //nolint:gosec // fixed test command
	cmd.Stdin = strings.NewReader(s)
	if err := cmd.Run(); err != nil {
		t.Fatalf("wl-copy stdin: %v", err)
	}
}

func TestSelectionChurnDuringReadNeverSilentEmpty(t *testing.T) {
	// Regression (reviewer round 2, HIGH): a selection event arriving
	// mid-transfer must not destroy the in-flight offer (aborted transfer
	// -> silent 0-byte success). Invariant: with text constantly offered,
	// ReadClipboard never returns ("", nil). 256KiB payloads keep the
	// transfer window wide enough for racers to overlap it.
	c := requireCompositor(t)
	pad := strings.Repeat("v", 256*1024)
	for i := range 5 {
		want := fmt.Sprintf("race value %d %s", i, pad)
		copyStdin(t, want)
		stop := make(chan struct{})
		done := make(chan struct{})
		rctx := t.Context()
		go func() {
			defer close(done)
			for j := 0; j < 3; j++ {
				select {
				case <-stop:
					return
				default:
				}
				cmd := exec.CommandContext(rctx, "wl-copy") //nolint:gosec // fixed test args
				cmd.Stdin = strings.NewReader(fmt.Sprintf("racer %d-%d %s", i, j, pad))
				_ = cmd.Run()
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		}()
		got, err := c.ReadClipboard()
		close(stop)
		<-done // no racer wl-copy may outlive this iteration
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if got == "" {
			t.Fatalf("iteration %d: silent empty read during selection churn", i)
		}
	}
}

func TestClaimThenImmediateWlPaste(t *testing.T) {
	// Regression (reviewer round-3 probe, 1/100 fail without barrier):
	// a reader connecting immediately after ClaimClipboard must see OUR
	// claim, not the previous (possibly dead) selection; claim()
	// round-trips via display.sync before returning.
	c := requireCompositor(t)
	for i := range 20 {
		want := fmt.Sprintf("immediate read %d", i)
		if err := c.ClaimClipboard(want); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if got := sh(t, "wl-paste", "--no-newline"); got != want {
			t.Fatalf("iteration %d: wl-paste = %q, want %q", i, got, want)
		}
	}
}

func TestNullIdSelectionEventHandled(t *testing.T) {
	// Hyprland sends selection/primary_selection with object id 0 for an
	// empty selection; the generated bindings panic on it. The filter must
	// clear state without panicking.
	c := requireCompositor(t)
	if err := c.do(func() error {
		c.dispatch(msg{data: []byte{0, 0, 0, 0}, sender: c.dev.ID(), opcode: 1})
		c.dispatch(msg{data: []byte{0, 0, 0, 0}, sender: c.dev.ID(), opcode: 3})
		if c.cbOffer != nil || c.prOffer != nil {
			t.Error("null-id events must clear the tracked offers")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPickTextMime(t *testing.T) {
	cases := []struct {
		want string
		in   []string
	}{
		{want: "text/plain;charset=utf-8", in: []string{"text/plain", "text/plain;charset=utf-8"}},
		{want: "text/plain", in: []string{"text/plain"}},
		{want: "text/html", in: []string{"image/png", "text/html"}},
		{want: "", in: []string{"image/png"}},
		{want: "", in: nil},
	}
	for _, tc := range cases {
		if got := pickTextMime(tc.in); got != tc.want {
			t.Errorf("pickTextMime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- M2: virtual-keyboard injection ---

func TestKeymapCaptured(t *testing.T) {
	// Connecting must capture the compositor's XKB keymap (bound
	// wl_keyboard receives keymap unconditionally). The virtual keyboard
	// requires it before any key event.
	c := requireCompositor(t)
	if len(c.keymapBytes) == 0 {
		t.Fatal("no keymap captured from compositor")
	}
	if c.keymapFormat != 1 { // WL_KEYMAP_FORMAT_XKB_V1
		t.Fatalf("keymap format = %d, want 1", c.keymapFormat)
	}
}

func TestVirtualKeyboardSetupRoundTrip(t *testing.T) {
	// Create the virtual keyboard and push the keymap; the connection
	// must survive the round-trip (Hyprland kills clients on protocol
	// misuse). NO key events: the user may be AFK; keystrokes would land
	// in whatever window has focus.
	c := requireCompositor(t)
	if !c.HasVirtualKeyboard() {
		t.Skip("compositor lacks zwp_virtual_keyboard_manager_v1")
	}
	if err := c.SetupVirtualKeyboard(); err != nil {
		t.Fatal(err)
	}
	defer c.CloseVirtualKeyboard()
	if err := c.do(func() error { return c.roundtrip() }); err != nil {
		t.Fatalf("connection dead after vk setup: %v", err)
	}
}

func TestPressCombo(t *testing.T) {
	// Real key injection is opt-in: it types into the FOCUSED window.
	// Live verification happens via the Hyprland keybind, not CI.
	if os.Getenv("NEEDLE_ALLOW_KEY_INJECTION") != "1" {
		t.Skip("set NEEDLE_ALLOW_KEY_INJECTION=1 to inject real keystrokes")
	}
	c := requireCompositor(t)
	if err := c.PressCombo(29, 47); err != nil { // Ctrl+V
		t.Fatal(err)
	}
}

func TestSocketPathProbe(t *testing.T) {
	dir := t.TempDir()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "wayland-1"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	p, err := socketPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "wayland-1") {
		t.Fatalf("socketPath probe = %q", p)
	}
	// Stale WAYLAND_DISPLAY must fall back to probing, not fail.
	t.Setenv("WAYLAND_DISPLAY", "wayland-does-not-exist")
	p, err = socketPath()
	if err != nil || p != filepath.Join(dir, "wayland-1") {
		t.Fatalf("socketPath with stale env = %q, err %v", p, err)
	}
}
