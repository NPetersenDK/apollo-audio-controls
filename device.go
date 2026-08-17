package main

// Device holds the connection to the e1x, and only between Connect and
// Disconnect: no session means no sockets, no multicast membership and no
// packets. Nothing here polls in the background.

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Grace period after the browser lets go, so a reload does not churn sockets.
const sessionLinger = 5 * time.Second

// A status query is repeated this many times before we call it unanswered.
const pollAttempts = 4

type Config struct {
	DeviceIP    string
	IfaceIP     string // empty = work out the interface towards the device
	LockPhantom bool   // block 48V entirely, including from the UI
}

// State is what the UI sees. The Rev counters let us wait for a NEW reply.
type State struct {
	Session bool   `json:"session"`
	Device  string `json:"device"`
	Iface   string `json:"iface"`

	HaveFlags bool   `json:"have_flags"`
	Flags     uint32 `json:"flags"`
	Detect    uint32 `json:"detect"`
	Cap       uint32 `json:"cap"`
	Plugged   bool   `json:"plugged"`
	FlagsAt   string `json:"flags_at,omitempty"`

	HaveGain bool   `json:"have_gain"`
	GainDB   int    `json:"gain_db"`
	GainAt   string `json:"gain_at,omitempty"`

	FlagsRev uint64 `json:"-"`
	GainRev  uint64 `json:"-"`
}

type session struct {
	rx       *net.UDPConn // multicast, the device's state changes
	tx       *net.UDPConn // our commands
	iface    *net.Interface
	deviceIP string // fixed for the lifetime of the session
	done     chan struct{}
}

type Device struct {
	cfg Config
	bus *EventBus
	seq *seqCounter

	cmdMu sync.Mutex // one command at a time; the device drops closely spaced messages

	mu      sync.Mutex
	refs    int
	sess    *session
	linger  *time.Timer
	state   State
	sender  [8]byte
	waiters map[chan State]struct{}
}

var errNoSession = errors.New("not connected -- press Connect first")

func NewDevice(cfg Config, bus *EventBus) *Device {
	return &Device{
		cfg:     cfg,
		bus:     bus,
		seq:     newSeqCounter(),
		waiters: make(map[chan State]struct{}),
		state:   State{Device: cfg.DeviceIP, Iface: cfg.IfaceIP},
	}
}

// --- state and publishing ---

func (d *Device) Snapshot() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// deviceIP is the address in use; the UI can change it between sessions.
func (d *Device) deviceIP() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.DeviceIP
}

// publishLocked pushes the state to every SSE client. Call with the lock held.
func (d *Device) publishLocked() {
	st := d.state
	for ch := range d.waiters {
		select {
		case ch <- st:
		default:
		}
	}
	d.bus.PublishJSON(map[string]any{"type": "state", "state": st})
}

func (d *Device) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	d.bus.PublishJSON(map[string]any{
		"type":  "log",
		"level": level,
		"ts":    time.Now().Format("15:04:05"),
		"msg":   msg,
	})
	switch level {
	case "err":
		slog.Error(msg)
	case "warn":
		slog.Warn(msg)
	default:
		slog.Info(msg)
	}
}

func (d *Device) subscribe() chan State {
	ch := make(chan State, 16)
	d.mu.Lock()
	d.waiters[ch] = struct{}{}
	d.mu.Unlock()
	return ch
}

func (d *Device) unsubscribe(ch chan State) {
	d.mu.Lock()
	delete(d.waiters, ch)
	d.mu.Unlock()
}

// wait blocks until a state satisfies pred, or gives up.
func (d *Device) wait(pred func(State) bool, timeout time.Duration) (State, bool) {
	ch := d.subscribe()
	defer d.unsubscribe(ch)
	deadline := time.After(timeout)
	for {
		select {
		case st := <-ch:
			if pred(st) {
				return st, true
			}
		case <-deadline:
			return State{}, false
		}
	}
}

// --- session lifetime ---

// Acquire opens the session. started is true when this is the only client,
// so the caller knows to read the status in; a session already running keeps
// the address it was opened with.
func (d *Device) Acquire(deviceIP string) (started bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.refs++
	if d.linger != nil {
		d.linger.Stop()
		d.linger = nil
	}
	if d.sess != nil {
		d.publishLocked()
		// Reusing a session nobody was left on: read the state in again,
		// since the device may have moved while we were gone.
		return d.refs == 1, nil
	}
	if deviceIP != "" {
		if net.ParseIP(deviceIP) == nil {
			d.refs--
			err := fmt.Errorf("invalid device address %q", deviceIP)
			d.logf("err", "%v", err)
			return false, err
		}
		d.cfg.DeviceIP = deviceIP
		d.state.Device = deviceIP
	}
	if err := d.startLocked(); err != nil {
		d.refs--
		return false, err
	}
	d.publishLocked()
	return true, nil
}

// Release closes the session once the last window has let go.
func (d *Device) Release() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.refs > 0 {
		d.refs--
	}
	if d.refs > 0 {
		d.publishLocked()
		return
	}
	d.publishLocked()
	if d.linger != nil {
		d.linger.Stop()
	}
	d.linger = time.AfterFunc(sessionLinger, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.refs == 0 && d.sess != nil {
			d.stopLocked()
			d.publishLocked()
		}
	})
}

func (d *Device) startLocked() error {
	iface, localIP, err := resolveIface(d.cfg.IfaceIP, d.cfg.DeviceIP)
	if err != nil {
		d.logf("err", "could not find a network interface: %v", err)
		return err
	}
	if len(iface.HardwareAddr) != 6 {
		return fmt.Errorf("interface %s has no usable MAC", iface.Name)
	}
	copy(d.sender[:], iface.HardwareAddr)
	d.sender[6], d.sender[7] = 0, 0

	rx, err := net.ListenMulticastUDP("udp4", iface,
		&net.UDPAddr{IP: net.ParseIP(stateGroupIP), Port: stateGroupPort})
	if err != nil {
		d.logf("err", "could not join %s:%d: %v", stateGroupIP, stateGroupPort, err)
		return err
	}
	_ = rx.SetReadBuffer(1 << 20)

	dst := &net.UDPAddr{IP: net.ParseIP(d.cfg.DeviceIP), Port: cmcPort}
	// The device ignores the source port, so any port works when UA Mixer
	// Engine holds 8700.
	tx, err := net.DialUDP("udp4", &net.UDPAddr{IP: localIP, Port: cmcPort}, dst)
	if err != nil {
		tx, err = net.DialUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0}, dst)
	}
	if err != nil {
		rx.Close()
		d.logf("err", "could not open the sending socket: %v", err)
		return err
	}

	s := &session{rx: rx, tx: tx, iface: iface, deviceIP: d.cfg.DeviceIP, done: make(chan struct{})}
	d.sess = s
	d.state.Session = true
	d.state.Device = d.cfg.DeviceIP
	d.state.Iface = localIP.String()
	go d.readLoop(s)

	d.logf("debug", "listening on %s:%d via %s (%s), sending from %s",
		stateGroupIP, stateGroupPort, iface.Name, localIP, tx.LocalAddr())
	return nil
}

func (d *Device) stopLocked() {
	s := d.sess
	d.sess = nil
	d.state.Session = false
	if s == nil {
		return
	}
	close(s.done)
	_ = s.rx.Close()
	_ = s.tx.Close()
	d.logf("warn", "disconnected from %s, multicast group left", s.deviceIP)
}

// Shutdown closes any open session when the program exits.
func (d *Device) Shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.linger != nil {
		d.linger.Stop()
		d.linger = nil
	}
	if d.sess != nil {
		d.stopLocked()
	}
}

// --- receiving ---

func (d *Device) readLoop(s *session) {
	buf := make([]byte, 65535)
	for {
		n, src, err := s.rx.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.done:
			default:
				d.logf("err", "the multicast listener stopped: %v", err)
			}
			return
		}
		// The group carries every Dante device on the network.
		if src.IP.String() != s.deviceIP {
			continue
		}
		f, ok := parseFrame(buf[:n])
		if !ok {
			continue
		}
		d.handleFrame(f)
	}
}

func (d *Device) handleFrame(f frame) {
	now := time.Now().Format("15:04:05")

	switch string(f.Vendor) {
	case string(vendorUA):
		db, ok := gainFromBody(f.Body)
		if !ok {
			return
		}
		d.mu.Lock()
		changed := !d.state.HaveGain || d.state.GainDB != db
		d.state.HaveGain = true
		d.state.GainDB = db
		d.state.GainAt = now
		d.state.GainRev++
		d.publishLocked()
		d.mu.Unlock()
		if changed {
			d.logf("ok", "device reports gain ch1 = %d dB", db)
		}

	case string(vendorDante):
		st, ok := statusFromBody(f.Body)
		if !ok {
			return
		}
		d.mu.Lock()
		// A first reading is reported by Poll, so only report movement here.
		changed := d.state.HaveFlags && (d.state.Flags != st.Flags || d.state.Detect != st.Detect)
		d.state.HaveFlags = true
		d.state.Flags = st.Flags
		d.state.Detect = st.Detect
		d.state.Cap = st.Cap
		d.state.Plugged = st.Detect&detectConnected != 0
		d.state.FlagsAt = now
		d.state.FlagsRev++
		d.publishLocked()
		d.mu.Unlock()
		if changed {
			plug := "nothing plugged in"
			if st.Detect&detectConnected != 0 {
				plug = "connected"
			}
			d.logf("ok", "device reports 0x%02x  %s  (input: %s)", st.Flags, fmtFlags(st.Flags), plug)
		}
	}
}

// --- sending ---

// send writes a packet if, and only if, a session is open.
func (d *Device) send(build func(sender [8]byte, seq uint16) ([]byte, error)) error {
	d.mu.Lock()
	s := d.sess
	sender := d.sender
	d.mu.Unlock()
	if s == nil {
		return errNoSession
	}
	pkt, err := build(sender, d.seq.next())
	if err != nil {
		return err
	}
	_, err = s.tx.Write(pkt)
	return err
}

// Poll reads the switches and input status. Connect and Refresh only.
func (d *Device) Poll(timeout time.Duration) (State, error) {
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()

	before := d.Snapshot().FlagsRev
	// The device drops queries arriving too soon after a command, and a
	// switch that has just seen our IGMP join needs a moment to forward the
	// group again, so ask more than once.
	for attempt := 0; attempt < pollAttempts; attempt++ {
		if err := d.send(func(sender [8]byte, seq uint16) ([]byte, error) {
			return buildStatusQuery(sender, seq), nil
		}); err != nil {
			return State{}, err
		}
		if st, ok := d.wait(func(s State) bool { return s.FlagsRev > before }, timeout/pollAttempts); ok {
			// An explicit read always reports back, even when nothing moved,
			// so Connect and Refresh are never silent.
			plug := "nothing plugged in"
			if st.Plugged {
				plug = "connected"
			}
			d.logf("debug", "status 0x%02x  %s  (input: %s)", st.Flags, fmtFlags(st.Flags), plug)
			return st, nil
		}
	}
	d.logf("warn", "no answer to the status query")
	return State{}, fmt.Errorf("no answer from %s within %s", d.deviceIP(), timeout)
}

// SetGain sets the gain on channel 1 and waits for the device's echo.
func (d *Device) SetGain(db int, timeout time.Duration) (State, error) {
	if _, err := dbToVV(db); err != nil {
		return State{}, err
	}
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()

	// Subscribe before sending: the echo arrives some 2 ms later.
	ch := d.subscribe()
	defer d.unsubscribe(ch)

	if err := d.send(func(sender [8]byte, seq uint16) ([]byte, error) {
		return buildGainSet(db, sender, seq)
	}); err != nil {
		return State{}, err
	}
	d.logf("debug", "gain ch1 -> %d dB sent to %s:%d", db, d.deviceIP(), cmcPort)

	deadline := time.After(timeout)
	for {
		select {
		case st := <-ch:
			if st.HaveGain && st.GainDB == db {
				return st, nil
			}
		case <-deadline:
			d.logf("warn", "gain was not confirmed within %s -- the command may have been ignored", timeout)
			return State{}, fmt.Errorf("the device did not confirm %d dB", db)
		}
	}
}

// SetFlag flips a switch and waits for the device to report the bit.
func (d *Device) SetFlag(name string, on, yes bool, timeout time.Duration) (State, error) {
	f, ok := flagByName(name)
	if !ok {
		return State{}, fmt.Errorf("unknown flag %q", name)
	}
	if f.Danger && on {
		if d.cfg.LockPhantom {
			return State{}, fmt.Errorf("48V is blocked in this instance (-lock-48v)")
		}
		if !yes {
			return State{}, fmt.Errorf("48V phantom power can destroy ribbon microphones and some " +
				"condenser microphones -- confirmation missing")
		}
	}

	value := uint32(0)
	if on {
		value = f.Mask
	}

	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()

	ch := d.subscribe()
	defer d.unsubscribe(ch)

	if err := d.send(func(sender [8]byte, seq uint16) ([]byte, error) {
		return buildFlagSet(f.Mask, value, sender, seq), nil
	}); err != nil {
		return State{}, err
	}
	state := "off"
	if on {
		state = "ON"
	}
	d.logf("debug", "%s -> %s (mask=0x%02x value=0x%02x)", f.Name, state, f.Mask, value)

	deadline := time.After(timeout)
	for {
		select {
		case st := <-ch:
			if st.HaveFlags && (st.Flags&f.Mask != 0) == on {
				return st, nil
			}
		case <-deadline:
			d.logf("warn", "%s did not reach the expected state within %s", f.Name, timeout)
			return State{}, fmt.Errorf("the device did not confirm %s=%s", f.Name, state)
		}
	}
}

// --- interface lookup ---

// resolveIface picks the interface to send from: the one sharing a subnet
// with the device, else whatever the routing table would use.
func resolveIface(ifaceIP, deviceIP string) (*net.Interface, net.IP, error) {
	dev := net.ParseIP(deviceIP)
	if dev == nil {
		return nil, nil, fmt.Errorf("invalid device IP %q", deviceIP)
	}

	if ifaceIP != "" {
		want := net.ParseIP(ifaceIP)
		if want == nil {
			return nil, nil, fmt.Errorf("invalid interface IP %q", ifaceIP)
		}
		ifi, ip := ifaceOwning(func(n *net.IPNet) bool { return n.IP.Equal(want) })
		if ifi == nil {
			return nil, nil, fmt.Errorf("no interface holds the address %s", ifaceIP)
		}
		return ifi, ip, nil
	}

	// Same subnet as the device -- the normal case on a flat Dante network.
	if ifi, ip := ifaceOwning(func(n *net.IPNet) bool { return n.Contains(dev) }); ifi != nil {
		return ifi, ip, nil
	}

	// A UDP "connection" sends nothing but reveals the local address.
	c, err := net.Dial("udp4", net.JoinHostPort(deviceIP, "9"))
	if err != nil {
		return nil, nil, fmt.Errorf("no route to %s -- pass -iface", deviceIP)
	}
	local := c.LocalAddr().(*net.UDPAddr).IP
	_ = c.Close()
	if ifi, ip := ifaceOwning(func(n *net.IPNet) bool { return n.IP.Equal(local) }); ifi != nil {
		return ifi, ip, nil
	}
	return nil, nil, fmt.Errorf("could not determine the interface towards %s -- pass -iface", deviceIP)
}

// ifaceOwning finds the first up, non-loopback interface matching match.
func ifaceOwning(match func(*net.IPNet) bool) (*net.Interface, net.IP) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if match(ipnet) {
				return &ifi, ipnet.IP.To4()
			}
		}
	}
	return nil, nil
}
