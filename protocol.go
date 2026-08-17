package main

// Encoding and decoding only, no networking. See README.md for the protocol.
//
//	ff ff | length | seq | 00 00 | sender ID (8) | vendor ID (8) | body
//
// Gain lives in UA's own UnvAudio block, the switches in the Audinate one.

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"
)

const (
	cmcPort        = 8700 // host -> device, Dante CMC
	stateGroupIP   = "224.0.0.231"
	stateGroupPort = 8702 // device -> multicast, state changes

	gainMinDB  = 10
	gainMaxDB  = 65
	gainOffset = 9 // dB = VV + 9

	detectConnected = 0x80 // something is plugged into the XLR input
)

var (
	vendorUA    = []byte("UnvAudio")
	vendorDante = []byte("Audinate")

	blobGainCh1 = []byte{0x43, 0x28, 0x40} // gain channel 1; last byte is VV

	// 07 39 01 40 | 00 0f 42 40 -- opcode + 1000000 us
	flagHdr = []byte{0x07, 0x39, 0x01, 0x40, 0x00, 0x0f, 0x42, 0x40}
	// byte[1] = 01 writes, 00 is a plain query
	flagMidWrite = []byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x08, 0x00, 0x10}
	flagMidQuery = []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x10}
	echoHdr      = []byte{0x07, 0x39, 0x01, 0x41}
)

// flagDef travels to the browser via /api/config, so the UI holds no copy.
type flagDef struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Mask     uint32 `json:"mask"`
	Hint     string `json:"hint"`
	Danger   bool   `json:"danger"`   // requires explicit confirmation
	ReadOnly bool   `json:"readonly"` // the device sets this one too
}

var flagDefs = []flagDef{
	{Name: "48v", Label: "48V", Mask: 0x01, Hint: "Phantom power", Danger: true},
	{Name: "mute", Label: "MUTE", Mask: 0x02, Hint: "Also set by the device itself around 48V and HPF changes"},
	{Name: "phase", Label: "Ø", Mask: 0x04, Hint: "Polarity invert"},
	{Name: "hpf", Label: "HPF", Mask: 0x08, Hint: "Low cut"},
	{Name: "pad", Label: "PAD", Mask: 0x10, Hint: "Attenuation"},
}

func flagByName(name string) (flagDef, bool) {
	for _, f := range flagDefs {
		if f.Name == name {
			return f, true
		}
	}
	return flagDef{}, false
}

func dbToVV(db int) (byte, error) {
	if db < gainMinDB || db > gainMaxDB {
		return 0, fmt.Errorf("gain must be %d..%d dB, got %d", gainMinDB, gainMaxDB, db)
	}
	return byte(db - gainOffset), nil
}

func vvToDB(vv byte) int { return int(vv) + gainOffset }

// The device silently drops a sequence number it just saw, so two processes
// must not start in the same place -- hence random, not time-based.
type seqCounter struct{ v atomic.Uint32 }

func newSeqCounter() *seqCounter {
	var b [2]byte
	_, _ = rand.Read(b[:])
	c := &seqCounter{}
	c.v.Store(uint32(binary.BigEndian.Uint16(b[:])))
	return c
}

func (c *seqCounter) next() uint16 { return uint16(c.v.Add(1)) }

func buildFrame(vendor, body []byte, sender [8]byte, seq uint16) []byte {
	total := 24 + len(body)
	pkt := make([]byte, 0, total)
	pkt = append(pkt, 0xff, 0xff)
	pkt = binary.BigEndian.AppendUint16(pkt, uint16(total))
	pkt = binary.BigEndian.AppendUint16(pkt, seq)
	pkt = append(pkt, 0x00, 0x00)
	pkt = append(pkt, sender[:]...)
	pkt = append(pkt, vendor...)
	return append(pkt, body...)
}

// buildGainSet wraps the gain blob in UA's TLV: 01 02 <outer len> <inner len>.
func buildGainSet(db int, sender [8]byte, seq uint16) ([]byte, error) {
	vv, err := dbToVV(db)
	if err != nil {
		return nil, err
	}
	blob := append(append([]byte{}, blobGainCh1...), vv)
	body := []byte{0x01, 0x02}
	body = binary.BigEndian.AppendUint16(body, uint16(len(blob)+2))
	body = binary.BigEndian.AppendUint16(body, uint16(len(blob)))
	body = append(body, blob...)
	return buildFrame(vendorUA, body, sender, seq), nil
}

// buildFlagSet writes the bitfield as a mask/value pair.
func buildFlagSet(mask, value uint32, sender [8]byte, seq uint16) []byte {
	body := append(append([]byte{}, flagHdr...), flagMidWrite...)
	body = binary.BigEndian.AppendUint32(body, mask)
	body = binary.BigEndian.AppendUint32(body, value)
	return buildFrame(vendorDante, body, sender, seq)
}

// buildStatusQuery is a write with the write flag cleared.
func buildStatusQuery(sender [8]byte, seq uint16) []byte {
	body := append(append([]byte{}, flagHdr...), flagMidQuery...)
	body = binary.BigEndian.AppendUint32(body, 0)
	body = binary.BigEndian.AppendUint32(body, 0)
	return buildFrame(vendorDante, body, sender, seq)
}

type frame struct {
	Seq    uint16
	Dev    []byte
	Vendor []byte
	Body   []byte
}

func parseFrame(b []byte) (frame, bool) {
	if len(b) < 24 || b[0] != 0xff || b[1] != 0xff {
		return frame{}, false
	}
	length := int(binary.BigEndian.Uint16(b[2:4]))
	if length > len(b) || length < 24 {
		length = len(b)
	}
	return frame{
		Seq:    binary.BigEndian.Uint16(b[4:6]),
		Dev:    b[8:16],
		Vendor: b[16:24],
		Body:   b[24:length],
	}, true
}

// gainFromBody reads the device's raw state blob (without the 01 02 header).
func gainFromBody(body []byte) (int, bool) {
	if len(body) < 4 || string(body[:3]) != string(blobGainCh1) {
		return 0, false
	}
	return vvToDB(body[3]), true
}

type statusReply struct {
	Detect uint32
	Cap    uint32
	Flags  uint32
}

func statusFromBody(body []byte) (statusReply, bool) {
	if len(body) < 36 || string(body[:4]) != string(echoHdr) {
		return statusReply{}, false
	}
	return statusReply{
		Detect: binary.BigEndian.Uint32(body[24:28]),
		Cap:    binary.BigEndian.Uint32(body[28:32]),
		Flags:  binary.BigEndian.Uint32(body[32:36]),
	}, true
}

func fmtFlags(bits uint32) string {
	out := ""
	for _, f := range flagDefs {
		if out != "" {
			out += " "
		}
		state := "off"
		if bits&f.Mask != 0 {
			state = "ON"
		}
		out += fmt.Sprintf("%s=%s", f.Name, state)
	}
	return out
}
