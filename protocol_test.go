package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The expected bodies come straight out of python/captures/, i.e. what UAD
// Console sent. Ours have to be byte-identical.

var testSender = [8]byte{0x02, 0x1a, 0x2b, 0x3c, 0x4d, 0x5e, 0x00, 0x00}

func bodyOf(t *testing.T, pkt []byte) string {
	t.Helper()
	f, ok := parseFrame(pkt)
	if !ok {
		t.Fatalf("could not parse our own packet: %s", hex.EncodeToString(pkt))
	}
	return hex.EncodeToString(f.Body)
}

func TestFrameHeader(t *testing.T) {
	pkt := buildFrame(vendorDante, []byte{0xaa, 0xbb}, testSender, 0x1234)
	got := hex.EncodeToString(pkt)
	want := "ffff" + "001a" + "1234" + "0000" +
		"021a2b3c4d5e0000" + hex.EncodeToString(vendorDante) + "aabb"
	if got != want {
		t.Fatalf("wrong frame\n got: %s\nwant: %s", got, want)
	}
}

func TestGainSetMatchesCapture(t *testing.T) {
	// params.pcap: 01 02 00 06 00 04 43 28 40 18  ->  VV=0x18 = 33 dB
	pkt, err := buildGainSet(33, testSender, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bodyOf(t, pkt), "010200060004432840"+"18"; got != want {
		t.Fatalf("gain body\n got: %s\nwant: %s", got, want)
	}
}

func TestGainRange(t *testing.T) {
	for _, db := range []int{gainMinDB, gainMaxDB} {
		if _, err := dbToVV(db); err != nil {
			t.Fatalf("%d dB should be valid: %v", db, err)
		}
	}
	for _, db := range []int{gainMinDB - 1, gainMaxDB + 1, 0, 200} {
		if _, err := dbToVV(db); err == nil {
			t.Fatalf("%d dB should have been rejected", db)
		}
	}
	// dB = VV + 9, linear.
	if vvToDB(0x01) != 10 || vvToDB(0x38) != 65 {
		t.Fatalf("gain conversion broken: 0x01=%d 0x38=%d", vvToDB(0x01), vvToDB(0x38))
	}
}

func TestStatusQueryMatchesCapture(t *testing.T) {
	// params.pcap: write flag cleared.
	want := "07390140000f424000000001000800100000000000000000"
	if got := bodyOf(t, buildStatusQuery(testSender, 7)); got != want {
		t.Fatalf("query body\n got: %s\nwant: %s", got, want)
	}
}

func TestFlagSetMatchesCapture(t *testing.T) {
	const prefix = "07390140000f424000010001000800100000"
	cases := []struct {
		name  string
		on    bool
		wantV string
	}{
		{"48v", true, "0001"},
		{"48v", false, "0000"},
		{"mute", true, "0002"},
		{"mute", false, "0000"},
		{"phase", true, "0004"},
		{"phase", false, "0000"},
		{"hpf", true, "0008"},
		{"hpf", false, "0000"},
		{"pad", true, "0010"},
		{"pad", false, "0000"},
	}
	for _, c := range cases {
		f, ok := flagByName(c.name)
		if !ok {
			t.Fatalf("unknown flag %s", c.name)
		}
		value := uint32(0)
		if c.on {
			value = f.Mask
		}
		mask := hex.EncodeToString([]byte{byte(f.Mask >> 8), byte(f.Mask)})
		want := prefix + mask + "0000" + c.wantV
		if got := bodyOf(t, buildFlagSet(f.Mask, value, testSender, 3)); got != want {
			t.Fatalf("%s=%v\n got: %s\nwant: %s", c.name, c.on, got, want)
		}
	}
}

func TestStatusFromBodyMatchesCapture(t *testing.T) {
	// params.pcap reply: detect=0x80, cap=0x3f, flags=0x00.
	raw, err := hex.DecodeString(strings.ReplaceAll(
		"07390141 00000000 00000001 00140010 00000000 000000c0 00000080 0000003f 00000000", " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := statusFromBody(raw)
	if !ok {
		t.Fatal("the reply was not recognised")
	}
	if st.Detect != 0x80 || st.Cap != 0x3f || st.Flags != 0x00 {
		t.Fatalf("wrong decoding: %+v", st)
	}
	if st.Detect&detectConnected == 0 {
		t.Fatal("detect=0x80 means something is plugged into the XLR input")
	}
	// Short bodies are rejected, not panics.
	if _, ok := statusFromBody(raw[:20]); ok {
		t.Fatal("a too short body was accepted")
	}
}

func TestGainFromBody(t *testing.T) {
	// The device multicasts the blob without the 01 02 header.
	if db, ok := gainFromBody([]byte{0x43, 0x28, 0x40, 0x18}); !ok || db != 33 {
		t.Fatalf("gain state: db=%d ok=%v", db, ok)
	}
	if _, ok := gainFromBody([]byte{0x07, 0x39, 0x01, 0x41}); ok {
		t.Fatal("an Audinate body was read as gain")
	}
	if _, ok := gainFromBody([]byte{0x43, 0x28}); ok {
		t.Fatal("a too short blob was accepted")
	}
}

func TestParseFrameRejectsRubbish(t *testing.T) {
	if _, ok := parseFrame([]byte{0x00, 0x01, 0x02}); ok {
		t.Fatal("a too short packet was accepted")
	}
	if _, ok := parseFrame(make([]byte, 40)); ok {
		t.Fatal("a packet without the magic was accepted")
	}
	// The length field must not point past the buffer.
	pkt := buildFrame(vendorUA, []byte{1, 2, 3, 4}, testSender, 1)
	pkt[2], pkt[3] = 0xff, 0xff
	f, ok := parseFrame(pkt)
	if !ok || len(f.Body) != 4 {
		t.Fatalf("length field past the buffer is mishandled: ok=%v len=%d", ok, len(f.Body))
	}
}

func TestSeqCounterAdvances(t *testing.T) {
	c := newSeqCounter()
	first := c.next()
	seen := map[uint16]bool{first: true}
	for i := 0; i < 1000; i++ {
		n := c.next()
		if seen[n] {
			t.Fatalf("sequence number %d repeated after %d steps", n, i)
		}
		seen[n] = true
	}
}
