package pdu

import (
	"bytes"
	"io"
	"testing"

	"github.com/adfnekc/grdp/emission"
)

// captureTransport records everything written to it.
type captureTransport struct {
	*emission.Emitter
	buf bytes.Buffer
}

func newCaptureTransport() *captureTransport {
	return &captureTransport{Emitter: emission.NewEmitter()}
}

func (t *captureTransport) Read(b []byte) (int, error)  { return 0, io.EOF }
func (t *captureTransport) Write(b []byte) (int, error) { return t.buf.Write(b) }
func (t *captureTransport) Close() error                { return nil }

func TestScancodeKeyEventSerialize(t *testing.T) {
	cases := []struct {
		name string
		ev   ScancodeKeyEvent
		want []byte
	}{
		{"enter down", ScancodeKeyEvent{KeyCode: 0x1C}, []byte{0x00, 0x00, 0x1C, 0x00, 0x00, 0x00}},
		{"enter up", ScancodeKeyEvent{KeyboardFlags: KBDFLAGS_RELEASE, KeyCode: 0x1C}, []byte{0x00, 0x80, 0x1C, 0x00, 0x00, 0x00}},
		{"super down (extended)", ScancodeKeyEvent{KeyboardFlags: KBDFLAGS_EXTENDED, KeyCode: 0x5B}, []byte{0x00, 0x01, 0x5B, 0x00, 0x00, 0x00}},
	}
	for _, c := range cases {
		if got := c.ev.Serialize(); !bytes.Equal(got, c.want) {
			t.Errorf("%s: got % X, want % X", c.name, got, c.want)
		}
	}
}

func TestPointerEventSerialize(t *testing.T) {
	ev := PointerEvent{PointerFlags: PTRFLAGS_MOVE | PTRFLAGS_BUTTON1, XPos: 0x0102, YPos: 0x0304}
	want := []byte{0x00, 0x18, 0x02, 0x01, 0x04, 0x03}
	if got := ev.Serialize(); !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

// fakeFastPath captures fast-path input PDUs.
type fakeFastPath struct {
	numEvents byte
	data      []byte
}

func (f *fakeFastPath) SendFastPath(secFlag byte, s []byte) (int, error) { return len(s), nil }
func (f *fakeFastPath) SendFastPathInput(n byte, s []byte) (int, error) {
	f.numEvents = n
	f.data = append([]byte(nil), s...)
	return len(s), nil
}

func TestFastPathInputKeyboard(t *testing.T) {
	cases := []struct {
		name string
		ev   ScancodeKeyEvent
		want []byte
	}{
		{"enter down", ScancodeKeyEvent{KeyCode: 0x1C}, []byte{0x00, 0x1C}},
		{"enter up", ScancodeKeyEvent{KeyboardFlags: KBDFLAGS_RELEASE, KeyCode: 0x1C}, []byte{0x01, 0x1C}},
		{"super down", ScancodeKeyEvent{KeyboardFlags: KBDFLAGS_EXTENDED, KeyCode: 0x5B}, []byte{0x02, 0x5B}},
	}
	for _, c := range cases {
		fp := &fakeFastPath{}
		cl := NewClient(newCaptureTransport())
		cl.SetFastPathSender(fp)
		cl.SendInputEvents(INPUT_EVENT_SCANCODE, []InputEventsInterface{&c.ev})
		if fp.numEvents != 1 {
			t.Errorf("%s: numEvents = %d, want 1", c.name, fp.numEvents)
		}
		if !bytes.Equal(fp.data, c.want) {
			t.Errorf("%s: data = % X, want % X", c.name, fp.data, c.want)
		}
	}
}

func TestFastPathInputMouse(t *testing.T) {
	fp := &fakeFastPath{}
	cl := NewClient(newCaptureTransport())
	cl.SetFastPathSender(fp)
	cl.SendInputEvents(INPUT_EVENT_MOUSE, []InputEventsInterface{
		&PointerEvent{PointerFlags: PTRFLAGS_MOVE, XPos: 968, YPos: 124},
	})
	// eventHeader = eventCode(1)<<5 = 0x20, then pointerFlags, x, y (LE)
	want := []byte{0x20, 0x00, 0x08, 0xC8, 0x03, 0x7C, 0x00}
	if fp.numEvents != 1 {
		t.Fatalf("numEvents = %d, want 1", fp.numEvents)
	}
	if !bytes.Equal(fp.data, want) {
		t.Fatalf("data = % X, want % X", fp.data, want)
	}
}

// TestSendInputEventsFraming verifies the slow-path input PDU layout against
// MS-RDPBCGR 2.2.8.1.1.3.1.1:
//
//	TS_INPUT_EVENT { eventTime(4) messageType(2) slowPathInputData(6) }
func TestSendInputEventsFraming(t *testing.T) {
	tr := newCaptureTransport()
	c := NewClient(tr)
	c.SendInputEvents(INPUT_EVENT_SCANCODE, []InputEventsInterface{
		&ScancodeKeyEvent{KeyCode: 0x1C},
	})
	want := []byte{
		0x00, 0x00, 0x00, 0x00, // eventTime
		0x04, 0x00, // messageType = INPUT_EVENT_SCANCODE
		0x00, 0x00, 0x1C, 0x00, 0x00, 0x00, // keyboardFlags, keyCode, pad
	}
	if !bytes.Contains(tr.buf.Bytes(), want) {
		t.Fatalf("input PDU % X does not contain % X", tr.buf.Bytes(), want)
	}
}

// TestSendMouseEventFraming checks the pointer message type is not mistaken for
// a scancode event (a regression that made the wheel/click path wrong).
func TestSendMouseEventFraming(t *testing.T) {
	tr := newCaptureTransport()
	c := NewClient(tr)
	c.SendInputEvents(INPUT_EVENT_MOUSE, []InputEventsInterface{
		&PointerEvent{PointerFlags: PTRFLAGS_MOVE, XPos: 5, YPos: 7},
	})
	want := []byte{
		0x00, 0x00, 0x00, 0x00,
		0x01, 0x80, // messageType = INPUT_EVENT_MOUSE
		0x00, 0x08, 0x05, 0x00, 0x07, 0x00, // flags, x, y
	}
	if !bytes.Contains(tr.buf.Bytes(), want) {
		t.Fatalf("input PDU % X does not contain % X", tr.buf.Bytes(), want)
	}
}

// The fast-path Unicode keyboard event is three bytes: the event header, whose
// low five bits are the flags, then the two byte code. FreeRDP's server side
// parser is the authority for that shape (fastpath_recv_input_event_unicode in
// libfreerdp/core/fastpath.c reads one UINT16 and takes the release flag from
// eventFlags in the header), and it is the reason this test checks the release
// bit rather than only the code.
//
// The header used to be written as eventCode<<5 alone, which drops the release
// flag: every Unicode event then arrived as a key press, the key never came up,
// and nothing was typed. No server in this repository's reach would say so:
// xrdp advertises INPUT_FLAG_UNICODE and then never delivers the result to X,
// so this is pinned here instead.
func TestFastPathInputUnicode(t *testing.T) {
	cases := []struct {
		name string
		ev   UnicodeKeyEvent
		want []byte
	}{
		{"a down", UnicodeKeyEvent{Unicode: 'a'}, []byte{0x80, 0x61, 0x00}},
		{"a up", UnicodeKeyEvent{KeyboardFlags: KBDFLAGS_RELEASE, Unicode: 'a'}, []byte{0x81, 0x61, 0x00}},
		// A character outside ASCII, which is the point of the event: its code
		// is written little endian, so the low byte comes first.
		{"CJK down", UnicodeKeyEvent{Unicode: 0x4E2D}, []byte{0x80, 0x2D, 0x4E}},
		{"CJK up", UnicodeKeyEvent{KeyboardFlags: KBDFLAGS_RELEASE, Unicode: 0x4E2D}, []byte{0x81, 0x2D, 0x4E}},
	}
	for _, c := range cases {
		fp := &fakeFastPath{}
		cl := NewClient(newCaptureTransport())
		cl.SetFastPathSender(fp)
		cl.SendInputEvents(INPUT_EVENT_UNICODE, []InputEventsInterface{&c.ev})
		if fp.numEvents != 1 {
			t.Errorf("%s: numEvents = %d, want 1", c.name, fp.numEvents)
		}
		if !bytes.Equal(fp.data, c.want) {
			t.Errorf("%s: data = % X, want % X", c.name, fp.data, c.want)
		}
	}
}

// The two halves of a surrogate pair travel as two events in one message, which
// is what makes a supplementary character possible at all: the event carries a
// single 16 bit code. Both events must be present, in order, and the message
// must say so in its event count.
func TestFastPathInputUnicodeSurrogatePair(t *testing.T) {
	high, low := rune(0xD83D), rune(0xDE00) // U+1F600
	fp := &fakeFastPath{}
	cl := NewClient(newCaptureTransport())
	cl.SetFastPathSender(fp)
	cl.SendInputEvents(INPUT_EVENT_UNICODE, []InputEventsInterface{
		&UnicodeKeyEvent{Unicode: uint16(high)},
		&UnicodeKeyEvent{Unicode: uint16(low)},
	})
	if fp.numEvents != 2 {
		t.Fatalf("numEvents = %d, want 2", fp.numEvents)
	}
	want := []byte{0x80, 0x3D, 0xD8, 0x80, 0x00, 0xDE}
	if !bytes.Equal(fp.data, want) {
		t.Errorf("data = % X, want % X", fp.data, want)
	}
}

// The slow path puts the release flag in the real keyboardFlags field instead,
// so the two encodings differ and both have to be right.
func TestSlowPathInputUnicode(t *testing.T) {
	tr := newCaptureTransport()
	cl := NewClient(tr)
	cl.SendInputEvents(INPUT_EVENT_UNICODE, []InputEventsInterface{
		&UnicodeKeyEvent{KeyboardFlags: KBDFLAGS_RELEASE, Unicode: 0x4E2D},
	})
	if tr.buf.Len() == 0 {
		t.Fatal("nothing was sent on the slow path")
	}
	// The event data is keyboardFlags, unicodeCode, pad2Octets, all little
	// endian: release 0x8000, 0x4E2D, 0x0000.
	want := []byte{0x00, 0x80, 0x2D, 0x4E, 0x00, 0x00}
	if !bytes.Contains(tr.buf.Bytes(), want) {
		t.Errorf("slow path data = % X, does not contain % X", tr.buf.Bytes(), want)
	}
}

// The synchronise event carries the toggle state in the event header and has no
// payload, so on the fast path it is a single byte. The slow path's version is
// six bytes and is not the same thing.
func TestFastPathInputSync(t *testing.T) {
	cases := []struct {
		name   string
		toggle uint32
		want   []byte
	}{
		{"all off", 0, []byte{0x60}},
		{"num lock", TS_SYNC_NUM_LOCK, []byte{0x62}},
		{"caps lock", TS_SYNC_CAPS_LOCK, []byte{0x64}},
		{"scroll and num", TS_SYNC_SCROLL_LOCK | TS_SYNC_NUM_LOCK, []byte{0x63}},
		{"kana", TS_SYNC_KANA_LOCK, []byte{0x68}},
	}
	for _, c := range cases {
		fp := &fakeFastPath{}
		cl := NewClient(newCaptureTransport())
		cl.SetFastPathSender(fp)
		cl.SendInputEvents(INPUT_EVENT_SYNC, []InputEventsInterface{
			&SynchronizeEvent{ToggleFlags: c.toggle},
		})
		if fp.numEvents != 1 {
			t.Errorf("%s: numEvents = %d, want 1", c.name, fp.numEvents)
		}
		if !bytes.Equal(fp.data, c.want) {
			t.Errorf("%s: data = % X, want % X", c.name, fp.data, c.want)
		}
	}
}

// The input preamble is a Tab release, the toggle state and another Tab release,
// in one PDU. That is what mstsc and FreeRDP send once the session is ready,
// FreeRDP's comment saying "send a tab up like mstsc.exe".
//
// The three events have three different event codes, which is exactly why this
// could not be sent before: the code used to be worked out once per PDU from the
// msgType and written on every event, so a PDU like this one was not expressible.
func TestFastPathInputPreambleIsOneMixedPDU(t *testing.T) {
	fp := &fakeFastPath{}
	cl := NewClient(newCaptureTransport())
	cl.SetFastPathSender(fp)

	tabUp := &ScancodeKeyEvent{KeyboardFlags: KBDFLAGS_RELEASE, KeyCode: 0x0f}
	cl.SendInputEvents(INPUT_EVENT_SCANCODE, []InputEventsInterface{
		tabUp,
		&SynchronizeEvent{ToggleFlags: TS_SYNC_NUM_LOCK},
		tabUp,
	})

	if fp.numEvents != 3 {
		t.Fatalf("numEvents = %d, want 3", fp.numEvents)
	}
	// scancode release (0x01) + Tab, sync (3<<5) + num lock, scancode release + Tab
	want := []byte{0x01, 0x0f, 0x62, 0x01, 0x0f}
	if !bytes.Equal(fp.data, want) {
		t.Fatalf("data = % X, want % X", fp.data, want)
	}
}

// Events of different types mix in one PDU regardless of the msgType passed for
// the PDU, because each event now works out its own code.
func TestFastPathInputMixesEventTypes(t *testing.T) {
	fp := &fakeFastPath{}
	cl := NewClient(newCaptureTransport())
	cl.SetFastPathSender(fp)

	cl.SendInputEvents(INPUT_EVENT_MOUSE, []InputEventsInterface{
		&PointerEvent{PointerFlags: PTRFLAGS_MOVE, XPos: 10, YPos: 20},
		&ScancodeKeyEvent{KeyCode: 0x1C},
		&SynchronizeEvent{},
	})

	if fp.numEvents != 3 {
		t.Fatalf("numEvents = %d, want 3", fp.numEvents)
	}
	want := []byte{
		0x20, 0x00, 0x08, 0x0A, 0x00, 0x14, 0x00, // mouse move to 10,20
		0x00, 0x1C, // enter down
		0x60, // sync, everything off
	}
	if !bytes.Equal(fp.data, want) {
		t.Fatalf("data = % X, want % X", fp.data, want)
	}
}

// A pointer event still picks up MOUSEX from the msgType, since the event itself
// carries no hint of which of the two encodings the caller wants.
func TestFastPathInputPointerKeepsMouseX(t *testing.T) {
	fp := &fakeFastPath{}
	cl := NewClient(newCaptureTransport())
	cl.SetFastPathSender(fp)
	cl.SendInputEvents(INPUT_EVENT_MOUSEX, []InputEventsInterface{
		&PointerEvent{PointerFlags: PTRFLAGS_MOVE, XPos: 1, YPos: 2},
	})
	// MOUSEX is 2, so the header is 0x40 rather than 0x20.
	if len(fp.data) == 0 || fp.data[0] != 0x40 {
		t.Fatalf("data = % X, want a MOUSEX header of 0x40", fp.data)
	}
}
