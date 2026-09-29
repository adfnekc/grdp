package orders

import (
	"bytes"
	"image"
	"testing"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// The multi-rectangle orders and MEM3BLT draw through DrawMulti, the one call
// the batch dispatch adds. These exercise it directly, because the dispatch in
// Screen.drawOne is not this file's to change.

func TestDrawMultiDstBltPaintsRectangles(t *testing.T) {
	s := NewScreen(16, 8)
	o := order(&pdu.MultiDstBlt{
		Opcode:        ropWhite,
		NumRectangles: 2,
		Rectangles: []pdu.DeltaRect{
			{Left: 1, Top: 1, Width: 2, Height: 2},
			{Left: 6, Top: 3, Width: 1, Height: 1},
		},
	})
	dirty := s.DrawMulti(&o, image.Rect(0, 0, 16, 8))
	if want := image.Rect(1, 1, 7, 4); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	for _, p := range [][2]int{{1, 1}, {2, 2}, {6, 3}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
			t.Errorf("(%d,%d) is %v, want the fill colour", p[0], p[1], got)
		}
	}
	if got := pixelAt(t, s, 4, 2); bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Error("the fill spilled between the two rectangles")
	}
}

func TestDrawMultiPatBltFillsRectangles(t *testing.T) {
	s := NewScreen(8, 4)
	o := order(&pdu.MultiPatBlt{
		Opcode:        ropPatCopy,
		FgColour:      [4]uint8{0x11, 0x22, 0x33, 0},
		NumRectangles: 1,
		Rectangles:    []pdu.DeltaRect{{Left: 2, Top: 1, Width: 3, Height: 2}},
	})
	dirty := s.DrawMulti(&o, image.Rect(0, 0, 8, 4))
	if want := image.Rect(2, 1, 5, 3); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	if got := pixelAt(t, s, 3, 2); !bytes.Equal(got, bgra(0x11, 0x22, 0x33)) {
		t.Errorf("filled pixel is %v, want the foreground colour", got)
	}
	if got := pixelAt(t, s, 0, 0); bytes.Equal(got, bgra(0x11, 0x22, 0x33)) {
		t.Error("the fill spilled outside the rectangle")
	}
}

func TestDrawMultiOpaqueRectFillsRectangles(t *testing.T) {
	s := NewScreen(8, 4)
	o := order(&pdu.MultiOpaqueRect{
		Colour:        [4]uint8{0x40, 0x50, 0x60, 0},
		NumRectangles: 2,
		Rectangles: []pdu.DeltaRect{
			{Left: 0, Top: 0, Width: 2, Height: 1},
			{Left: 5, Top: 2, Width: 1, Height: 1},
		},
	})
	s.DrawMulti(&o, image.Rect(0, 0, 8, 4))
	want := bgra(0x40, 0x50, 0x60)
	for _, p := range [][2]int{{0, 0}, {1, 0}, {5, 2}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, want) {
			t.Errorf("(%d,%d) is %v, want the fill colour", p[0], p[1], got)
		}
	}
	if got := pixelAt(t, s, 2, 0); bytes.Equal(got, want) {
		t.Error("the fill spilled between the two rectangles")
	}
}

func TestDrawMultiScrBltCopiesPixels(t *testing.T) {
	s := NewScreen(8, 2)
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 1, Cy: 1, Colour: [4]uint8{0x11, 0x22, 0x33, 0},
	})})
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 1, Y: 0, Cx: 1, Cy: 1, Colour: [4]uint8{0x44, 0x55, 0x66, 0},
	})})

	// The destination rectangle sits at the order's own origin, so the copy
	// starts at the source origin the order carries.
	o := order(&pdu.MultiScrBlt{
		Opcode:        ropSrcCopy,
		X:             2,
		Y:             0,
		Srcx:          0,
		Srcy:          0,
		NumRectangles: 1,
		Rectangles:    []pdu.DeltaRect{{Left: 2, Top: 0, Width: 2, Height: 1}},
	})
	s.DrawMulti(&o, image.Rect(0, 0, 8, 2))

	if got := pixelAt(t, s, 2, 0); !bytes.Equal(got, bgra(0x11, 0x22, 0x33)) {
		t.Errorf("(2,0) is %v, want the copied first pixel", got)
	}
	if got := pixelAt(t, s, 3, 0); !bytes.Equal(got, bgra(0x44, 0x55, 0x66)) {
		t.Errorf("(3,0) is %v, want the copied second pixel", got)
	}
}

// Each rectangle reads the source at the same translation the order applies as
// a whole, so a rectangle away from the order's origin does not read the source
// origin's first pixels. A single source origin per rectangle would copy one
// source row into every rectangle and smear the shape.
func TestDrawMultiScrBltMapsEachRectangleToTheSourceRegion(t *testing.T) {
	s := NewScreen(4, 2)
	// A source row for the blit to read, one row below the destination.
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 1, Cx: 1, Cy: 1, Colour: [4]uint8{0x11, 0x22, 0x33, 0},
	})})
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 1, Y: 1, Cx: 1, Cy: 1, Colour: [4]uint8{0x44, 0x55, 0x66, 0},
	})})

	// The order's destination is the origin and its source is one row down, so
	// each destination rectangle reads the source directly below it.
	o := order(&pdu.MultiScrBlt{
		Opcode:        ropSrcCopy,
		X:             0,
		Y:             0,
		Srcx:          0,
		Srcy:          1,
		NumRectangles: 2,
		Rectangles: []pdu.DeltaRect{
			{Left: 0, Top: 0, Width: 1, Height: 1},
			{Left: 1, Top: 0, Width: 1, Height: 1},
		},
	})
	s.DrawMulti(&o, image.Rect(0, 0, 4, 2))

	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, bgra(0x11, 0x22, 0x33)) {
		t.Errorf("(0,0) is %v, want the pixel below it", got)
	}
	if got := pixelAt(t, s, 1, 0); !bytes.Equal(got, bgra(0x44, 0x55, 0x66)) {
		t.Errorf("(1,0) is %v, want the pixel below it, not the first source pixel", got)
	}
}

// MEM3BLT is a MEMBLT against a third colour table that copies, so SRCCOPY
// draws from the cache the same way.
func TestDrawMem3bltDrawsFromCache(t *testing.T) {
	s := NewScreen(8, 4)
	src := solid(2, 2, 0x10, 0x20, 0x30)
	s.Draw([]pdu.OrderPdu{cacheOrder(1, 3, 2, 2, 32, src)})

	o := order(&pdu.Mem3blt{CacheId: 1, CacheIdx: 3, X: 2, Y: 1, Cx: 2, Cy: 2, Opcode: ropSrcCopy})
	dirty := s.DrawMulti(&o, image.Rect(0, 0, 8, 4))
	if want := image.Rect(2, 1, 4, 3); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	for _, p := range [][2]int{{2, 1}, {3, 1}, {2, 2}, {3, 2}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, bgra(0x10, 0x20, 0x30)) {
			t.Errorf("(%d,%d) is %v, want the cached colour", p[0], p[1], got)
		}
	}
	if got := pixelAt(t, s, 1, 1); bytes.Equal(got, bgra(0x10, 0x20, 0x30)) {
		t.Error("the blit spilled outside the requested rectangle")
	}
}

// MEM3BLT is parsed with no cache id until one is sent, and a blit from an empty
// slot must leave the screen alone.
func TestDrawMem3bltOfEmptySlotDrawsNothing(t *testing.T) {
	s := NewScreen(4, 4)
	before := s.Pixels()
	o := order(&pdu.Mem3blt{X: 0, Y: 0, Cx: 4, Cy: 4, Opcode: ropSrcCopy})
	if dirty := s.DrawMulti(&o, image.Rect(0, 0, 4, 4)); !dirty.Empty() {
		t.Errorf("dirty area is %v, want empty", dirty)
	}
	if !bytes.Equal(before, s.Pixels()) {
		t.Error("the screen changed")
	}
}

// MEM3BLT's bRop is a real raster operation, unlike MEMBLT's colour index. An
// operation that is not SRCCOPY must be counted rather than drawn as a copy.
func TestDrawMem3bltUnhandledRopIsCounted(t *testing.T) {
	s := NewScreen(8, 4)
	src := solid(2, 2, 0x10, 0x20, 0x30)
	s.Draw([]pdu.OrderPdu{cacheOrder(1, 3, 2, 2, 32, src)})
	before := s.Pixels()

	// PATCOPY against a cached bitmap is not a plain copy; drawing it as one
	// would paint the cached colour where the pattern belongs.
	o := order(&pdu.Mem3blt{CacheId: 1, CacheIdx: 3, X: 2, Y: 1, Cx: 2, Cy: 2, Opcode: ropPatCopy})
	if dirty := s.DrawMulti(&o, image.Rect(0, 0, 8, 4)); !dirty.Empty() {
		t.Errorf("dirty area is %v, want empty", dirty)
	}
	if !bytes.Equal(before, s.Pixels()) {
		t.Error("a MEM3BLT with a ROP other than SRCCOPY was drawn as a copy")
	}
	if s.Unsupported()["mem3blt rop 0xf0"] == 0 {
		t.Errorf("the unhandled ROP was not counted: %v", s.Unsupported())
	}
}

// The order's own bounds still clip the whole rectangle list.
func TestDrawMultiHonoursClip(t *testing.T) {
	s := NewScreen(8, 4)
	o := order(&pdu.MultiOpaqueRect{
		Colour:        [4]uint8{0x40, 0x50, 0x60, 0},
		NumRectangles: 1,
		Rectangles:    []pdu.DeltaRect{{Left: 0, Top: 0, Width: 8, Height: 4}},
	})
	clip := image.Rect(2, 1, 4, 3)
	dirty := s.DrawMulti(&o, clip)
	if dirty != clip {
		t.Errorf("dirty area is %v, want %v", dirty, clip)
	}
	want := bgra(0x40, 0x50, 0x60)
	for _, p := range [][2]int{{2, 1}, {3, 1}, {2, 2}, {3, 2}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, want) {
			t.Errorf("(%d,%d) inside the clip was not drawn: %v", p[0], p[1], got)
		}
	}
	for _, p := range [][2]int{{1, 1}, {4, 1}, {2, 0}, {2, 3}} {
		if got := pixelAt(t, s, p[0], p[1]); bytes.Equal(got, want) {
			t.Errorf("(%d,%d) is outside the clip but was drawn", p[0], p[1])
		}
	}
}

// An order may reuse the previous order's rectangles and only reduce the count,
// and then only the first count of them are meant.
func TestDrawMultiUsesTheCountNotTheWholeList(t *testing.T) {
	s := NewScreen(8, 2)
	o := order(&pdu.MultiOpaqueRect{
		Colour:        [4]uint8{0x40, 0x50, 0x60, 0},
		NumRectangles: 1,
		Rectangles: []pdu.DeltaRect{
			{Left: 0, Top: 0, Width: 1, Height: 1},
			{Left: 4, Top: 0, Width: 1, Height: 1},
		},
	})
	s.DrawMulti(&o, image.Rect(0, 0, 8, 2))
	want := bgra(0x40, 0x50, 0x60)
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, want) {
		t.Errorf("the first rectangle was not drawn: %v", got)
	}
	if got := pixelAt(t, s, 4, 0); bytes.Equal(got, want) {
		t.Error("a rectangle past the count was drawn")
	}
}

// DrawMulti must not claim an order it does not handle.
func TestDrawMultiIgnoresOtherOrders(t *testing.T) {
	s := NewScreen(4, 4)
	before := s.Pixels()
	o := order(&pdu.Memblt{CacheId: 1, CacheIdx: 0, X: 0, Y: 0, Cx: 4, Cy: 4})
	if dirty := s.DrawMulti(&o, image.Rect(0, 0, 4, 4)); !dirty.Empty() {
		t.Errorf("dirty area is %v, want empty", dirty)
	}
	if !bytes.Equal(before, s.Pixels()) {
		t.Error("a MEMBLT was drawn by DrawMulti")
	}
}
