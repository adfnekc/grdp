package disp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The layout PDU's shape is checked byte for byte against FreeRDP's sender,
// which writes a header of type and length, then the monitor size, then the
// count, then forty bytes per monitor. The length covering the header as well as
// the body is FreeRDP's arithmetic (8 + 8 + n*40) and is the part a reader
// cannot tell from the bytes alone.
func TestMonitorLayoutBytes(t *testing.T) {
	m := Monitor{
		Flags:              monitorPrimary,
		Left:               0,
		Top:                0,
		Width:              1920,
		Height:             1080,
		PhysicalWidth:      508,
		PhysicalHeight:     285,
		Orientation:        OrientationLandscape,
		DesktopScaleFactor: 100,
		DeviceScaleFactor:  100,
	}
	got := buildMonitorLayout([]Monitor{m})
	if len(got) != headerLength+8+monitorLayoutSize {
		t.Fatalf("layout is %d bytes, want %d", len(got), headerLength+8+monitorLayoutSize)
	}
	if typ := binary.LittleEndian.Uint32(got[0:4]); typ != pduTypeMonitorLayout {
		t.Errorf("type = %#x, want %#x", typ, pduTypeMonitorLayout)
	}
	if n := binary.LittleEndian.Uint32(got[4:8]); int(n) != len(got) {
		t.Errorf("length = %d, want %d (it covers the header as well as the body)", n, len(got))
	}
	if n := binary.LittleEndian.Uint32(got[8:12]); n != monitorLayoutSize {
		t.Errorf("monitor layout size = %d, want %d", n, monitorLayoutSize)
	}
	if n := binary.LittleEndian.Uint32(got[12:16]); n != 1 {
		t.Errorf("monitor count = %d, want 1", n)
	}
	// The monitor entry, in wire order.
	want := []uint32{
		monitorPrimary, // Flags
		0,              // Left
		0,              // Top
		1920, 1080, 508, 285,
		OrientationLandscape, 100, 100,
	}
	for i, w := range want {
		off := 16 + i*4
		if v := binary.LittleEndian.Uint32(got[off : off+4]); v != w {
			t.Errorf("field %d = %d, want %d", i, v, w)
		}
	}
}

// The writer and the reader have to agree, which is the only thing that says
// either of them is right about the order of ten fields of the same width.
func TestMonitorLayoutRoundTrip(t *testing.T) {
	want := []Monitor{
		{Flags: monitorPrimary, Left: 0, Top: 0, Width: 1280, Height: 1024,
			PhysicalWidth: 338, PhysicalHeight: 270, Orientation: OrientationLandscape,
			DesktopScaleFactor: 100, DeviceScaleFactor: 100},
		{Flags: 0, Left: -1280, Top: 0, Width: 800, Height: 600,
			PhysicalWidth: 211, PhysicalHeight: 158, Orientation: OrientationPortrait,
			DesktopScaleFactor: 150, DeviceScaleFactor: 200},
	}
	got, err := ParseMonitorLayout(buildMonitorLayout(want))
	if err != nil {
		t.Fatalf("ParseMonitorLayout: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d monitors, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("monitor %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// A negative origin is signed on the wire and has to survive the round trip:
// a monitor to the left of the primary one has a negative Left.
func TestMonitorLayoutKeepsNegativeCoordinates(t *testing.T) {
	got, err := ParseMonitorLayout(buildMonitorLayout([]Monitor{{Left: -1920, Top: -100}}))
	if err != nil {
		t.Fatalf("ParseMonitorLayout: %v", err)
	}
	if got[0].Left != -1920 || got[0].Top != -100 {
		t.Errorf("origin = (%d,%d), want (-1920,-100)", got[0].Left, got[0].Top)
	}
}

// The count is a wire value. Reading it at face value and then allocating would
// let a short message ask for as many monitors as it liked.
func TestParseMonitorLayoutRejectsAnImpossibleCount(t *testing.T) {
	pdu := buildMonitorLayout([]Monitor{{Flags: monitorPrimary}})
	// Claim four monitors in a message that holds one.
	binary.LittleEndian.PutUint32(pdu[12:16], 4)
	if _, err := ParseMonitorLayout(pdu); err == nil {
		t.Error("a message claiming four monitors in forty bytes was accepted")
	}

	// And a declared length past what arrived.
	pdu = buildMonitorLayout([]Monitor{{Flags: monitorPrimary}})
	binary.LittleEndian.PutUint32(pdu[4:8], 100000)
	if _, err := ParseMonitorLayout(pdu); err == nil {
		t.Error("a declared length of 100000 bytes was accepted")
	}
}

func TestParseMonitorLayoutRejectsRubbish(t *testing.T) {
	if _, err := ParseMonitorLayout([]byte{1, 2, 3}); err == nil {
		t.Error("a three byte message was accepted")
	}
	pdu := buildMonitorLayout([]Monitor{{Flags: monitorPrimary}})
	binary.LittleEndian.PutUint32(pdu[0:4], pduTypeCaps)
	if _, err := ParseMonitorLayout(pdu); err == nil {
		t.Error("a caps message was accepted as a layout")
	}
	pdu = buildMonitorLayout([]Monitor{{Flags: monitorPrimary}})
	binary.LittleEndian.PutUint32(pdu[8:12], 12) // some other monitor size
	if _, err := ParseMonitorLayout(pdu); err == nil {
		t.Error("a layout claiming a twelve byte monitor was accepted")
	}
}

// A size the server does not accept is refused rather than clamped, so that a
// caller is not told its request succeeded when the size was changed behind it.
func TestRequestResizeRefusesSizesOutOfRange(t *testing.T) {
	for _, c := range []struct{ w, h int }{
		{100, 800},  // too narrow
		{9000, 800}, // too wide
		{800, 100},  // too short
		{800, 9000}, // too tall
		{0, 0},
		{-1, 800},
	} {
		d := NewDisplayControlClient()
		d.SetSender(func(uint32, []byte) error { return nil })
		d.OnOpen(1)
		err := d.RequestResize(c.w, c.h)
		if err == nil {
			t.Errorf("RequestResize(%d, %d) succeeded", c.w, c.h)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("outside")) {
			t.Errorf("RequestResize(%d, %d) said %q, which does not explain the range", c.w, c.h, err)
		}
	}
}

// An odd width is rounded down to even, which is what the server requires and
// what FreeRDP sends.
func TestRequestResizeRoundsWidthDownToEven(t *testing.T) {
	var sent []byte
	d := NewDisplayControlClient()
	d.SetSender(func(id uint32, data []byte) error { sent = data; return nil })
	d.OnOpen(7)
	if err := d.RequestResize(1025, 768); err != nil {
		t.Fatalf("RequestResize: %v", err)
	}
	layout, err := ParseMonitorLayout(sent)
	if err != nil {
		t.Fatalf("ParseMonitorLayout: %v", err)
	}
	if layout[0].Width != 1024 {
		t.Errorf("width = %d, want 1024 (1025 rounded down to even)", layout[0].Width)
	}
	if layout[0].Height != 768 {
		t.Errorf("height = %d, want 768", layout[0].Height)
	}
	if layout[0].Flags != monitorPrimary {
		t.Errorf("flags = %#x, want the primary monitor bit", layout[0].Flags)
	}
	if layout[0].Left != 0 || layout[0].Top != 0 {
		t.Errorf("the primary monitor's origin is %d,%d, want 0,0", layout[0].Left, layout[0].Top)
	}
}

// Sending needs an open channel, and saying so is the difference between a
// caller that knows why nothing happened and one that waits.
func TestRequestResizeNeedsAnOpenChannel(t *testing.T) {
	d := NewDisplayControlClient()
	if err := d.RequestResize(1024, 768); err == nil {
		t.Error("RequestResize worked with no channel")
	}
	d.SetSender(func(uint32, []byte) error { return nil })
	if err := d.RequestResize(1024, 768); err == nil {
		t.Error("RequestResize worked with no open channel")
	}
	d.OnOpen(1)
	if err := d.RequestResize(1024, 768); err != nil {
		t.Errorf("RequestResize with an open channel: %v", err)
	}
	d.OnClose()
	if err := d.RequestResize(1024, 768); err == nil {
		t.Error("RequestResize worked after the channel closed")
	}
}

// The server sends its limits once, and they are the only thing that arrives on
// this channel.
func TestCapsAreParsed(t *testing.T) {
	var got Caps
	d := NewDisplayControlClient()
	d.OnCaps(func(c Caps) { got = c })

	pdu := appendUint32(nil, pduTypeCaps)
	pdu = appendUint32(pdu, headerLength+capsBodyLength)
	pdu = appendUint32(pdu, 16)   // MaxNumMonitors
	pdu = appendUint32(pdu, 8192) // MaxMonitorAreaFactorA
	pdu = appendUint32(pdu, 8192) // MaxMonitorAreaFactorB
	d.OnData(pdu)

	if got.MaxNumMonitors != 16 || got.MaxMonitorAreaFactorA != 8192 || got.MaxMonitorAreaFactorB != 8192 {
		t.Errorf("caps = %+v", got)
	}
}

// A message whose declared length is past what arrived would read outside the
// buffer if it were trusted.
func TestOnDataRejectsBadLengths(t *testing.T) {
	d := NewDisplayControlClient()
	fired := false
	d.OnCaps(func(Caps) { fired = true })

	short := appendUint32(nil, pduTypeCaps)
	short = appendUint32(short, 4096) // claims far more than follows
	short = appendUint32(short, 16)
	d.OnData(short)
	if fired {
		t.Error("a message with a length past its end was believed")
	}

	// And a caps message with a truncated body.
	d.OnData(appendUint32(appendUint32(nil, pduTypeCaps), headerLength+4))
	if fired {
		t.Error("a truncated caps message was believed")
	}
}

func TestOnDataIgnoresShortMessages(t *testing.T) {
	d := NewDisplayControlClient()
	fired := false
	d.OnCaps(func(Caps) { fired = true })
	d.OnData([]byte{1, 2, 3})
	d.OnData(nil)
	if fired {
		t.Error("a message shorter than the header produced caps")
	}
}

func TestPhysicalForStaysInRange(t *testing.T) {
	for _, px := range []uint32{0, 1, 200, 1920, 8192, 100000} {
		mm := physicalFor(px)
		if mm < 10 || mm > 10000 {
			t.Errorf("physicalFor(%d) = %d, outside the 10 to 10000 the server accepts", px, mm)
		}
	}
}
