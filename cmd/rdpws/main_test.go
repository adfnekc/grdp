package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/adfnekc/grdp/client"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// The frame encoder needs a *client.Framebuffer, which only a live session can
// build, so it is covered by -selftest rather than here: that drives the real
// thing and checks the JPEG decodes. What is checked here is the cursor encoding,
// which is pure, and the pieces of the protocol between the page and the client.

// A shape cursor goes as a PNG with alpha, and its hotspot goes in the header
// where the page uses it to line the pointer up with the mouse. Without the
// hotspot the pointer is drawn beside the mouse rather than under it, which
// looks like a rendering bug and is not.
func TestEncodeCursorCarriesAlphaAndHotspot(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 0})   // transparent
	img.Set(1, 0, color.RGBA{R: 255, A: 255}) // opaque
	cur := &client.Cursor{Image: img, Width: 2, Height: 3, Hotspot: image.Point{X: 1, Y: 2}}

	msg := encodeCursor(cur)
	if msg == nil {
		t.Fatal("no message")
	}
	if got := binary.LittleEndian.Uint16(msg[0:2]); got != msgCursor {
		t.Fatalf("type = %d, want %d", got, msgCursor)
	}
	if got := binary.LittleEndian.Uint16(msg[2:4]); got != 1 {
		t.Errorf("hotspot x = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint16(msg[4:6]); got != 2 {
		t.Errorf("hotspot y = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(msg[6:8]); got != 2 {
		t.Errorf("width = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(msg[8:10]); got != 3 {
		t.Errorf("height = %d, want 3", got)
	}
	decoded, err := png.Decode(bytes.NewReader(msg[binaryHeader:]))
	if err != nil {
		t.Fatalf("payload is not a PNG: %v", err)
	}
	if _, _, _, a := decoded.At(0, 0).RGBA(); a != 0 {
		t.Errorf("the transparent pixel has alpha %d, want 0", a>>8)
	}
	if _, _, _, a := decoded.At(1, 0).RGBA(); a == 0 {
		t.Error("the opaque pixel came out transparent")
	}
}

// A system cursor has no pixels, so it is sent as a marker rather than an image.
// Sending an empty PNG instead would have the page draw nothing, which is the
// failure this path exists to avoid: a caller that is never told the server
// changed the pointer keeps drawing the last one it had.
func TestEncodeCursorMarksSystemCursors(t *testing.T) {
	for _, st := range []uint32{pdu.SYSPTR_NULL, pdu.SYSPTR_DEFAULT} {
		msg := encodeCursor(&client.Cursor{System: true, SystemType: st})
		if msg == nil {
			t.Fatalf("%#x: no message", st)
		}
		if got := binary.LittleEndian.Uint16(msg[0:2]); got != msgCursor {
			t.Errorf("%#x: type = %d", st, got)
		}
		if got := binary.LittleEndian.Uint16(msg[8:10]); got != 0 {
			t.Errorf("%#x: the marker has height %d, want 0 (that is what makes it a marker)", st, got)
		}
		if len(msg) != binaryHeader {
			t.Errorf("%#x: a system cursor carried %d payload bytes, want none",
				st, len(msg)-binaryHeader)
		}
	}
}

// The two kinds of message have to be told apart by the first two bytes, because
// that is all the reader looks at before deciding what the rest is.
func TestMessageTypesAreDistinct(t *testing.T) {
	types := map[string]uint16{"frame": msgFrame, "cursor": msgCursor, "size": msgSize}
	seen := map[uint16]string{}
	for name, v := range types {
		if other, dup := seen[v]; dup {
			t.Errorf("%s and %s share the type %d", name, other, v)
		}
		seen[v] = name
	}
	if binaryHeader%2 != 0 {
		t.Errorf("the header is %d bytes, which is not a whole number of uint16 fields", binaryHeader)
	}
}

// Keys go as scancodes so that shortcuts work, which is why the mapping exists
// at all. A browser reports a key by name and the server wants a scancode.
func TestBrowserScancodesArePlausible(t *testing.T) {
	// Enter and the arrows are the ones a terminal needs, and the extended
	// arrows carry the 0xE0 prefix the client splits into the extended flag.
	if got := browserScancodes["Enter"]; got != 0x1c {
		t.Errorf("Enter = %#x, want 0x1c", got)
	}
	for _, k := range []string{"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Delete"} {
		sc, ok := browserScancodes[k]
		if !ok {
			t.Errorf("%s has no scancode", k)
			continue
		}
		if sc&0xFF00 != 0xE000 {
			t.Errorf("%s = %#x, which is not marked extended", k, sc)
		}
	}
	// Nothing may map to zero: that is not a scancode, and sending it would put
	// an undefined key in the server's input queue.
	for name, sc := range browserScancodes {
		if sc == 0 {
			t.Errorf("%s maps to zero", name)
		}
	}
}
