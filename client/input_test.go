package client

import (
	"bytes"
	"io"
	"testing"

	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/protocol/pdu"
)

type capTransport struct{ emission.Emitter }

func (t *capTransport) Read(b []byte) (int, error)  { return 0, io.EOF }
func (t *capTransport) Write(b []byte) (int, error) { return len(b), nil }
func (t *capTransport) Close() error                { return nil }

type capFastPath struct {
	numEvents byte
	data      []byte
}

func (f *capFastPath) SendFastPath(secFlag byte, s []byte) (int, error) { return len(s), nil }
func (f *capFastPath) SendFastPathInput(n byte, s []byte) (int, error) {
	f.numEvents = n
	f.data = append([]byte(nil), s...)
	return len(s), nil
}

// newInputClient returns a client wired to capture what its pointer events look
// like on the wire, without a connection.
func newInputClient(fp *capFastPath) *RdpClient {
	c := &RdpClient{}
	c.pdu = pdu.NewClient(&capTransport{Emitter: *emission.NewEmitter()})
	c.pdu.SetFastPathSender(fp)
	return c
}

// Button events are PTRFLAGS_DOWN plus the button, and nothing else.
//
// PTRFLAGS_MOVE does not belong here. Movement and buttons are separate kinds of
// pointer event in MS-RDPBCGR 2.2.8.1.1.3.1.1.3: a movement event carries
// PTRFLAGS_MOVE, and a button event carries PTRFLAGS_DOWN together with the
// button that was pressed or released. FreeRDP's own clients send exactly that,
// which is what xf_event.c shows, and a move carries PTRFLAGS_MOVE alone.
//
// Sending both together is what made a Windows 10 build 19041 target ignore every
// click while still accepting movement. xrdp only looks at the button bits, so
// clicks worked there and the difference went unnoticed.
func TestPointerButtonEventsCarryNoMoveFlag(t *testing.T) {
	cases := []struct {
		name string
		call func(c *RdpClient)
		want []byte
	}{
		// The event byte first, then pointerFlags little endian, then x and y.
		{"left down", func(c *RdpClient) { c.MouseDown(0, 0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0x90, 0x23, 0x01, 0x56, 0x04}},
		{"left up", func(c *RdpClient) { c.MouseUp(0, 0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0x10, 0x23, 0x01, 0x56, 0x04}},
		// Button 1 is the middle button, which is PTRFLAGS_BUTTON3.
		{"middle down", func(c *RdpClient) { c.MouseDown(1, 0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0xC0, 0x23, 0x01, 0x56, 0x04}},
		{"middle up", func(c *RdpClient) { c.MouseUp(1, 0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0x40, 0x23, 0x01, 0x56, 0x04}},
		// Button 2 is the right button, which is PTRFLAGS_BUTTON2.
		{"right down", func(c *RdpClient) { c.MouseDown(2, 0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0xA0, 0x23, 0x01, 0x56, 0x04}},
		{"right up", func(c *RdpClient) { c.MouseUp(2, 0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0x20, 0x23, 0x01, 0x56, 0x04}},
		// A move is a move and carries no button at all.
		{"move", func(c *RdpClient) { c.MouseMove(0x0123, 0x0456) },
			[]byte{0x20, 0x00, 0x08, 0x23, 0x01, 0x56, 0x04}},
	}
	for _, tc := range cases {
		fp := &capFastPath{}
		c := newInputClient(fp)
		tc.call(c)
		if fp.numEvents != 1 {
			t.Errorf("%s: numEvents = %d, want 1", tc.name, fp.numEvents)
			continue
		}
		if !bytes.Equal(fp.data, tc.want) {
			t.Errorf("%s: data = % X, want % X", tc.name, fp.data, tc.want)
		}
	}
}

// A button number that means nothing sends nothing at all, rather than a press
// with no button bit, which the spec forbids and a server would reject.
func TestMouseButtonOutOfRangeSendsNothing(t *testing.T) {
	for _, button := range []int{3, -1, 99} {
		fp := &capFastPath{}
		c := newInputClient(fp)
		c.MouseDown(button, 10, 20)
		c.MouseUp(button, 10, 20)
		if fp.numEvents != 0 || len(fp.data) != 0 {
			t.Errorf("button %d: sent % X, want nothing", button, fp.data)
		}
	}
}

// The wheel's rotation is a signed value inside a nine bit field, and
// PTRFLAGS_WHEEL_NEGATIVE is that field's sign bit rather than a flag to set
// beside a magnitude.
//
// MS-RDPBCGR defines PTRFLAGS_WHEEL_NEGATIVE as saying that "the wheel rotation
// value (contained in the WheelRotationMask bit field) is negative and MUST be
// sign-extended before injection at the server", so the field carries the
// negative value itself. A notch down is therefore 0x0188, the two's complement
// of -120, and not 0x0100|0x78, which a server sign-extends to -136. FreeRDP's
// X11 client sent the second form until 2023 and changed it to 0x188 for exactly
// this reason.
//
// A wheel event also carries no PTRFLAGS_MOVE: "the only valid flags in a vertical
// wheel rotation event are PTRFLAGS_WHEEL_NEGATIVE and the WheelRotationMask; all
// other pointer flags are ignored", and a stray movement bit on an event is what
// made a Windows target drop button events.
func TestMouseWheelCarriesASignedRotation(t *testing.T) {
	cases := []struct {
		scroll int
		want   []byte
	}{
		{WheelDelta, []byte{0x20, 0x78, 0x02, 0x23, 0x01, 0x56, 0x04}},
		{-WheelDelta, []byte{0x20, 0x88, 0x03, 0x23, 0x01, 0x56, 0x04}},
		{1, []byte{0x20, 0x01, 0x02, 0x23, 0x01, 0x56, 0x04}},
		{-1, []byte{0x20, 0xff, 0x03, 0x23, 0x01, 0x56, 0x04}},
		{0, []byte{0x20, 0x00, 0x02, 0x23, 0x01, 0x56, 0x04}},
		// Past the ends of the field: clamped, not wrapped, because wrapping a
		// scroll up into a scroll down would be worse than a shorter scroll.
		{1000, []byte{0x20, 0xff, 0x02, 0x23, 0x01, 0x56, 0x04}},
		{-1000, []byte{0x20, 0x00, 0x03, 0x23, 0x01, 0x56, 0x04}},
	}
	for _, tc := range cases {
		fp := &capFastPath{}
		c := newInputClient(fp)
		c.MouseWheel(tc.scroll, 0x0123, 0x0456)
		if !bytes.Equal(fp.data, tc.want) {
			t.Errorf("MouseWheel(%d): data = % X, want % X", tc.scroll, fp.data, tc.want)
		}
	}
}

// What a server does with the field, done here: mask it out of the flags and sign
// extend it. Every value whose two's complement fits must come back as itself.
func TestMouseWheelRotationSignExtends(t *testing.T) {
	decode := func(event []byte) int {
		flags := int(event[1]) | int(event[2])<<8
		v := flags & pdu.WheelRotationMask
		if v&pdu.PTRFLAGS_WHEEL_NEGATIVE != 0 {
			v -= 1 << 9
		}
		return v
	}
	for _, scroll := range []int{-256, -255, -120, -2, -1, 0, 1, 2, 120, 255} {
		fp := &capFastPath{}
		c := newInputClient(fp)
		c.MouseWheel(scroll, 0, 0)
		if len(fp.data) != 7 {
			t.Fatalf("MouseWheel(%d): sent % X, want one wheel event", scroll, fp.data)
		}
		if got := decode(fp.data); got != scroll {
			t.Errorf("MouseWheel(%d): the server reads %d", scroll, got)
		}
	}
}
