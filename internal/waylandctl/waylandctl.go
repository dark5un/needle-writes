// Package waylandctl is a pure-Go client for the ext-data-control Wayland
// protocol (staging): it reads CLIPBOARD and PRIMARY and claims CLIPBOARD
// without shelling out to wl-copy/wl-paste and without needing keyboard
// focus. It speaks the wire protocol directly via
// github.com/SpinningVinyl/go-wayland.
//
// Concurrency model: the wayland Context is NOT safe for concurrent use, so
// exactly one goroutine (the loop) owns it. Incoming messages are read by a
// reader goroutine (net.UnixConn allows one concurrent reader + writer) and
// handed to the loop, which dispatches them and runs protocol operations
// queued by public methods. This keeps events flowing (e.g. servicing a
// clipboard "send" request while the caller execs an external reader).
package waylandctl

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SpinningVinyl/go-wayland/wayland/client"
	ext_data_control "github.com/SpinningVinyl/go-wayland/wayland/staging/ext-data-control-v1"
)

const (
	// Registry interface name Hyprland advertises (verified live; note it
	// differs from the XML protocol file name ext_data_control_unstable_v1).
	managerIface = "ext_data_control_manager_v1"
	seatIface    = "wl_seat"

	// Timeouts bound every wait so a stuck peer can never hang the tool.
	setupTimeout = 5 * time.Second
	readTimeout  = 5 * time.Second
	writeTimeout = 5 * time.Second
	// freshEventTimeout bounds the wait for a selection-change event that
	// an external writer (user Ctrl+C, wl-copy in tests) has just issued.
	freshEventTimeout = 500 * time.Millisecond
)

var (
	ErrNoCompositor = errors.New("no wayland compositor socket found")
	ErrDead         = errors.New("wayland connection closed")
	ErrTimeout      = errors.New("wayland round-trip timed out")
)

// msg is one incoming wire message; field order is alignment-optimized.
type msg struct {
	data   []byte
	fds    []int
	sender uint32
	opcode uint32
}

// Client is a live data-control connection. It is safe for concurrent method
// calls; all protocol work is serialised on the internal loop goroutine.
type Client struct {
	ctx      *client.Context
	display  *client.Display
	registry *client.Registry
	mgr      *ext_data_control.DataControlManager
	dev      *ext_data_control.DataControlDevice

	ops  chan func()
	evc  chan msg
	dead chan struct{} // closed when the loop goroutine exits

	// State below is touched only on the loop goroutine.
	globals    map[string]client.RegistryGlobalEvent
	mimes      map[*ext_data_control.DataControlOffer][]string
	inFlight   map[*ext_data_control.DataControlOffer]int  // offers mid-transfer: never destroy
	deferred   map[*ext_data_control.DataControlOffer]bool // destroys parked by inFlight
	cbOffer    *ext_data_control.DataControlOffer
	prOffer    *ext_data_control.DataControlOffer
	cbClaim    string // our own CLIPBOARD claim (compositors do not echo it back)
	prClaim    string
	cbClaimSrc *ext_data_control.DataControlSource // source backing cbClaim
	prClaimSrc *ext_data_control.DataControlSource
	claimSrcs  []*ext_data_control.DataControlSource // keep claimed sources alive
	cbSeq      uint32                                // selection-event counter; bumped on every (incl. empty) event
	prSeq      uint32
	lastCbRead uint32 // cbSeq at the previous ReadClipboard
	lastPrRead uint32
	synced     bool
	devDone    bool
}

// Connect opens the compositor socket. It honours WAYLAND_DISPLAY and, when
// that is unset, probes $XDG_RUNTIME_DIR/wayland-* (Hyprland on this machine
// exposes wayland-1, not wayland-0).
func Connect() (*Client, error) {
	addr, err := socketPath()
	if err != nil {
		return nil, err
	}
	display, err := client.Connect(addr)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}
	c := &Client{
		ctx:      display.Context(),
		display:  display,
		ops:      make(chan func()),
		evc:      make(chan msg, 128),
		dead:     make(chan struct{}),
		globals:  map[string]client.RegistryGlobalEvent{},
		mimes:    map[*ext_data_control.DataControlOffer][]string{},
		inFlight: map[*ext_data_control.DataControlOffer]int{},
		deferred: map[*ext_data_control.DataControlOffer]bool{},
	}
	go c.reader()
	go c.loop()
	if serr := c.setup(); serr != nil {
		// Close on the loop goroutine: Context is not safe for concurrent
		// use and the loop may be mid-DispatchMessage.
		_ = c.do(func() error { return c.ctx.Close() })
		select {
		case <-c.dead:
		case <-time.After(setupTimeout + time.Second):
		}
		return nil, serr
	}
	return c, nil
}

// socketPath resolves the compositor socket. Every component comes from the
// invoking user's own environment (XDG_RUNTIME_DIR, WAYLAND_DISPLAY), so the
// path is trusted input, not attacker-controlled.
func socketPath() (string, error) {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		return "", errors.New("XDG_RUNTIME_DIR not set")
	}
	if d := os.Getenv("WAYLAND_DISPLAY"); d != "" {
		p := filepath.Join(rt, d)
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 { //nolint:gosec // env-derived user-owned path
			return p, nil
		}
	}
	sockets, _ := filepath.Glob(filepath.Join(rt, "wayland-*"))
	sort.Slice(sockets, func(i, j int) bool { return socketNum(sockets[i]) < socketNum(sockets[j]) })
	for _, s := range sockets {
		if fi, err := os.Stat(s); err == nil && fi.Mode()&os.ModeSocket != 0 { //nolint:gosec // env-derived user-owned path
			return s, nil
		}
	}
	return "", fmt.Errorf("%w: no wayland socket in %s", ErrNoCompositor, rt)
}

// socketNum extracts the numeric suffix of a wayland-N socket name so
// probing orders numerically (wayland-2 before wayland-10).
func socketNum(path string) int {
	base := filepath.Base(path)
	rest := strings.TrimPrefix(base, "wayland-")
	if rest == base {
		return math.MaxInt
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return math.MaxInt
	}
	return n
}

func (c *Client) reader() {
	for {
		sender, opcode, fds, data, err := c.ctx.ReadMsg()
		if err != nil {
			// Connection closed (Close or compositor gone). Wake the loop.
			select {
			case <-c.dead:
			default:
				close(c.evc)
			}
			return
		}
		select {
		case c.evc <- msg{data: data, fds: fds, sender: sender, opcode: opcode}:
		case <-c.dead:
			for _, fd := range fds {
				_ = closeFD(fd)
			}
			return
		}
	}
}

func (c *Client) loop() {
	defer close(c.dead)
	for {
		select {
		case ev, ok := <-c.evc:
			if !ok {
				return
			}
			c.dispatch(ev)
		case f := <-c.ops:
			f()
		}
	}
}

func (c *Client) dispatch(ev msg) {
	// Hyprland sends selection/primary_selection (device opcodes 1/3) with
	// object id 0 when the clipboard/primary is empty; the generated
	// bindings panic on the nil type assertion, so filter null-id events
	// before dispatching.
	if c.dev != nil && ev.sender == c.dev.ID() && (ev.opcode == 1 || ev.opcode == 3) &&
		len(ev.data) >= 4 && client.Uint32(ev.data[:4]) == 0 {
		if ev.opcode == 1 {
			c.destroySuperseded(c.cbOffer, nil)
			c.cbOffer, c.cbSeq, c.cbClaim = nil, c.cbSeq+1, ""
		} else {
			c.destroySuperseded(c.prOffer, nil)
			c.prOffer, c.prSeq, c.prClaim = nil, c.prSeq+1, ""
		}
		for _, fd := range ev.fds {
			_ = closeFD(fd)
		}
		return
	}
	// Errors here are protocol-level (unknown sender etc.); a one-shot
	// client ignores them and keeps servicing the connection.
	_ = c.ctx.DispatchMessage(ev.sender, ev.opcode, ev.fds, ev.data)
}

// do runs f on the loop goroutine and waits for it.
func (c *Client) do(f func() error) error {
	type res struct{ err error }
	ch := make(chan res, 1)
	task := func() { ch <- res{f()} }
	select {
	case c.ops <- task:
	case <-c.dead:
		return ErrDead
	}
	select {
	case r := <-ch:
		return r.err
	case <-c.dead:
		return ErrDead
	}
}

// pump processes incoming events until pred holds. Must run on the loop
// goroutine (i.e. inside a do task).
func (c *Client) pump(pred func() bool, timeout time.Duration) error {
	if pred() {
		return nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for !pred() {
		select {
		case ev, ok := <-c.evc:
			if !ok {
				return ErrDead
			}
			c.dispatch(ev)
		case <-deadline.C:
			return ErrTimeout
		}
	}
	return nil
}

func (c *Client) setup() error {
	return c.do(func() error {
		reg, err := c.display.GetRegistry()
		if err != nil {
			return fmt.Errorf("get registry: %w", err)
		}
		c.registry = reg
		reg.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
			c.globals[e.Interface] = e
		})
		cb, err := c.display.Sync()
		if err != nil {
			return fmt.Errorf("sync: %w", err)
		}
		cb.SetDoneHandler(func(client.CallbackDoneEvent) { c.synced = true })
		if perr := c.pump(func() bool { return c.synced }, setupTimeout); perr != nil {
			return fmt.Errorf("registry sync: %w", perr)
		}
		mgrG, ok := c.globals[managerIface]
		if !ok {
			return errors.New("compositor does not advertise " + managerIface)
		}
		seatG, ok := c.globals[seatIface]
		if !ok {
			return errors.New("compositor does not advertise " + seatIface)
		}
		seat := client.NewSeat(c.ctx)
		if berr := reg.Bind(seatG.Name, seatIface, 1, seat); berr != nil {
			return fmt.Errorf("bind seat: %w", berr)
		}
		mgr := ext_data_control.NewDataControlManager(c.ctx)
		version := min(mgrG.Version, uint32(1))
		if berr := reg.Bind(mgrG.Name, managerIface, version, mgr); berr != nil {
			return fmt.Errorf("bind data-control manager: %w", berr)
		}
		c.mgr = mgr
		dev, err := mgr.GetDataDevice(seat)
		if err != nil {
			return fmt.Errorf("get data device: %w", err)
		}
		c.dev = dev
		dev.SetDataOfferHandler(func(e ext_data_control.DataControlDeviceDataOfferEvent) {
			e.Id.SetOfferHandler(func(oe ext_data_control.DataControlOfferOfferEvent) {
				c.mimes[e.Id] = append(c.mimes[e.Id], oe.MimeType)
			})
		})
		dev.SetSelectionHandler(func(e ext_data_control.DataControlDeviceSelectionEvent) {
			c.destroySuperseded(c.cbOffer, e.Id)
			c.cbOffer, c.cbSeq, c.cbClaim = e.Id, c.cbSeq+1, ""
		})
		dev.SetPrimarySelectionHandler(func(e ext_data_control.DataControlDevicePrimarySelectionEvent) {
			c.destroySuperseded(c.prOffer, e.Id)
			c.prOffer, c.prSeq, c.prClaim = e.Id, c.prSeq+1, ""
		})
		dev.SetFinishedHandler(func(ext_data_control.DataControlDeviceFinishedEvent) { c.devDone = true })
		// The compositor flushes the current selections to the new device
		// before answering a sync issued after binding, so a second sync
		// round-trip guarantees initial data_offer/selection events have
		// been processed.
		c.synced = false
		cb2, err := c.display.Sync()
		if err != nil {
			return fmt.Errorf("sync 2: %w", err)
		}
		cb2.SetDoneHandler(func(client.CallbackDoneEvent) { c.synced = true })
		return c.pump(func() bool { return c.synced || c.devDone }, setupTimeout)
	})
}

// destroySuperseded releases the previous selection offer per the
// ext-data-control spec ("the client should destroy the previous offer
// upon receiving this event"), keeping ctx.objects and c.mimes bounded.
// An offer with a transfer in flight is NEVER destroyed mid-transfer (the
// compositor would abort it into a silent 0-byte read); the destroy is
// parked until the transfer completes. Runs on the loop goroutine.
func (c *Client) destroySuperseded(old, next *ext_data_control.DataControlOffer) {
	if old == nil || old == next {
		return
	}
	if c.inFlight[old] > 0 {
		c.deferred[old] = true
		return
	}
	c.destroyOffer(old)
}

func (c *Client) destroyOffer(offer *ext_data_control.DataControlOffer) {
	_ = offer.Destroy()
	delete(c.mimes, offer)
	delete(c.deferred, offer)
	delete(c.inFlight, offer)
}

// waitForFreshSelection pumps until a selection event newer than `before`
// is processed. A short timeout is fine: if the selection did not change,
// the state captured at Connect is current.
func (c *Client) waitForFreshSelection(seq *uint32, before uint32) {
	_ = c.pump(func() bool { return *seq != before || c.devDone }, freshEventTimeout)
}

// ReadClipboard returns the current CLIPBOARD text, or "" when nothing is
// offered or no text offer exists.
func (c *Client) ReadClipboard() (string, error) {
	var out string
	err := c.do(func() error {
		// Skip the fresh-event wait when a selection change already
		// arrived since the last read; otherwise give an in-flight
		// external write (e.g. user Ctrl+C) a short window.
		if c.cbSeq == c.lastCbRead {
			c.waitForFreshSelection(&c.cbSeq, c.lastCbRead)
		}
		c.lastCbRead = c.cbSeq
		if c.cbOffer == nil && c.cbClaim != "" {
			out = c.cbClaim // our own claim: no compositor echo, serve locally
			return nil
		}
		var e error
		out, e = c.readOffer(
			func() *ext_data_control.DataControlOffer { return c.cbOffer },
			func() { c.waitForFreshSelection(&c.cbSeq, c.cbSeq) },
		)
		return e
	})
	return out, err
}

// readOffer receives the current offer, retrying (bounded) when a
// text-advertising selection yields nothing: either a 0-byte completion
// (source superseded compositor-side mid-handshake) or a stalled transfer
// whose offer has demonstrably moved (source cancelled before our request
// landed). Each retry re-resolves the CURRENT offer and, if it has not
// moved yet, pumps briefly for the pending selection event first. A
// genuinely empty clipboard (no offer at all) exits after the first try.
func (c *Client) readOffer(current func() *ext_data_control.DataControlOffer, pumpFresh func()) (string, error) {
	offer := current()
	out, err := c.receive(offer)
	for attempt := 0; attempt < 3; attempt++ {
		empty := err == nil && out == ""
		stalled := errors.Is(err, ErrTimeout)
		if !empty && !stalled {
			return out, err
		}
		next := current()
		if next == offer {
			if stalled {
				break // stalled with no movement: honest error, no 5s re-stall
			}
			pumpFresh()
			next = current()
		}
		if next == offer && len(c.mimes[offer]) == 0 {
			break // no offer and no change coming: genuinely empty
		}
		offer = next
		out, err = c.receive(offer)
	}
	return out, err
}

// ReadPrimary returns the current PRIMARY (mouse selection) text.
func (c *Client) ReadPrimary() (string, error) {
	var out string
	err := c.do(func() error {
		if c.prSeq == c.lastPrRead {
			c.waitForFreshSelection(&c.prSeq, c.lastPrRead)
		}
		c.lastPrRead = c.prSeq
		if c.prOffer == nil && c.prClaim != "" {
			out = c.prClaim
			return nil
		}
		var e error
		out, e = c.readOffer(
			func() *ext_data_control.DataControlOffer { return c.prOffer },
			func() { c.waitForFreshSelection(&c.prSeq, c.prSeq) },
		)
		return e
	})
	return out, err
}

// receive asks the offering client to write the best text mime into a pipe
// and reads it back. The pipe read runs on a helper goroutine while the
// loop keeps dispatching events: this services our OWN send requests
// (claim-then-read-back on the same client) and keeps the loop responsive
// while an external peer takes its time. Bounded by readTimeout.
func (c *Client) receive(offer *ext_data_control.DataControlOffer) (string, error) {
	if offer == nil {
		return "", nil
	}
	mime := pickTextMime(c.mimes[offer])
	if mime == "" {
		return "", nil
	}
	// Mark the offer as carrying an in-flight transfer: selection events
	// arriving mid-transfer must park their destroy until we are done
	// (destroying an offer mid-transfer aborts it into a 0-byte read).
	c.inFlight[offer]++
	defer func() {
		c.inFlight[offer]--
		if c.inFlight[offer] == 0 && c.deferred[offer] {
			c.destroyOffer(offer)
		}
	}()
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	if rerr := offer.Receive(mime, int(w.Fd())); rerr != nil {
		_ = w.Close() // *os.File owns the fd: close via the File, never raw
		_ = r.Close()
		return "", fmt.Errorf("offer receive: %w", rerr)
	}
	_ = w.Close() // our write end: EOF once the peer closes its end
	type rr struct {
		err  error
		data []byte
	}
	ch := make(chan rr, 1)
	go func() {
		data, rerr := io.ReadAll(r)
		ch <- rr{rerr, data}
	}()
	deadline := time.NewTimer(readTimeout)
	defer deadline.Stop()
	for {
		select {
		case res := <-ch:
			_ = r.Close()
			if res.err != nil {
				return "", fmt.Errorf("read clipboard data: %w", res.err)
			}
			return string(res.data), nil
		case ev, ok := <-c.evc:
			if !ok {
				_ = r.Close()
				return "", ErrDead
			}
			c.dispatch(ev)
		case <-deadline.C:
			_ = r.Close() // unblocks the helper goroutine
			return "", fmt.Errorf("%w: timed out waiting for clipboard data from offering client", ErrTimeout)
		}
	}
}

func pickTextMime(mimes []string) string {
	best := ""
	for _, m := range mimes {
		switch m {
		case "text/plain;charset=utf-8":
			return m
		case "text/plain":
			best = m
		}
	}
	if best != "" {
		return best
	}
	for _, m := range mimes {
		if strings.HasPrefix(m, "text/") {
			return m
		}
	}
	return ""
}

// ClaimClipboard makes text the CLIPBOARD selection, served in-process: the
// data stays in this client and is written on demand to each reader. The
// claim lives only as long as the Client: Close (or process exit) drops it.
func (c *Client) ClaimClipboard(text string) error {
	return c.claim(text, false)
}

// ClaimPrimary makes text the PRIMARY selection.
func (c *Client) ClaimPrimary(text string) error {
	return c.claim(text, true)
}

// roundtrip blocks until the compositor has processed everything sent so
// far (display.sync callback). Used after claims: without it, a reader
// connecting immediately after ClaimClipboard can still see the PREVIOUS
// (possibly dead) selection because SetSelection was only buffered.
func (c *Client) roundtrip() error {
	c.synced = false
	cb, err := c.display.Sync()
	if err != nil {
		return err
	}
	cb.SetDoneHandler(func(client.CallbackDoneEvent) { c.synced = true })
	return c.pump(func() bool { return c.synced }, setupTimeout)
}

func (c *Client) claim(text string, primary bool) error {
	return c.do(func() error {
		src, err := c.mgr.CreateDataSource()
		if err != nil {
			return fmt.Errorf("create data source: %w", err)
		}
		if oerr := src.Offer("text/plain;charset=utf-8"); oerr != nil {
			_ = src.Destroy()
			return fmt.Errorf("offer utf8: %w", oerr)
		}
		if oerr := src.Offer("text/plain"); oerr != nil {
			_ = src.Destroy()
			return fmt.Errorf("offer plain: %w", oerr)
		}
		src.SetSendHandler(func(e ext_data_control.DataControlSourceSendEvent) {
			// Service the fd OFF the loop: a slow or stalled reader must
			// never park the loop goroutine (which would wedge Close and
			// every later call). Bounded by writeTimeout.
			go func() {
				f := os.NewFile(uintptr(e.Fd), "data-control-send")
				_ = f.SetWriteDeadline(time.Now().Add(writeTimeout))
				_, _ = io.WriteString(f, text)
				_ = f.Close()
			}()
		})
		src.SetCancelledHandler(func(ext_data_control.DataControlSourceCancelledEvent) {
			// Superseded: release the server-side object, stop tracking,
			// and drop any local-serve claim this source backed.
			_ = src.Destroy()
			c.dropSrc(src)
			if c.cbClaimSrc == src {
				c.cbClaim, c.cbClaimSrc = "", nil
			}
			if c.prClaimSrc == src {
				c.prClaim, c.prClaimSrc = "", nil
			}
		})
		var serr error
		if primary {
			serr = c.dev.SetPrimarySelection(src)
		} else {
			serr = c.dev.SetSelection(src)
		}
		if serr != nil {
			_ = src.Destroy()
			return fmt.Errorf("set selection: %w", serr)
		}
		// Compositors do not echo the selection event back to the setting
		// client: track our own claim so Read* can serve it locally. The
		// external offer we are replacing must still be released. State
		// is updated BEFORE the round-trip pump: a selection event that
		// supersedes us during the barrier then correctly clears cbClaim
		// rather than being overwritten by it.
		if primary {
			c.destroySuperseded(c.prOffer, nil)
			c.prOffer, c.prSeq, c.prClaim, c.prClaimSrc = nil, c.prSeq+1, text, src
		} else {
			c.destroySuperseded(c.cbOffer, nil)
			c.cbOffer, c.cbSeq, c.cbClaim, c.cbClaimSrc = nil, c.cbSeq+1, text, src
		}
		// Keep the source referenced for the client's lifetime; the
		// compositor delivers it to readers via send events on the loop.
		c.claimSrcs = append(c.claimSrcs, src)
		// Guarantee the compositor processed the selection change before
		// returning, so any reader that connects next sees OUR claim.
		if rerr := c.roundtrip(); rerr != nil {
			return fmt.Errorf("claim round-trip: %w", rerr)
		}
		return nil
	})
}

// dropSrc removes a cancelled source from the keep-alive slice.
// Runs on the loop goroutine.
func (c *Client) dropSrc(src *ext_data_control.DataControlSource) {
	for i, s := range c.claimSrcs {
		if s == src {
			c.claimSrcs = append(c.claimSrcs[:i], c.claimSrcs[i+1:]...)
			return
		}
	}
}

// Close tears down the connection. Any clipboard claims die with it.
func (c *Client) Close() {
	// Close on the loop goroutine so it cannot race DispatchMessage.
	_ = c.do(func() error { return c.ctx.Close() })
	// Give the loop time to unwind its bounded internal waits (reads up to
	// readTimeout) so goroutines do not outlive Close.
	select {
	case <-c.dead:
	case <-time.After(readTimeout + 2*time.Second):
	}
}

func closeFD(fd int) error { return syscall.Close(fd) }
