package orders

import (
	"bytes"
	"image"
	"testing"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// The three orders that parsed but were counted as unsupported rather than
// drawn. Each renderer is a method on Screen that the batch dispatch reaches
// with the clip already worked out, so these call the methods directly the way
// screen.go would.

func TestLineToDrawsASegment(t *testing.T) {
	s := NewScreen(8, 8)
	// A green pen. The colour convention is blue, green, red, which is what the
	// parser produces and what the line helper reverses into BGRA.
	dirty := s.lineTo(&pdu.LineTo{
		Startx: 0, Starty: 0, Endx: 7, Endy: 0,
		Pen: pdu.Pen{Colour: [4]uint8{0x00, 0xff, 0x00, 0xff}},
	}, image.Rect(0, 0, 8, 8))

	if want := image.Rect(0, 0, 8, 1); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	for x := 0; x < 8; x++ {
		if got := pixelAt(t, s, x, 0); !bytes.Equal(got, bgra(0x00, 0xff, 0x00)) {
			t.Errorf("(%d,0) is %v, want the pen colour", x, got)
		}
	}
	if got := pixelAt(t, s, 4, 1); bytes.Equal(got, bgra(0x00, 0xff, 0x00)) {
		t.Error("the line spilled to the row below")
	}
}

// The order's bounds are the clip, exactly as they are for the other drawing
// orders, so a line that runs past them is cut by the shared segment clipper.
func TestLineToHonoursTheClip(t *testing.T) {
	s := NewScreen(16, 16)
	dirty := s.lineTo(&pdu.LineTo{
		Startx: 0, Starty: 8, Endx: 15, Endy: 8,
		Pen: pdu.Pen{Colour: [4]uint8{0xff, 0xff, 0xff, 0xff}},
	}, image.Rect(4, 0, 8, 16))

	if want := image.Rect(4, 8, 8, 9); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	if got := pixelAt(t, s, 3, 8); bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Error("a pixel left of the clip was drawn")
	}
	if got := pixelAt(t, s, 8, 8); bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Error("a pixel right of the clip was drawn")
	}
	if got := pixelAt(t, s, 5, 8); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("a pixel inside the clip is %v, want the pen colour", got)
	}
}

// Coordinates accumulate across orders under delta encoding, so a segment can
// name a point millions of pixels away. The clipper has to cut it before the
// step count follows the coordinates, or one order asks for a loop no screen has
// pixels for.
func TestLineToFarAwayCoordinatesDoNotHang(t *testing.T) {
	s := NewScreen(8, 8)
	dirty := s.lineTo(&pdu.LineTo{
		Startx: 0, Starty: 0, Endx: 1 << 30, Endy: 1 << 30,
		Pen: pdu.Pen{Colour: [4]uint8{0xff, 0xff, 0xff, 0xff}},
	}, image.Rect(0, 0, 8, 8))
	if want := image.Rect(0, 0, 8, 8); dirty != want {
		t.Errorf("dirty area is %v, want the whole screen", dirty)
	}
}

func TestLineToNilOrder(t *testing.T) {
	s := NewScreen(4, 4)
	if dirty := s.lineTo(nil, image.Rect(0, 0, 4, 4)); !dirty.Empty() {
		t.Errorf("dirty area is %v, want nothing", dirty)
	}
}

// parsedSaveBitmap builds a SAVE_BITMAP order through its parser, which is the
// only way to set the operation byte from outside the pdu package.
func parsedSaveBitmap(t *testing.T, offset uint32, left, top, right, bottom int32, operation uint8) *pdu.SaveBitmap {
	t.Helper()
	var b bytes.Buffer
	writeU32 := func(v uint32) {
		b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
	}
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU32(offset)
	writeU16(uint16(left))
	writeU16(uint16(top))
	writeU16(uint16(right))
	writeU16(uint16(bottom))
	b.WriteByte(operation)

	const present = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0020
	var d pdu.SaveBitmap
	if err := d.Unpack(bytes.NewReader(b.Bytes()), present, false); err != nil {
		t.Fatalf("save bitmap parse: %v", err)
	}
	return &d
}

// SAVE_BITMAP saves the rectangle it names so that a later restore can put it
// back. The save itself draws nothing; the restore blits the stored pixels.
func TestSaveBitmapSavesAndRestores(t *testing.T) {
	s := NewScreen(8, 8)
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 4, Cy: 4, Colour: [4]uint8{0xff, 0x00, 0x00, 0},
	})})

	// Save the top left four by four, offset 1.
	if dirty := s.saveBitmap(parsedSaveBitmap(t, 1, 0, 0, 3, 3, 0x01), image.Rect(0, 0, 8, 8)); !dirty.Empty() {
		t.Errorf("saving drew %v, want nothing", dirty)
	}

	// Paint the whole screen blue over the saved region.
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 8, Cy: 8, Colour: [4]uint8{0x00, 0x00, 0xff, 0},
	})})

	dirty := s.saveBitmap(parsedSaveBitmap(t, 1, 0, 0, 3, 3, 0x02), image.Rect(0, 0, 8, 8))
	if want := image.Rect(0, 0, 4, 4); dirty != want {
		t.Errorf("restore dirty area is %v, want %v", dirty, want)
	}
	if got := pixelAt(t, s, 1, 1); !bytes.Equal(got, bgra(0xff, 0x00, 0x00)) {
		t.Errorf("the restored pixel is %v, want red", got)
	}
	if got := pixelAt(t, s, 5, 5); !bytes.Equal(got, bgra(0x00, 0x00, 0xff)) {
		t.Errorf("a pixel outside the restored region is %v, want blue", got)
	}
}

// The rectangle is inclusive, so a three by three selection starting at zero has
// four rows and four columns, which is what rdesktop's process_desksave reads.
func TestSaveBitmapRectIsInclusive(t *testing.T) {
	s := NewScreen(8, 8)
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 8, Cy: 8, Colour: [4]uint8{0xff, 0x00, 0x00, 0},
	})})
	s.saveBitmap(parsedSaveBitmap(t, 2, 0, 0, 3, 3, 0x01), image.Rect(0, 0, 8, 8))
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 8, Cy: 8, Colour: [4]uint8{0x00, 0x00, 0xff, 0},
	})})
	s.saveBitmap(parsedSaveBitmap(t, 2, 0, 0, 3, 3, 0x02), image.Rect(0, 0, 8, 8))

	// (3,3) is the last pixel of the inclusive rectangle and has to be restored.
	if got := pixelAt(t, s, 3, 3); !bytes.Equal(got, bgra(0xff, 0x00, 0x00)) {
		t.Errorf("(3,3) is %v, want red: the rectangle should include its last row and column", got)
	}
	if got := pixelAt(t, s, 4, 4); !bytes.Equal(got, bgra(0x00, 0x00, 0xff)) {
		t.Errorf("(4,4) is %v, want blue: it is outside the rectangle", got)
	}
}

// A rectangle that runs past the screen edge is clamped before anything is
// allocated for it, so a coordinate the server picked cannot ask for a buffer
// the order never carried.
func TestSaveBitmapClampsToTheScreen(t *testing.T) {
	s := NewScreen(8, 8)
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 8, Cy: 8, Colour: [4]uint8{0xff, 0x00, 0x00, 0},
	})})
	// A rectangle far larger than the screen; it must be clamped to it.
	s.saveBitmap(parsedSaveBitmap(t, 3, 0, 0, 32767, 32767, 0x01), image.Rect(0, 0, 8, 8))
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 8, Cy: 8, Colour: [4]uint8{0x00, 0x00, 0xff, 0},
	})})
	dirty := s.saveBitmap(parsedSaveBitmap(t, 3, 0, 0, 32767, 32767, 0x02), image.Rect(0, 0, 8, 8))
	if want := image.Rect(0, 0, 8, 8); dirty != want {
		t.Errorf("dirty area is %v, want the whole screen", dirty)
	}
	if got := pixelAt(t, s, 7, 7); !bytes.Equal(got, bgra(0xff, 0x00, 0x00)) {
		t.Errorf("the corner is %v, want red", got)
	}
}

// A restore of an offset that was never saved leaves the screen alone and is
// counted, which is what a client that missed the save would show.
func TestSaveBitmapRestoreOfEmptySlot(t *testing.T) {
	s := NewScreen(4, 4)
	before := s.Pixels()
	dirty := s.saveBitmap(parsedSaveBitmap(t, 9, 0, 0, 2, 2, 0x02), image.Rect(0, 0, 4, 4))
	if !dirty.Empty() {
		t.Errorf("dirty area is %v, want empty", dirty)
	}
	if !bytes.Equal(before, s.Pixels()) {
		t.Error("the screen changed")
	}
	if s.unsupported["savebitmap restore of an empty slot"] == 0 {
		t.Error("the skipped restore was not counted")
	}
}

func TestSaveBitmapUnknownOperationIsCounted(t *testing.T) {
	s := NewScreen(4, 4)
	s.saveBitmap(parsedSaveBitmap(t, 1, 0, 0, 2, 2, 0x03), image.Rect(0, 0, 4, 4))
	if len(s.Unsupported()) == 0 {
		t.Errorf("an unknown operation should be counted: %v", s.Unsupported())
	}
}

// FastIndex is a glyph order and draws out of the same glyph cache TEXT2 does,
// so it is turned into the GlyphIndex DrawText2 already walks.
func TestDrawFastIndexDrawsFromTheGlyphCache(t *testing.T) {
	s := NewScreen(8, 6)
	s.Glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	d := &pdu.FastIndex{
		CacheId:    0,
		FlAccel:    pdu.SO_HORIZONTAL,
		ForeColour: [4]uint8{0xff, 0xff, 0xff, 0xff},
		BackColour: [4]uint8{0x00, 0x00, 0x00, 0xff},
		X:          1,
		Y:          1,
		// Glyph index 1, then a zero advance.
		Data: []byte{0x01, 0x00},
	}
	dirty := s.drawFastIndex(d, image.Rect(0, 0, 8, 6))
	if want := image.Rect(1, 1, 3, 3); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	if got := pixelAt(t, s, 1, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("the glyph's set bit is %v, want white", got)
	}
	if got := pixelAt(t, s, 2, 2); !bytes.Equal(got, bgra(0x00, 0x00, 0x00)) {
		t.Errorf("the glyph's clear bit is %v, want the background", got)
	}
}

// An opBottom of -32768 is "erase all the way", and the low nibble of opTop says
// which of the background rectangle's edges to take for the ones left at zero.
// FreeRDP's update_gdi_fast_index resolves them before it draws.
func TestDrawFastIndexOpBottomSentinel(t *testing.T) {
	s := NewScreen(8, 8)
	s.Glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	d := &pdu.FastIndex{
		CacheId: 0,
		FlAccel: pdu.SO_HORIZONTAL,
		// Blue, in the blue green red convention the order carries.
		BackColour: [4]uint8{0x00, 0x00, 0xff, 0xff},
		ForeColour: [4]uint8{0xff, 0xff, 0xff, 0xff},
		BkLeft:     2, BkTop: 3, BkRight: 6, BkBottom: 7,
		// opBottom says take bkBottom; opLeft and opRight are zero, so they take
		// bkLeft and bkRight.
		OpTop: 0x0001, OpBottom: -32768,
		X: 2, Y: 3,
		Data: []byte{0x01, 0x00},
	}
	s.drawFastIndex(d, image.Rect(0, 0, 8, 8))

	// The resolved rectangle is left 2, top 1, right 6, bottom 7.
	if got := pixelAt(t, s, 3, 2); !bytes.Equal(got, bgra(0x00, 0x00, 0xff)) {
		t.Errorf("a pixel inside the background rectangle is %v, want blue", got)
	}
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, bgra(0x00, 0x00, 0x00)) {
		t.Errorf("a pixel outside the rectangle is %v, want untouched black", got)
	}
}

// The order's bounds clip the drawing, and a nil order or a screen with no glyph
// cache draws nothing rather than panicking.
func TestDrawFastIndexClipsAndHandlesNil(t *testing.T) {
	s := NewScreen(8, 6)
	s.Glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})
	d := &pdu.FastIndex{
		CacheId:    0,
		FlAccel:    pdu.SO_HORIZONTAL,
		ForeColour: [4]uint8{0xff, 0xff, 0xff, 0xff},
		X:          1, Y: 1,
		Data: []byte{0x01, 0x00},
	}
	if dirty := s.drawFastIndex(d, image.Rect(4, 0, 6, 6)); !dirty.Empty() {
		t.Errorf("dirty area is %v, want nothing: the glyph is outside the clip", dirty)
	}
	if dirty := s.drawFastIndex(nil, image.Rect(0, 0, 8, 6)); !dirty.Empty() {
		t.Errorf("a nil order drew %v", dirty)
	}

	s.Glyphs = nil
	if dirty := s.drawFastIndex(d, image.Rect(0, 0, 8, 6)); !dirty.Empty() {
		t.Errorf("a screen with no glyph cache drew %v", dirty)
	}
}
