package orders

import (
	"bytes"
	"image"
	"strings"
	"testing"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// bgra builds a BGRA pixel with a distinct colour per coordinate so that a
// misplaced blit is visible rather than plausible.
func bgra(r, g, b uint8) []byte { return []byte{b, g, r, 0xff} }

func solid(width, height int, r, g, b uint8) []byte {
	out := make([]byte, width*height*4)
	for i := 0; i < len(out); i += 4 {
		copy(out[i:i+4], bgra(r, g, b))
	}
	return out
}

// cacheOrder is a secondary order carrying a decoded bitmap, which is what the
// server sends to fill a cache slot.
func cacheOrder(cacheID, index uint32, width, height, bpp int, pixels []byte) pdu.OrderPdu {
	return pdu.OrderPdu{
		Secondary: &pdu.Secondary{
			CacheBitmap: &pdu.CacheBitmap{
				CacheID:    cacheID,
				CacheIndex: index,
				Width:      width,
				Height:     height,
				Bpp:        bpp,
				Pixels:     pixels,
			},
		},
	}
}

func order(data pdu.PrimaryOrder) pdu.OrderPdu {
	return pdu.OrderPdu{Primary: &pdu.Primary{Data: data}}
}

// orderWithin is an order that may only draw inside the given rectangle.
func orderWithin(data pdu.PrimaryOrder, left, top, right, bottom int32) pdu.OrderPdu {
	return pdu.OrderPdu{
		ControlFlags: pdu.TS_BOUNDS,
		Primary: &pdu.Primary{
			Bounds: pdu.Bounds{Left: left, Top: top, Right: right, Bottom: bottom},
			Data:   data,
		},
	}
}

func pixelAt(t *testing.T, s *Screen, x, y int) []byte {
	t.Helper()
	w, h := s.Size()
	if x < 0 || y < 0 || x >= w || y >= h {
		t.Fatalf("(%d,%d) is outside the %dx%d screen", x, y, w, h)
	}
	px := s.Pixels()
	o := (y*w + x) * 4
	return px[o : o+4]
}

func TestCacheStoresAndReturnsEntries(t *testing.T) {
	c := NewCache()
	// Two entries in one cache, and one in another, to show the index is not
	// shared between caches.
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 7, Width: 1, Height: 1, Bpp: 32,
		Pixels: []byte{1, 2, 3, 0}})
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 8, Width: 1, Height: 1, Bpp: 32,
		Pixels: []byte{4, 5, 6, 0}})
	c.Put(&pdu.CacheBitmap{CacheID: 2, CacheIndex: 7, Width: 1, Height: 1, Bpp: 32,
		Pixels: []byte{9, 9, 9, 0}})

	if c.Len() != 3 {
		t.Fatalf("cache holds %d entries, want 3", c.Len())
	}
	if e := c.Get(1, 7); e == nil || e.Pixels[0] != 1 {
		t.Errorf("cache 1 slot 7 is wrong: %+v", e)
	}
	if e := c.Get(2, 7); e == nil || e.Pixels[0] != 9 {
		t.Errorf("cache 2 slot 7 should be its own entry: %+v", e)
	}
	if e := c.Get(1, 99); e != nil {
		t.Errorf("slot 99 should be empty, got %+v", e)
	}

	c.Reset()
	if c.Len() != 0 {
		t.Errorf("reset left %d entries", c.Len())
	}
}

// A cache entry in a format we cannot convert must be dropped rather than
// drawn as something wrong.
func TestCacheDropsUnsupportedFormats(t *testing.T) {
	c := NewCache()
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 1, Width: 2, Height: 2, Bpp: 8,
		Pixels: []byte{1, 2, 3, 4}})
	if c.Len() != 0 {
		t.Errorf("8bpp entry was kept, %d entries", c.Len())
	}

	// A revision 3 entry is encoded rather than decoded, so there is nothing to
	// blit yet.
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 2, Width: 4, Height: 4, Bpp: 32,
		CodecID: 3, Data: []byte{1, 2, 3}})
	if c.Len() != 0 {
		t.Errorf("revision 3 entry was kept, %d entries", c.Len())
	}
}

// The order that draws practically everything: fill a cache slot, then blit it.
func TestMembltDrawsFromCache(t *testing.T) {
	s := NewScreen(8, 4)
	src := solid(2, 2, 0x10, 0x20, 0x30)

	if _, err := s.Draw([]pdu.OrderPdu{cacheOrder(1, 3, 2, 2, 32, src)}); err != nil {
		t.Fatal(err)
	}
	// Nothing reached the screen yet: filling the cache is not drawing.
	if got := pixelAt(t, s, 0, 0); bytes.Equal(got, bgra(0x10, 0x20, 0x30)) {
		t.Fatal("filling the cache should not draw")
	}

	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.Memblt{
		CacheId:  1,
		CacheIdx: 3,
		X:        2,
		Y:        1,
		Cx:       2,
		Cy:       2,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if want := image.Rect(2, 1, 4, 3); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}

	for _, p := range [][2]int{{2, 1}, {3, 1}, {2, 2}, {3, 2}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, bgra(0x10, 0x20, 0x30)) {
			t.Errorf("pixel (%d,%d) is %v, want the cached colour", p[0], p[1], got)
		}
	}
	// And nothing around it.
	if got := pixelAt(t, s, 1, 1); bytes.Equal(got, bgra(0x10, 0x20, 0x30)) {
		t.Error("the blit spilled to the left")
	}
	if got := pixelAt(t, s, 4, 1); bytes.Equal(got, bgra(0x10, 0x20, 0x30)) {
		t.Error("the blit spilled to the right")
	}
}

// The bounds an order carries are a clip: whatever the order says, a client
// must not draw outside them.
func TestMembltHonoursBounds(t *testing.T) {
	s := NewScreen(8, 4)
	src := solid(4, 4, 0x10, 0x20, 0x30)
	s.Draw([]pdu.OrderPdu{cacheOrder(1, 0, 4, 4, 32, src)})

	// Ask to blit 4x4 at the origin, but only allow the middle two columns of
	// the first two rows to be touched.
	_, err := s.Draw([]pdu.OrderPdu{orderWithin(&pdu.Memblt{
		CacheId: 1, CacheIdx: 0, X: 0, Y: 0, Cx: 4, Cy: 4,
	}, 1, 0, 3, 2)})
	if err != nil {
		t.Fatal(err)
	}

	colour := bgra(0x10, 0x20, 0x30)
	for _, p := range [][2]int{{1, 0}, {2, 0}, {1, 1}, {2, 1}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, colour) {
			t.Errorf("(%d,%d) inside the bounds was not drawn: %v", p[0], p[1], got)
		}
	}
	for _, p := range [][2]int{{0, 0}, {3, 0}, {0, 1}, {3, 1}, {1, 2}, {2, 3}} {
		if got := pixelAt(t, s, p[0], p[1]); bytes.Equal(got, colour) {
			t.Errorf("(%d,%d) is outside the bounds but was drawn", p[0], p[1])
		}
	}
}

// Zero sized or inside out bounds mean draw nothing, not draw everything.
func TestDegenerateBoundsDrawNothing(t *testing.T) {
	s := NewScreen(4, 4)
	src := solid(4, 4, 0x10, 0x20, 0x30)
	s.Draw([]pdu.OrderPdu{cacheOrder(1, 0, 4, 4, 32, src)})

	for _, b := range [][4]int32{{2, 2, 2, 4}, {2, 2, 4, 2}, {3, 3, 1, 1}} {
		before := s.Pixels()
		s.Draw([]pdu.OrderPdu{orderWithin(&pdu.Memblt{
			CacheId: 1, CacheIdx: 0, X: 0, Y: 0, Cx: 4, Cy: 4,
		}, b[0], b[1], b[2], b[3])})
		if !bytes.Equal(before, s.Pixels()) {
			t.Errorf("bounds %v drew something", b)
		}
	}
}

// A blit of a slot that was never filled must leave the screen alone rather
// than draw whatever happens to be in memory.
func TestMembltOfEmptySlotDrawsNothing(t *testing.T) {
	s := NewScreen(4, 4)
	before := s.Pixels()
	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.Memblt{
		CacheId: 1, CacheIdx: 42, X: 0, Y: 0, Cx: 4, Cy: 4,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Empty() {
		t.Errorf("dirty area is %v, want empty", dirty)
	}
	if !bytes.Equal(before, s.Pixels()) {
		t.Error("the screen changed")
	}
	if s.Unsupported()["memblt of an empty cache slot"] == 0 {
		t.Error("the skipped blit was not counted")
	}
}

// A blit may take a piece of the cached bitmap from an offset, which is how one
// cached cell serves several parts of the screen.
func TestMembltFromASourceOffset(t *testing.T) {
	s := NewScreen(4, 2)
	var src []byte
	src = append(src, bgra(1, 1, 1)...)
	src = append(src, bgra(2, 2, 2)...)
	src = append(src, bgra(3, 3, 3)...)
	src = append(src, bgra(4, 4, 4)...)
	s.Draw([]pdu.OrderPdu{cacheOrder(1, 0, 2, 2, 32, src)})

	// Copy the bottom right pixel to the top left of the screen.
	s.Draw([]pdu.OrderPdu{order(&pdu.Memblt{
		CacheId: 1, CacheIdx: 0, Srcx: 1, Srcy: 1, X: 0, Y: 0, Cx: 1, Cy: 1,
	})})
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, bgra(4, 4, 4)) {
		t.Errorf("got %v, want the bottom right pixel", got)
	}
}

func TestScrbltCopiesWithinTheScreen(t *testing.T) {
	s := NewScreen(4, 1)
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 0, Y: 0, Cx: 1, Cy: 1, Colour: [4]uint8{0x11, 0x22, 0x33, 0},
	})})
	s.Draw([]pdu.OrderPdu{order(&pdu.Scrblt{
		Srcx: 0, Srcy: 0, X: 3, Y: 0, Cx: 1, Cy: 1, Opcode: ropSrcCopy,
	})})
	if got := pixelAt(t, s, 3, 0); !bytes.Equal(got, bgra(0x11, 0x22, 0x33)) {
		t.Errorf("got %v, want the copied pixel", got)
	}
}

// A blit whose source runs off the buffer must report only the pixels it
// actually copied. This is reachable from the wire: SCRBLT copies from the
// screen itself at an origin the server picks, and nothing bounds that origin to
// the screen.
func TestScrbltSourceRunningOutReportsOnlyWrittenPixels(t *testing.T) {
	s := NewScreen(4, 2)
	// Two distinguishable pixels on the last row, for the blit to read.
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 2, Y: 1, Cx: 1, Cy: 1, Colour: [4]uint8{0x11, 0x22, 0x33, 0},
	})})
	s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: 3, Y: 1, Cx: 1, Cy: 1, Colour: [4]uint8{0x44, 0x55, 0x66, 0},
	})})

	// The screen holds eight pixels and the source starts at pixel six, so a
	// four wide copy can read only the first two of them.
	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.Scrblt{
		Srcx: 2, Srcy: 1, X: 0, Y: 0, Cx: 4, Cy: 1, Opcode: ropSrcCopy,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if want := image.Rect(0, 0, 2, 1); dirty != want {
		t.Errorf("dirty area is %v, want %v: the source ran out after two pixels", dirty, want)
	}
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, bgra(0x11, 0x22, 0x33)) {
		t.Errorf("(0,0) is %v, want the first copied pixel", got)
	}
	if got := pixelAt(t, s, 1, 0); !bytes.Equal(got, bgra(0x44, 0x55, 0x66)) {
		t.Errorf("(1,0) is %v, want the second copied pixel", got)
	}
	// The pixels past the end of the source were never written and must not be
	// claimed as dirty.
	for _, x := range []int{2, 3} {
		got := pixelAt(t, s, x, 0)
		if bytes.Equal(got, bgra(0x11, 0x22, 0x33)) || bytes.Equal(got, bgra(0x44, 0x55, 0x66)) {
			t.Errorf("(%d,0) is past the source but was drawn: %v", x, got)
		}
	}
}

// A source origin entirely off the screen copies nothing, so it must not claim
// any area as changed.
func TestScrbltFromPastTheScreenChangesNothing(t *testing.T) {
	s := NewScreen(4, 2)
	before := s.Pixels()
	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.Scrblt{
		Srcx: 0, Srcy: 7, X: 0, Y: 0, Cx: 4, Cy: 1, Opcode: ropSrcCopy,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Empty() {
		t.Errorf("dirty area is %v, want empty", dirty)
	}
	if !bytes.Equal(before, s.Pixels()) {
		t.Error("the screen changed")
	}
}

func TestOpaqueRectIsClippedToTheScreen(t *testing.T) {
	s := NewScreen(4, 4)
	// Ask for a rectangle that runs off every edge.
	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.OpaqueRect{
		X: -2, Y: -2, Cx: 100, Cy: 100, Colour: [4]uint8{0x40, 0x50, 0x60, 0},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if want := image.Rect(0, 0, 4, 4); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	if got := pixelAt(t, s, 3, 3); !bytes.Equal(got, bgra(0x40, 0x50, 0x60)) {
		t.Errorf("got %v, want the fill colour", got)
	}
}

// An order we recognise but cannot draw must be counted, so that a caller can
// tell a half drawn screen from a complete one.
func TestUnsupportedOrdersAreCounted(t *testing.T) {
	s := NewScreen(4, 4)
	// An order type that parses and has no renderer. The nine grid orders do
	// that: they are recognised so that the batch survives, and counted rather
	// than drawn, so a server sending one shows up here instead of silently
	// leaving part of the screen stale.
	s.Draw([]pdu.OrderPdu{order(&pdu.DrawNineGrid{})})
	found := false
	for kind := range s.Unsupported() {
		if strings.Contains(kind, "DrawNineGrid") {
			found = true
		}
	}
	if !found {
		t.Errorf("the unrendered order should be counted by type, got %v", s.Unsupported())
	}
}

// A reset throws the cached bitmaps away: after a reset graphics the server
// starts from scratch and nothing from before may be blitted.
func TestResizeClearsTheScreenAndCacheIsSeparate(t *testing.T) {
	s := NewScreen(4, 4)
	s.Draw([]pdu.OrderPdu{cacheOrder(1, 0, 2, 2, 32, solid(2, 2, 9, 9, 9))})
	if s.Cache.Len() != 1 {
		t.Fatal("the cache should hold the entry")
	}

	s.Resize(8, 8)
	if w, h := s.Size(); w != 8 || h != 8 {
		t.Fatalf("size is %dx%d, want 8x8", w, h)
	}
	for _, px := range [][]byte{pixelAt(t, s, 0, 0), pixelAt(t, s, 7, 7)} {
		if !bytes.Equal(px, []byte{0, 0, 0, 0xff}) {
			t.Errorf("a resized screen should be opaque black, got %v", px)
		}
	}
}

// PATBLT is how a server draws a fill. Almost all of them are a solid brush with
// PATCOPY, which is a rectangle in a colour; a patterned brush is an eight by
// eight monochrome tile.
func TestPatbltSolidFill(t *testing.T) {
	s := NewScreen(6, 4)
	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.Patblt{
		X: 1, Y: 1, Cx: 3, Cy: 2, Opcode: ropPatCopy,
		FgColour: [4]uint8{0x11, 0x22, 0x33, 0},
		BgColour: [4]uint8{0x00, 0x00, 0x00, 0},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if want := image.Rect(1, 1, 4, 3); dirty != want {
		t.Errorf("dirty area is %v, want %v", dirty, want)
	}
	// TS_COLOR is red, green, blue, so the pixel is the reverse.
	want := bgra(0x11, 0x22, 0x33)
	if got := pixelAt(t, s, 2, 2); !bytes.Equal(got, want) {
		t.Errorf("filled pixel is %v, want %v", got, want)
	}
	if got := pixelAt(t, s, 0, 0); bytes.Equal(got, want) {
		t.Error("the fill spilled outside its rectangle")
	}
}

func TestPatbltMonochromePattern(t *testing.T) {
	s := NewScreen(8, 8)
	// A checkerboard: alternating bits.
	pattern := []byte{0xAA, 0x55, 0xAA, 0x55, 0xAA, 0x55, 0xAA, 0x55}
	s.Draw([]pdu.OrderPdu{order(&pdu.Patblt{
		X: 0, Y: 0, Cx: 8, Cy: 8, Opcode: ropPatCopy,
		FgColour: [4]uint8{0xff, 0xff, 0xff, 0},
		BgColour: [4]uint8{0x00, 0x00, 0x00, 0},
		Brush:    pdu.Brush{Style: 3, Data: pattern},
	})})

	// The leftmost pixel is the most significant bit, which is set here.
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("(0,0) is %v, want the foreground colour", got)
	}
	if got := pixelAt(t, s, 1, 0); !bytes.Equal(got, bgra(0, 0, 0)) {
		t.Errorf("(1,0) is %v, want the background colour", got)
	}
	// The next row is the opposite phase.
	if got := pixelAt(t, s, 0, 1); !bytes.Equal(got, bgra(0, 0, 0)) {
		t.Errorf("(0,1) is %v, want the background colour", got)
	}
}

func TestPatbltUnhandledRopIsCounted(t *testing.T) {
	s := NewScreen(4, 4)
	s.Draw([]pdu.OrderPdu{order(&pdu.Patblt{
		X: 0, Y: 0, Cx: 2, Cy: 2, Opcode: 0x99,
	})})
	if len(s.Unsupported()) == 0 {
		t.Errorf("an unhandled ROP should be counted: %v", s.Unsupported())
	}
}

// The shape orders are drawn from points that the server sends as deltas, and
// the polygon fill is a scanline sweep.
func TestPolygonScFillsItsShape(t *testing.T) {
	s := NewScreen(8, 8)
	// A right triangle: (1,1) (6,1) (1,6), closed.
	pts := []pdu.Point{{X: 1, Y: 1}, {X: 6, Y: 1}, {X: 1, Y: 6}, {X: 1, Y: 1}}
	dirty, err := s.Draw([]pdu.OrderPdu{order(&pdu.PolygonSc{
		Points: pts, Fgcolour: [4]uint8{0xff, 0x00, 0x00, 0},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Empty() {
		t.Fatal("nothing was drawn")
	}
	inside := bgra(0xff, 0x00, 0x00)
	// A point well inside the triangle.
	if got := pixelAt(t, s, 2, 2); !bytes.Equal(got, inside) {
		t.Errorf("(2,2) is %v, want the fill colour", got)
	}
	// A point outside it, diagonally across.
	if got := pixelAt(t, s, 6, 6); bytes.Equal(got, inside) {
		t.Error("(6,6) is outside the triangle but was filled")
	}
}

func TestPolylineDrawsSegments(t *testing.T) {
	s := NewScreen(8, 8)
	pts := []pdu.Point{{X: 0, Y: 0}, {X: 7, Y: 0}, {X: 7, Y: 7}}
	s.Draw([]pdu.OrderPdu{order(&pdu.Polyline{
		Points: pts, Colour: [4]uint8{0x00, 0xff, 0x00, 0},
	})})
	ink := bgra(0x00, 0xff, 0x00)
	for _, p := range [][2]int{{0, 0}, {4, 0}, {7, 0}, {7, 4}, {7, 7}} {
		if got := pixelAt(t, s, p[0], p[1]); !bytes.Equal(got, ink) {
			t.Errorf("(%d,%d) is %v, want the line colour", p[0], p[1], got)
		}
	}
	// Nothing off the path.
	if got := pixelAt(t, s, 3, 3); bytes.Equal(got, ink) {
		t.Error("(3,3) is not on the path but was drawn")
	}
}

func TestEllipseOutlineAndFill(t *testing.T) {
	ink := bgra(0xff, 0xff, 0x00)

	// Filled: the centre is drawn.
	filled := NewScreen(20, 12)
	filled.Draw([]pdu.OrderPdu{order(&pdu.EllipeCb{
		Left: 0, Top: 0, Right: 20, Bottom: 12,
		FgColour: [4]uint8{0xff, 0xff, 0x00, 0},
	})})
	if got := pixelAt(t, filled, 10, 6); !bytes.Equal(got, ink) {
		t.Errorf("the centre of a filled ellipse is %v", got)
	}
	// And so are its ends.
	if got := pixelAt(t, filled, 0, 6); !bytes.Equal(got, ink) {
		t.Errorf("the left edge of a filled ellipse is %v", got)
	}

	// Outlined: the centre is not, but the edge is, and the corner never is.
	hollow := NewScreen(20, 12)
	hollow.Draw([]pdu.OrderPdu{order(&pdu.EllipeSc{
		Left: 0, Top: 0, Right: 20, Bottom: 12,
		Colour: [4]uint8{0xff, 0xff, 0x00, 0},
	})})
	if got := pixelAt(t, hollow, 10, 6); bytes.Equal(got, ink) {
		t.Error("the centre of an outlined ellipse should be untouched")
	}
	if got := pixelAt(t, hollow, 0, 6); !bytes.Equal(got, ink) {
		t.Errorf("the left edge of an outlined ellipse is %v", got)
	}
	if got := pixelAt(t, hollow, 0, 0); bytes.Equal(got, ink) {
		t.Error("the corner is outside the ellipse but was drawn")
	}
}
