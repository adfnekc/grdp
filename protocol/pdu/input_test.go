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
