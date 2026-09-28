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
	// An order type with no renderer at all.
	s.Draw([]pdu.OrderPdu{order(&pdu.Polyline{})})
	if len(s.Unsupported()) == 0 {
		t.Errorf("expected the unrendered order type to be counted: %v", s.Unsupported())
	}
	found := false
	for kind := range s.Unsupported() {
		if strings.Contains(kind, "Polyline") {
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
