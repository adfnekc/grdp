package rdpgfx

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/adfnekc/grdp/codec"
)

// pdu builds a complete RDPGFX PDU: the 8 byte header plus the body.
func pdu(cmdID uint16, body []byte) []byte {
	out := appendHeader(nil, cmdID, 0, uint32(headerSize+len(body)))
	return append(out, body...)
}

func u16(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }
func u32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
func rect16(left, top, right, bottom uint16) []byte {
	return append(append(append(append([]byte{}, u16(left)...), u16(top)...), u16(right)...), u16(bottom)...)
}

// newTestClient returns a client whose output PDUs are captured.
func newTestClient(t *testing.T) (*GfxClient, *[][]byte) {
	t.Helper()
	var sent [][]byte
	c := NewGfxClient()
	c.SetSender(func(_ uint32, data []byte) error {
		cp := make([]byte, len(data))
		copy(cp, data)
		sent = append(sent, cp)
		return nil
	})
	c.OnOpen(7)
	// OnOpen advertises capabilities, which we are not testing here.
	sent = nil
	return c, &sent
}

func TestParseHeader(t *testing.T) {
	// Quadruple check the header layout: cmdId, flags and pduLength are all
	// little endian, and pduLength includes the header.
	raw := pdu(cmdStartFrame, append(u32(1234), u32(42)...))
	if len(raw) != headerSize+8 {
		t.Fatalf("got %d bytes, want %d", len(raw), headerSize+8)
	}
	if got := binary.LittleEndian.Uint16(raw); got != cmdStartFrame {
		t.Fatalf("cmdId = 0x%04x", got)
	}
	if got := binary.LittleEndian.Uint32(raw[4:]); got != uint32(len(raw)) {
		t.Fatalf("pduLength = %d, want %d", got, len(raw))
	}
}

func TestCreateAndWireUncompressed(t *testing.T) {
	c, _ := newTestClient(t)

	// Create a 4x2 surface.
	c.OnData(pdu(cmdCreateSurface, append(append(u16(1), u16(4)...), append(u16(2), 0x20, 0)...)))
	// Fill it with uncompressed BGRA pixels.
	pixels := []byte{
		1, 2, 3, 255, 4, 5, 6, 255, 7, 8, 9, 255, 10, 11, 12, 255,
		13, 14, 15, 255, 16, 17, 18, 255, 19, 20, 21, 255, 22, 23, 24, 255,
	}
	body := append(append(append([]byte{}, u16(1)...), u16(codecUncompressed)...), 0x20, 0)
	body = append(body, rect16(0, 0, 4, 2)...)
	body = append(body, u32(uint32(len(pixels)))...)
	body = append(body, pixels...)
	c.OnData(pdu(cmdWireToSurface1, body))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	if s == nil {
		t.Fatal("surface was not created")
	}
	if got := s.Pixels(); !bytes.Equal(got, pixels) {
		t.Fatalf("got  %v\nwant %v", got, pixels)
	}
}

func TestWireToSurfaceClipping(t *testing.T) {
	c, _ := newTestClient(t)
	c.OnData(pdu(cmdCreateSurface, append(append(u16(1), u16(4)...), append(u16(4), 0x20, 0)...)))

	// A 2x2 update at (3,3) on a 4x4 surface: only (3,3) is inside.
	pixels := []byte{
		1, 1, 1, 255, 2, 2, 2, 255,
		3, 3, 3, 255, 4, 4, 4, 255,
	}
	body := append(append(append([]byte{}, u16(1)...), u16(codecUncompressed)...), 0x20, 0)
	body = append(body, rect16(3, 3, 5, 5)...)
	body = append(body, u32(uint32(len(pixels)))...)
	body = append(body, pixels...)
	c.OnData(pdu(cmdWireToSurface1, body))

	c.mu.Lock()
	got := c.surfaces[1].Pixels()
	c.mu.Unlock()

	// Only the top left pixel of the update should have landed.
	if !bytes.Equal(got[15*4:16*4], []byte{1, 1, 1, 255}) {
		t.Fatalf("pixel (3,3) = %v", got[15*4:16*4])
	}
	// The rest of the surface must be untouched.
	for i, v := range got {
		if i >= 15*4 && i < 16*4 {
			continue
		}
		if v != 0 {
			t.Fatalf("pixel byte %d was modified: %d", i, v)
		}
	}
}

func TestWireToSurfaceRejectsUnknownSurface(t *testing.T) {
	c, _ := newTestClient(t)
	body := append(append(append([]byte{}, u16(99)...), u16(codecUncompressed)...), 0x20, 0)
	body = append(body, rect16(0, 0, 1, 1)...)
	body = append(body, u32(4)...)
	body = append(body, 0, 0, 0, 255)
	c.OnData(pdu(cmdWireToSurface1, body))

	c.mu.Lock()
	n := len(c.surfaces)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no surfaces, got %d", n)
	}
}

// TestWireToSurfaceRemoteFX is the interesting one: it wraps a RemoteFX
// payload that was produced by libfreerdp's encoder inside a real
// WireToSurface PDU and checks that the surface ends up with the pixels
// libfreerdp's own decoder produces. That covers the EGFX plumbing, the codec
// dispatch and the RFX decoder in one go.
func TestWireToSurfaceRemoteFX(t *testing.T) {
	const vector = "../../codec/testdata/rfx_64x64_rlgr3.bin"
	const reference = "../../codec/testdata/rfx_dec_64x64_rlgr3.bin"

	rfxData, err := os.ReadFile(vector)
	if err != nil {
		t.Skipf("missing RFX vector; run scripts/gen-codec-vectors.sh")
	}
	want, err := os.ReadFile(reference)
	if err != nil {
		t.Skipf("missing RFX reference; run scripts/gen-codec-vectors.sh")
	}

	old := codec.RFXMode
	codec.RFXMode = codec.RLGR3
	defer func() { codec.RFXMode = old }()

	c, _ := newTestClient(t)
	c.OnData(pdu(cmdCreateSurface, append(append(u16(5), u16(64)...), append(u16(64), 0x20, 0)...)))

	body := append(append(append([]byte{}, u16(5)...), u16(codecCAVideo)...), 0x20, 0)
	body = append(body, rect16(0, 0, 64, 64)...)
	body = append(body, u32(uint32(len(rfxData)))...)
	body = append(body, rfxData...)
	c.OnData(pdu(cmdWireToSurface1, body))

	c.mu.Lock()
	s := c.surfaces[5]
	c.mu.Unlock()
	if s == nil {
		t.Fatal("surface was not created")
	}
	got := s.Pixels()
	if !bytes.Equal(got, want) {
		t.Fatalf("surface differs from the libfreerdp decoder output")
	}
}

func TestStartAndEndFrame(t *testing.T) {
	c, sent := newTestClient(t)

	c.OnData(pdu(cmdStartFrame, append(u32(999), u32(3)...)))
	c.OnData(pdu(cmdEndFrame, u32(3)))

	// End frame must be followed by an acknowledge, or the server stops
	// sending frames.
	if len(*sent) != 1 {
		t.Fatalf("got %d PDUs, want 1 frame acknowledge", len(*sent))
	}
	ack := (*sent)[0]
	if got := binary.LittleEndian.Uint16(ack); got != cmdFrameAcknowledge {
		t.Fatalf("cmdId = 0x%04x, want 0x%04x", got, cmdFrameAcknowledge)
	}
	if got := binary.LittleEndian.Uint32(ack[4:]); got != headerSize+12 {
		t.Fatalf("pduLength = %d, want %d", got, headerSize+12)
	}
	if got := binary.LittleEndian.Uint32(ack[12:]); got != 3 {
		t.Fatalf("frameId = %d, want 3", got)
	}
}

func TestResetGraphicsClearsSurfaces(t *testing.T) {
	c, _ := newTestClient(t)
	c.OnData(pdu(cmdCreateSurface, append(append(u16(1), u16(8)...), append(u16(8), 0x20, 0)...)))
	// width(4) height(4) monitorCount(4)
	reset := append(append(u32(1920), u32(1080)...), u32(0)...)
	c.OnData(pdu(cmdResetGraphics, reset))

	c.mu.Lock()
	w, h, n := c.width, c.height, len(c.surfaces)
	c.mu.Unlock()
	if w != 1920 || h != 1080 {
		t.Fatalf("got %dx%d, want 1920x1080", w, h)
	}
	if n != 0 {
		t.Fatalf("reset should drop surfaces, still have %d", n)
	}
}

func TestDeleteSurface(t *testing.T) {
	c, _ := newTestClient(t)
	c.OnData(pdu(cmdCreateSurface, append(append(u16(1), u16(2)...), append(u16(2), 0x20, 0)...)))
	c.OnData(pdu(cmdDeleteSurface, u16(1)))
	c.mu.Lock()
	n := len(c.surfaces)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("surface was not deleted, %d left", n)
	}
}

func TestSolidFill(t *testing.T) {
	c, _ := newTestClient(t)
	c.OnData(pdu(cmdCreateSurface, append(append(u16(1), u16(4)...), append(u16(4), 0x20, 0)...)))

	// RDPGFX_COLOR32 is XRGB: 0x00332211 means R=0x33 G=0x22 B=0x11.
	body := append(append([]byte{}, u16(1)...), u32(0x00332211)...)
	body = append(body, u16(1)...)
	body = append(body, rect16(1, 1, 3, 2)...)
	c.OnData(pdu(cmdSolidFill, body))

	c.mu.Lock()
	got := c.surfaces[1].Pixels()
	c.mu.Unlock()

	if !bytes.Equal(got[1*4*4+1*4:1*4*4+2*4], []byte{0x11, 0x22, 0x33, 0xFF}) {
		t.Fatalf("pixel (1,1) = %v, want [17 34 51 255]", got[1*4*4+1*4:1*4*4+2*4])
	}
	if !bytes.Equal(got[1*4*4+2*4:1*4*4+3*4], []byte{0x11, 0x22, 0x33, 0xFF}) {
		t.Fatalf("pixel (2,1) was not filled")
	}
	if got[0] != 0 {
		t.Fatalf("pixel (0,0) should be untouched, got %d", got[0])
	}
}

func TestSurfaceToSurface(t *testing.T) {
	c, _ := newTestClient(t)
	c.OnData(pdu(cmdCreateSurface, append(append(u16(1), u16(2)...), append(u16(2), 0x20, 0)...)))
	c.OnData(pdu(cmdCreateSurface, append(append(u16(2), u16(4)...), append(u16(4), 0x20, 0)...)))

	// Fill surface 1 with known pixels.
	pixels := []byte{
		1, 1, 1, 255, 2, 2, 2, 255,
		3, 3, 3, 255, 4, 4, 4, 255,
	}
	body := append(append(append([]byte{}, u16(1)...), u16(codecUncompressed)...), 0x20, 0)
	body = append(body, rect16(0, 0, 2, 2)...)
	body = append(body, u32(uint32(len(pixels)))...)
	body = append(body, pixels...)
	c.OnData(pdu(cmdWireToSurface1, body))

	// source(2) dest(2) destPointsCount(2) destPoint(4) srcRect(8)
	body = append(append([]byte{}, u16(1)...), u16(2)...)
	body = append(body, u16(1)...)
	body = append(body, u16(2)...) // destination x
	body = append(body, u16(1)...) // destination y
	body = append(body, rect16(0, 0, 2, 2)...)
	c.OnData(pdu(cmdSurfaceToSurface, body))

	c.mu.Lock()
	got := c.surfaces[2].Pixels()
	c.mu.Unlock()

	// Surface 2 is 4 wide, so (2,1) and (3,1) should hold the copied pixels.
	want := []byte{1, 1, 1, 255, 2, 2, 2, 255}
	if !bytes.Equal(got[(1*4+2)*4:(1*4+4)*4], want) {
		t.Fatalf("got %v, want %v", got[(1*4+2)*4:(1*4+4)*4], want)
	}
}

func TestMultiplePdusInOnePayload(t *testing.T) {
	c, _ := newTestClient(t)
	// Two completions back to back: a create followed by a delete.
	payload := append(
		pdu(cmdCreateSurface, append(append(u16(1), u16(2)...), append(u16(2), 0x20, 0)...)),
		pdu(cmdDeleteSurface, u16(1))...)

	c.OnData(payload)

	c.mu.Lock()
	n := len(c.surfaces)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("both PDUs should have been processed, %d surfaces left", n)
	}
}

func TestShortAndUnknownPdusAreIgnored(t *testing.T) {
	c, _ := newTestClient(t)

	// A header claiming a length longer than the payload must not be acted on.
	bogus := appendHeader(nil, cmdCreateSurface, 0, 9999)
	c.OnData(append(bogus, u16(1)...))

	// An unknown command with a valid length must be skipped, and must not
	// stop the following command from being processed.
	payload := append(pdu(0x7777, []byte{1, 2, 3, 4}), pdu(cmdDeleteSurface, u16(1))...)
	c.OnData(payload)

	c.mu.Lock()
	n := len(c.surfaces)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("got %d surfaces", n)
	}
}

func TestCapsAdvertiseOffersNoAVC(t *testing.T) {
	c := NewGfxClient()
	var sent [][]byte
	c.SetSender(func(_ uint32, data []byte) error {
		sent = append(sent, data)
		return nil
	})
	c.OnOpen(1)

	if len(sent) != 1 {
		t.Fatalf("got %d PDUs, want 1 capabilities advertise", len(sent))
	}
	p := sent[0]
	if got := binary.LittleEndian.Uint16(p); got != cmdCapsAdvertise {
		t.Fatalf("cmdId = 0x%04x", got)
	}
	count := binary.LittleEndian.Uint16(p[8:])
	if count != 2 {
		t.Fatalf("got %d capsets, want 2", count)
	}
	// version(4) length(4) flags(4) per capset.
	if got := binary.LittleEndian.Uint32(p[10:]); got != CapsVersion8 {
		t.Fatalf("first capset version = 0x%08x", got)
	}
	if got := binary.LittleEndian.Uint32(p[14:]); got != 4 {
		t.Fatalf("first capset length = %d, want 4", got)
	}
	if got := binary.LittleEndian.Uint32(p[18:]); got != 0 {
		t.Fatalf("first capset flags = 0x%08x, want 0 (no AVC)", got)
	}
	if got := binary.LittleEndian.Uint32(p[22:]); got != CapsVersion81 {
		t.Fatalf("second capset version = 0x%08x", got)
	}
}
