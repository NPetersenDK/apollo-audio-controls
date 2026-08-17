package main

import (
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// TEST-NET-1, so nothing here can point at real hardware.
const testDeviceIP = "192.0.2.10"

func testDevice() *Device {
	return NewDevice(Config{DeviceIP: testDeviceIP}, NewEventBus())
}

func statusFrame(t *testing.T, detect, capBits, flags byte) frame {
	t.Helper()
	body, err := hex.DecodeString(strings.ReplaceAll(
		"07390141 00000000 00000001 00140010 00000000 000000c0 000000"+hex.EncodeToString([]byte{detect})+
			" 000000"+hex.EncodeToString([]byte{capBits})+" 000000"+hex.EncodeToString([]byte{flags}), " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return frame{Vendor: vendorDante, Body: body}
}

// No session, no packets.
func TestCommandsRequireSession(t *testing.T) {
	d := testDevice()

	if _, err := d.SetGain(40, 50*time.Millisecond); !errors.Is(err, errNoSession) {
		t.Fatalf("SetGain without a session: %v", err)
	}
	if _, err := d.SetFlag("hpf", true, false, 50*time.Millisecond); !errors.Is(err, errNoSession) {
		t.Fatalf("SetFlag without a session: %v", err)
	}
	if _, err := d.Poll(50 * time.Millisecond); !errors.Is(err, errNoSession) {
		t.Fatalf("Poll without a session: %v", err)
	}
	if d.Snapshot().Session {
		t.Fatal("the state claims a session that does not exist")
	}
}

// 48V is refused before the network is touched, not after.
func TestPhantomNeedsConfirmation(t *testing.T) {
	d := testDevice()

	_, err := d.SetFlag("48v", true, false, 50*time.Millisecond)
	if err == nil || errors.Is(err, errNoSession) {
		t.Fatalf("48V without confirmation must be refused up front, got: %v", err)
	}

	// Off needs no confirmation, so it reaches the session check.
	if _, err := d.SetFlag("48v", false, false, 50*time.Millisecond); !errors.Is(err, errNoSession) {
		t.Fatalf("48V off: %v", err)
	}

	locked := NewDevice(Config{DeviceIP: testDeviceIP, LockPhantom: true}, NewEventBus())
	if _, err := locked.SetFlag("48v", true, true, 50*time.Millisecond); err == nil || errors.Is(err, errNoSession) {
		t.Fatalf("-lock-48v must refuse even with confirmation, got: %v", err)
	}
}

func TestHandleFrameUpdatesState(t *testing.T) {
	d := testDevice()

	d.handleFrame(statusFrame(t, 0x80, 0x3f, 0x09)) // 48V + HPF, XLR in
	st := d.Snapshot()
	if !st.HaveFlags || st.Flags != 0x09 || !st.Plugged || st.Cap != 0x3f {
		t.Fatalf("switch state was not updated: %+v", st)
	}
	if st.FlagsRev == 0 || st.FlagsAt == "" {
		t.Fatal("revision/timestamp missing")
	}

	d.handleFrame(frame{Vendor: vendorUA, Body: []byte{0x43, 0x28, 0x40, 0x18}})
	if st = d.Snapshot(); !st.HaveGain || st.GainDB != 33 {
		t.Fatalf("gain was not updated: %+v", st)
	}

	// Unknown bodies change nothing.
	before := d.Snapshot()
	d.handleFrame(frame{Vendor: vendorUA, Body: []byte{0x00, 0x11}})
	d.handleFrame(frame{Vendor: vendorDante, Body: []byte{0x07, 0x39}})
	if d.Snapshot() != before {
		t.Fatal("an unknown message changed the state")
	}
}

// wait ignores stale values and wakes on the right state only.
func TestWaitSkipsStaleValues(t *testing.T) {
	d := testDevice()

	go func() {
		time.Sleep(10 * time.Millisecond)
		d.handleFrame(statusFrame(t, 0x80, 0x3f, 0x00)) // HPF still off
		time.Sleep(10 * time.Millisecond)
		d.handleFrame(statusFrame(t, 0x80, 0x3f, 0x08)) // HPF switched on
	}()

	st, ok := d.wait(func(s State) bool { return s.HaveFlags && s.Flags&0x08 != 0 }, time.Second)
	if !ok {
		t.Fatal("waited in vain for HPF")
	}
	if st.Flags != 0x08 {
		t.Fatalf("woke on the wrong state: 0x%02x", st.Flags)
	}

	if _, ok := d.wait(func(s State) bool { return s.Flags == 0xff }, 50*time.Millisecond); ok {
		t.Fatal("wait claimed a state that never arrived")
	}
}

func TestResolveIface(t *testing.T) {
	if _, _, err := resolveIface("", "not an ip"); err == nil {
		t.Fatal("an invalid device IP was accepted")
	}
	// TEST-NET-1: nobody holds this locally.
	if _, _, err := resolveIface("192.0.2.1", testDeviceIP); err == nil {
		t.Fatal("an interface IP we do not have was accepted")
	}

	// TEST-NET-3 shares no subnet, so this falls back on the routing table.
	ifi, ip, err := resolveIface("", "203.0.113.5")
	if err != nil {
		return
	}
	if ifi == nil || ip.To4() == nil {
		t.Fatalf("route fallback returned something unusable: %v %v", ifi, ip)
	}
	if ifi.Flags&net.FlagUp == 0 {
		t.Fatalf("interface %s is not up", ifi.Name)
	}
}

func TestFmtFlags(t *testing.T) {
	if got := fmtFlags(0x09); got != "48v=ON mute=off phase=off hpf=ON pad=off" {
		t.Fatalf("fmtFlags: %q", got)
	}
}
