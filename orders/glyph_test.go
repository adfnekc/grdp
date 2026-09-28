package orders

import (
	"bytes"
	"image"
	"testing"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// glyph2x2 is a two by two glyph whose set bits are the whole top row and the
// left of the bottom row. The bits are packed most significant first, so an
// upside down or mirrored draw moves the set bit and fails the tests.
func glyph2x2(index uint32) pdu.Glyph {
	return pdu.Glyph{CacheIndex: index, Width: 2, Height: 2, Bits: []byte{0xC0, 0x80}}
}

func TestGlyphCacheStoresGlyphs(t *testing.T) {
	c := NewGlyphCache()
	c.Put(&pdu.GlyphCacheOrder{CacheID: 1, Glyphs: []pdu.Glyph{glyph2x2(7)}})

	g := c.Glyph(1, 7)
	if g == nil {
		t.Fatal("glyph 7 is not in cache 1")
	}
	if g.Width != 2 || g.Height != 2 {
		t.Errorf("size is %dx%d, want 2x2", g.Width, g.Height)
	}
	if c.Glyph(0, 7) != nil {
		t.Error("the cache id is part of the key")
	}
	if c.Len() != 1 {
		t.Errorf("cache holds %d glyphs, want 1", c.Len())
	}

	c.Reset()
	if c.Len() != 0 || c.Glyph(1, 7) != nil {
		t.Error("reset left glyphs behind")
	}
}

func TestDrawText2OpaqueGlyph(t *testing.T) {
	s := NewScreen(8, 6)
	glyphs := NewGlyphCache()
	glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	order := &pdu.GlyphIndex{
		CacheID:   0,
		FlAccel:   pdu.SO_HORIZONTAL,
		ForeColor: [4]uint8{0xff, 0xff, 0xff, 0xff},
		BackColor: [4]uint8{0x00, 0x00, 0x00, 0xff},
		X:         1,
		Y:         1,
		// Glyph index 1, then a zero advance: the pen does not move yet.
		Data: []byte{0x01, 0x00},
	}

	dirty := s.DrawText2(order, glyphs, image.Rect(0, 0, 8, 6))
	if want := image.Rect(1, 1, 3, 3); dirty != want {
		t.Errorf("dirty is %v, want %v", dirty, want)
	}

	if got := pixelAt(t, s, 1, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("top left is %v, want white", got)
	}
	if got := pixelAt(t, s, 2, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("top right is %v, want white", got)
	}
	if got := pixelAt(t, s, 1, 2); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("bottom left is %v, want white", got)
	}
	if got := pixelAt(t, s, 2, 2); !bytes.Equal(got, bgra(0, 0, 0)) {
		t.Errorf("bottom right is %v, want the black background", got)
	}
}

// Transparent text leaves the bits that are clear where they were, which is
// what fOpRedundant means.
func TestDrawText2TransparentLeavesTheBackground(t *testing.T) {
	s := NewScreen(8, 6)
	// Paint the whole screen blue with an order, then draw transparent text on
	// top: the clear bit must still be blue.
	if _, err := s.Draw([]pdu.OrderPdu{{
		Primary: &pdu.Primary{Data: &pdu.OpaqueRect{Cx: 8, Cy: 6, Colour: [4]uint8{0x00, 0x00, 0xff}}},
	}}); err != nil {
		t.Fatalf("paint: %v", err)
	}

	glyphs := NewGlyphCache()
	glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	order := &pdu.GlyphIndex{
		CacheID:      0,
		FlAccel:      pdu.SO_HORIZONTAL,
		FOpRedundant: 1,
		ForeColor:    [4]uint8{0xff, 0xff, 0xff, 0xff},
		BackColor:    [4]uint8{0x00, 0x00, 0x00, 0xff},
		X:            1,
		Y:            1,
		Data:         []byte{0x01, 0x00},
	}
	s.DrawText2(order, glyphs, image.Rect(0, 0, 8, 6))

	if got := pixelAt(t, s, 1, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("set bit is %v, want white", got)
	}
	if got := pixelAt(t, s, 2, 2); !bytes.Equal(got, bgra(0x00, 0x00, 0xff)) {
		t.Errorf("clear bit is %v, want the blue underneath", got)
	}
}

// The opaque rectangle the order carries is painted in the background colour
// before the glyphs, which is what makes the text readable over the window.
func TestDrawText2PaintsTheOpaqueRectangle(t *testing.T) {
	s := NewScreen(8, 6)
	glyphs := NewGlyphCache()

	order := &pdu.GlyphIndex{
		FOpRedundant: 0,
		BackColor:    [4]uint8{0x00, 0x00, 0xff, 0xff},
		OpLeft:       0, OpTop: 0, OpRight: 5, OpBottom: 3,
		X: 1, Y: 1,
	}
	s.DrawText2(order, glyphs, image.Rect(0, 0, 8, 6))

	if got := pixelAt(t, s, 3, 3); !bytes.Equal(got, bgra(0x00, 0x00, 0xff)) {
		t.Errorf("inside the rectangle is %v, want blue", got)
	}
	if got := pixelAt(t, s, 0, 4); !bytes.Equal(got, bgra(0, 0, 0)) {
		t.Errorf("outside the rectangle is %v, want black", got)
	}
}

// A run that adds a fragment and then uses it draws the same glyphs twice, with
// the fragment bytes standing in for the glyph indices the second time.
func TestDrawText2StoresAndReplaysAFragment(t *testing.T) {
	s := NewScreen(16, 6)
	glyphs := NewGlyphCache()
	glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	base := func(x int32) *pdu.GlyphIndex {
		return &pdu.GlyphIndex{
			CacheID:   0,
			FlAccel:   pdu.SO_HORIZONTAL,
			ForeColor: [4]uint8{0xff, 0xff, 0xff, 0xff},
			BackColor: [4]uint8{0x00, 0x00, 0x00, 0xff},
			X:         x,
			Y:         1,
		}
	}

	// Glyph index 1 with a zero advance, then ADD: cache the two bytes that
	// precede the ADD, which are exactly that run, as fragment 0.
	add := base(1)
	add.Data = []byte{0x01, 0x00, pdu.GLYPH_FRAGMENT_ADD, 0x00, 0x02}
	s.DrawText2(add, glyphs, image.Rect(0, 0, 16, 6))

	if got := pixelAt(t, s, 1, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("the ADD run did not draw at (1,1): %v", got)
	}
	fragment := glyphs.Fragment(0)
	if want := []byte{0x01, 0x00}; !bytes.Equal(fragment, want) {
		t.Fatalf("stored fragment is % x, want % x", fragment, want)
	}

	// Use fragment 0 at a different pen position: the same glyph appears there.
	use := base(5)
	use.Data = []byte{pdu.GLYPH_FRAGMENT_USE, 0x00, 0x00}
	s.DrawText2(use, glyphs, image.Rect(0, 0, 16, 6))

	if got := pixelAt(t, s, 5, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("the replayed fragment did not draw at (5,1): %v", got)
	}
	if got := pixelAt(t, s, 6, 2); !bytes.Equal(got, bgra(0, 0, 0)) {
		t.Errorf("the replayed glyph's clear bit is %v, want black", got)
	}
}

// An advance larger than 127 is written as 0x80 and two little endian bytes,
// and must move the pen by the whole value.
func TestDrawText2ReadsTheLongAdvance(t *testing.T) {
	s := NewScreen(16, 6)
	glyphs := NewGlyphCache()
	glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	order := &pdu.GlyphIndex{
		CacheID:   0,
		FlAccel:   pdu.SO_HORIZONTAL,
		ForeColor: [4]uint8{0xff, 0xff, 0xff, 0xff},
		BackColor: [4]uint8{0x00, 0x00, 0x00, 0xff},
		X:         1,
		Y:         1,
		// Glyph index 1, then an advance of ten written as 0x80 and two bytes.
		Data: []byte{0x01, 0x80, 0x0A, 0x00},
	}
	s.DrawText2(order, glyphs, image.Rect(0, 0, 16, 6))

	if got := pixelAt(t, s, 11, 1); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("the glyph after a ten pixel advance is %v at (11,1)", got)
	}
}

// A USE of a fragment the client never stored draws nothing rather than reading
// the fragment as nothing and losing the rest of the run.
func TestDrawText2MissingFragmentDrawsNothing(t *testing.T) {
	s := NewScreen(8, 6)
	glyphs := NewGlyphCache()

	order := &pdu.GlyphIndex{
		CacheID:   0,
		FlAccel:   pdu.SO_HORIZONTAL,
		ForeColor: [4]uint8{0xff, 0xff, 0xff, 0xff},
		Data:      []byte{pdu.GLYPH_FRAGMENT_USE, 0x09, 0x00},
	}
	if dirty := s.DrawText2(order, glyphs, image.Rect(0, 0, 8, 6)); !dirty.Empty() {
		t.Errorf("dirty is %v, want nothing drawn", dirty)
	}
	if s.unsupported["text2 fragment not in the cache"] == 0 {
		t.Error("the missing fragment was not counted")
	}
}

// A nil cache or order must not panic, since the dispatch can reach a TEXT2
// order before the cache has necessarily been created.
func TestDrawText2WithNoCache(t *testing.T) {
	s := NewScreen(4, 4)
	if dirty := s.DrawText2(&pdu.GlyphIndex{X: 1, Y: 1}, nil, image.Rect(0, 0, 4, 4)); !dirty.Empty() {
		t.Errorf("dirty is %v, want nothing", dirty)
	}
	if dirty := s.DrawText2(nil, NewGlyphCache(), image.Rect(0, 0, 4, 4)); !dirty.Empty() {
		t.Errorf("dirty is %v, want nothing", dirty)
	}
}

// A glyph that runs off the edge of the screen is clipped, and a caller that
// passes a clip larger than the screen does not make the write run past the
// framebuffer.
func TestDrawText2ClampsToTheScreen(t *testing.T) {
	s := NewScreen(4, 4)
	glyphs := NewGlyphCache()
	glyphs.Put(&pdu.GlyphCacheOrder{CacheID: 0, Glyphs: []pdu.Glyph{glyph2x2(1)}})

	order := &pdu.GlyphIndex{
		CacheID:   0,
		FlAccel:   pdu.SO_HORIZONTAL,
		ForeColor: [4]uint8{0xff, 0xff, 0xff, 0xff},
		X:         3,
		Y:         3,
		Data:      []byte{0x01, 0x00},
	}
	dirty := s.DrawText2(order, glyphs, image.Rect(-10, -10, 100, 100))
	if want := image.Rect(3, 3, 4, 4); dirty != want {
		t.Errorf("dirty is %v, want %v", dirty, want)
	}
	if got := pixelAt(t, s, 3, 3); !bytes.Equal(got, bgra(0xff, 0xff, 0xff)) {
		t.Errorf("(3,3) is %v, want white", got)
	}
}
