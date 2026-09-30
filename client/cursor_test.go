package client

import (
	"testing"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// colourShape builds a 24bpp x 1 shape whose single row is the given BGR pixels,
// with an AND mask of all zeroes so that every pixel is drawn.
//
// The row is padded the way the wire pads it. Writing three bytes per pixel and
// stopping would make a shape whose row is not a multiple of two bytes long
// shorter than the decoder expects, which reads as a broken cursor rather than a
// broken test.
func colourShape(pixels ...[3]byte) *pdu.PointerShape {
	xor := make([]byte, 0, len(pixels)*3)
	for _, p := range pixels {
		xor = append(xor, p[0], p[1], p[2])
	}
	if len(xor)%2 != 0 {
		xor = append(xor, 0)
	}
	and := make([]byte, ((len(pixels)+15)/16)*2)
	return &pdu.PointerShape{
		Width: len(pixels), Height: 1, XorBpp: 3,
		HotspotX: 0, HotspotY: 0, Xor: xor, And: and,
	}
}

// colour32Shape is the same shape at 32bpp, where the pixels carry their own
// alpha. The two depths have different rules, so the tests have to say which
// they mean.
func colour32Shape(pixels ...[4]byte) *pdu.PointerShape {
	xor := make([]byte, 0, len(pixels)*4)
	for _, p := range pixels {
		xor = append(xor, p[0], p[1], p[2], p[3])
	}
	and := make([]byte, ((len(pixels)+15)/16)*2)
	return &pdu.PointerShape{
		Width: len(pixels), Height: 1, XorBpp: 4,
		HotspotX: 0, HotspotY: 0, Xor: xor, And: and,
	}
}

func rgbaAt(c *Cursor, x, y int) [4]byte {
	o := y*c.Image.Stride + x*4
	return [4]byte{c.Image.Pix[o], c.Image.Pix[o+1], c.Image.Pix[o+2], c.Image.Pix[o+3]}
}

// An AND bit of zero means the colour is drawn as it is. The bytes are BGR, so
// the red channel is the third byte of each pixel.
func TestCursorAndMaskClearIsOpaque(t *testing.T) {
	s := colourShape([3]byte{10, 20, 30}, [3]byte{40, 50, 60})
	cur := cursorFromShape(s)
	if cur == nil {
		t.Fatal("no cursor")
	}
	if got := rgbaAt(cur, 0, 0); got != [4]byte{30, 20, 10, 255} {
		t.Errorf("pixel 0 = %v, want [30 20 10 255]", got)
	}
	if got := rgbaAt(cur, 1, 0); got != [4]byte{60, 50, 40, 255} {
		t.Errorf("pixel 1 = %v, want [60 50 40 255]", got)
	}
}

// The AND mask on a colour pointer is not a transparency mask, which is what
// this test asserted before a real Windows target said otherwise. An AND bit of
// one means the colour decides: black becomes transparent, white becomes the
// inverse of the background, and anything else is drawn with its own alpha.
//
// The Windows 10 cursor is why this matters. Its AND mask is mostly set, because
// the shape lives in the alpha channel, so treating a set bit as transparent
// erased all but one pixel of the arrow.
func TestCursorColourAndMaskDependsOnTheColour(t *testing.T) {
	// A dark colour that is not black keeps its pixel, alpha and all.
	s := colour32Shape([4]byte{10, 20, 30, 200}, [4]byte{40, 50, 60, 128})
	s.And[0] = 0x80 // the first pixel only
	cur := cursorFromShape(s)
	if got := rgbaAt(cur, 0, 0); got != [4]byte{30, 20, 10, 200} {
		t.Errorf("and=1 with a dark colour = %v, want it kept with its alpha", got)
	}
	if got := rgbaAt(cur, 1, 0); got != [4]byte{60, 50, 40, 128} {
		t.Errorf("and=0 = %v, want it kept with its alpha", got)
	}
}

// Black with the AND bit set is the transparency: it is how a shape says "leave
// the screen alone here".
func TestCursorColourAndMaskClearsBlack(t *testing.T) {
	s := colour32Shape([4]byte{0, 0, 0, 255}, [4]byte{1, 2, 3, 255})
	s.And[0] = 0x80
	cur := cursorFromShape(s)
	if got := rgbaAt(cur, 0, 0); got != [4]byte{0, 0, 0, 0} {
		t.Errorf("and=1 black = %v, want transparent", got)
	}
	if got := rgbaAt(cur, 1, 0); got != [4]byte{3, 2, 1, 255} {
		t.Errorf("and=0 = %v, want it kept", got)
	}
}

// A 24bpp pointer has no alpha of its own, so white with the AND bit set means
// the inverse of the background, and a colour that is neither white nor black is
// a hole.
func TestCursor24bppAndMaskIsTheOldRule(t *testing.T) {
	s := colourShape([3]byte{0xff, 0xff, 0xff}, [3]byte{10, 20, 30})
	s.And[0] = 0xC0 // both pixels
	cur := cursorFromShape(s)
	if got := rgbaAt(cur, 0, 0); got[3] != 0xff {
		t.Errorf("and=1 white at 24bpp = %v, want the inverted substitute drawn", got)
	}
	if got := rgbaAt(cur, 1, 0); got != [4]byte{0, 0, 0, 0} {
		t.Errorf("and=1 with a colour that is neither white nor black at 24bpp = %v, want transparent", got)
	}
	// With the bit clear there is no alpha to use, so the pixel is made opaque.
	s2 := colourShape([3]byte{10, 20, 30})
	if got := rgbaAt(cursorFromShape(s2), 0, 0); got != [4]byte{30, 20, 10, 255} {
		t.Errorf("and=0 at 24bpp = %v, want opaque", got)
	}
}

// The XOR mask is bottom up, so the first row on the wire is the last row of the
// image. Getting this wrong draws every cursor upside down, which is the kind of
// thing that looks like a rendering bug somewhere else entirely.
func TestCursorXorMaskIsBottomUp(t *testing.T) {
	// Two rows, each a single BGR pixel. The wire's first row is the image's
	// last row.
	// Each row is three bytes of colour padded to four.
	xor := []byte{
		1, 2, 3, 0, // bottom row on the wire, so the image's last row
		4, 5, 6, 0, // the image's first row
	}
	// Each row is two bytes of AND mask for a one pixel wide shape.
	and := []byte{0, 0, 0, 0}
	s := &pdu.PointerShape{Width: 1, Height: 2, XorBpp: 3, Xor: xor, And: and}
	cur := cursorFromShape(s)
	if cur == nil || cur.Height != 2 {
		t.Fatalf("cursor = %+v", cur)
	}
	if got := rgbaAt(cur, 0, 0); got != [4]byte{6, 5, 4, 255} {
		t.Errorf("top row = %v, want [6 5 4 255] (the second wire row)", got)
	}
	if got := rgbaAt(cur, 0, 1); got != [4]byte{3, 2, 1, 255} {
		t.Errorf("bottom row = %v, want [3 2 1 255] (the first wire row)", got)
	}
}

// A 32bpp shape carries its own alpha and that is what decides the edges, which
// is how Windows sends a pointer with soft antialiased edges.
func TestCursor32bppKeepsAlpha(t *testing.T) {
	s := &pdu.PointerShape{
		Width: 2, Height: 1, XorBpp: 4,
		Xor: []byte{1, 2, 3, 0x00, 4, 5, 6, 0x80},
		And: []byte{0, 0}, // one padded row for a two pixel wide shape
	}
	cur := cursorFromShape(s)
	if got := rgbaAt(cur, 0, 0); got != [4]byte{3, 2, 1, 0} {
		t.Errorf("transparent edge = %v, want alpha 0", got)
	}
	if got := rgbaAt(cur, 1, 0); got != [4]byte{6, 5, 4, 0x80} {
		t.Errorf("half transparent pixel = %v, want alpha 0x80", got)
	}
}

// A hotspot outside the shape would have a caller position the cursor by a point
// that is not on it.
func TestCursorHotspotOutsideIsClamped(t *testing.T) {
	for _, s := range []*pdu.PointerShape{
		{Width: 2, Height: 2, XorBpp: 3, Xor: make([]byte, 4*2), And: make([]byte, 4), HotspotX: 99, HotspotY: 0},
		{Width: 2, Height: 2, XorBpp: 3, Xor: make([]byte, 4*2), And: make([]byte, 4), HotspotX: 0, HotspotY: 99},
	} {
		cur := cursorFromShape(s)
		if cur.Hotspot.X < 0 || cur.Hotspot.X >= cur.Width || cur.Hotspot.Y < 0 || cur.Hotspot.Y >= cur.Height {
			t.Errorf("hotspot %v is outside %dx%d", cur.Hotspot, cur.Width, cur.Height)
		}
	}
}

// A shape with no size at all is not a cursor, and returning one would have a
// caller draw a zero sized image.
func TestCursorEmptyShapeIsNil(t *testing.T) {
	for _, s := range []*pdu.PointerShape{
		nil,
		{Width: 0, Height: 4},
		{Width: 4, Height: 0},
		{Width: 4, Height: 4, XorBpp: 0},
	} {
		if cur := cursorFromShape(s); cur != nil {
			t.Errorf("shape %+v produced a cursor", s)
		}
	}
}

func TestSameShape(t *testing.T) {
	a := colourShape([3]byte{1, 2, 3})
	b := colourShape([3]byte{1, 2, 3})
	if !sameShape(a, b) {
		t.Error("identical shapes compared unequal")
	}
	b.Xor[0] = 9
	if sameShape(a, b) {
		t.Error("shapes differing in one byte compared equal")
	}
	if sameShape(nil, a) || sameShape(a, nil) {
		t.Error("nil compared equal to a shape")
	}
	if !sameShape(nil, nil) {
		t.Error("nil compared unequal to nil")
	}
	// A difference that is not in the pixels still matters: two shapes of the
	// same size with different hotspots put the pointer in different places.
	c := colourShape([3]byte{1, 2, 3})
	c.HotspotX = 1
	if sameShape(a, c) {
		t.Error("shapes with different hotspots compared equal")
	}
}

func TestCursorHidden(t *testing.T) {
	hidden := &Cursor{System: true, SystemType: pdu.SYSPTR_NULL}
	if !hidden.Hidden() {
		t.Error("SYSPTR_NULL is not reported as hidden")
	}
	def := &Cursor{System: true, SystemType: pdu.SYSPTR_DEFAULT}
	if def.Hidden() {
		t.Error("SYSPTR_DEFAULT is reported as hidden")
	}
	if (*Cursor)(nil).Hidden() {
		t.Error("a nil cursor is reported as hidden")
	}
}

// The cache is what makes a cached pointer possible at all, so the shape put in
// has to come back out unchanged and the caller's slice must not be able to
// alter it afterwards.
func TestPointerCacheRoundTripThroughTheClient(t *testing.T) {
	cache := pdu.NewPointerCache(20)
	s := colourShape([3]byte{1, 2, 3}, [3]byte{4, 5, 6})
	if err := cache.Put(5, s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := cache.Get(5)
	if !ok {
		t.Fatal("Get found nothing")
	}
	cur := cursorFromShape(got)
	if cur == nil {
		t.Fatal("the cached shape did not decode")
	}
	if px := rgbaAt(cur, 1, 0); px != [4]byte{6, 5, 4, 255} {
		t.Errorf("cached pixel = %v, want [6 5 4 255]", px)
	}
}

// monoShape builds a two colour shape: one bit per pixel in each mask, rows
// padded to an even number of bytes, most significant bit first.
//
// xorRows and andRows give one significant byte per row, which is all a shape
// narrower than nine pixels needs; the rest of each padded row stays zero. They
// are per row rather than laid out as the padded mask, so that a test cannot get
// the padding wrong in the same way the decoder could.
func monoShape(w, h int, xorRows, andRows []byte) *pdu.PointerShape {
	xorRow, andRow := pdu.PointerXorRowBytes(w, 1), pdu.AndMaskRowBytes(w)
	xor := make([]byte, xorRow*h)
	and := make([]byte, andRow*h)
	for y := 0; y < h; y++ {
		if y < len(xorRows) {
			xor[y*xorRow] = xorRows[y]
		}
		if y < len(andRows) {
			and[y*andRow] = andRows[y]
		}
	}
	return &pdu.PointerShape{Width: w, Height: h, XorBpp: 1, Xor: xor, And: and}
}

// All four combinations mean something different in a two colour pointer, which
// is why it cannot go through the colour path where an AND bit of one is simply
// transparent. The rule is FreeRDP's.
func TestCursorMonochromeFourCases(t *testing.T) {
	// Bits are most significant first: 0x80 is x=0, 0x40 is x=1.
	// Row 0 is to be black then white, which is and=0 with xor=0 then xor=1.
	// Row 1 is to be transparent, which is and=1 with xor=0.
	s := monoShape(4, 2,
		[]byte{0x40, 0x00}, // xor: row 0 has x=1, row 1 has nothing
		[]byte{0x00, 0x40}, // and: row 0 is clear, row 1 has x=1
	)
	cur := cursorFromShape(s)
	if cur == nil {
		t.Fatal("no cursor")
	}
	if got := rgbaAt(cur, 0, 0); got != [4]byte{0, 0, 0, 0xff} {
		t.Errorf("and=0 xor=0 = %v, want opaque black", got)
	}
	if got := rgbaAt(cur, 1, 0); got != [4]byte{0xff, 0xff, 0xff, 0xff} {
		t.Errorf("and=0 xor=1 = %v, want opaque white", got)
	}
	// The second row has the AND bit set at x=1 and the XOR bits clear, so
	// that pixel is transparent and its neighbour is black.
	if got := rgbaAt(cur, 1, 1); got != [4]byte{0, 0, 0, 0} {
		t.Errorf("and=1 xor=0 at x=1 = %v, want transparent", got)
	}
	if got := rgbaAt(cur, 0, 1); got != [4]byte{0, 0, 0, 0xff} {
		t.Errorf("and=0 xor=0 at x=0 = %v, want opaque black", got)
	}
}

// and=1 with xor=1 is the inverse of whatever is behind the pointer, which
// cannot be resolved from a shape alone. A checkerboard is what FreeRDP
// substitutes, and it stays visible on any background where a flat colour does
// not. Either way it must be opaque: leaving it transparent would erase part of
// the pointer.
func TestCursorMonochromeInvertedCaseIsOpaque(t *testing.T) {
	s := monoShape(4, 1, []byte{0xC0}, []byte{0xC0}) // both bits set for x=0,1
	cur := cursorFromShape(s)
	if cur == nil {
		t.Fatal("no cursor")
	}
	for x := 0; x < 4; x++ {
		got := rgbaAt(cur, x, 0)
		if got[3] != 0xff {
			t.Errorf("x=%d: alpha = %d, want 255 (the inverted case is drawn)", x, got[3])
		}
	}
	// The checkerboard alternates, so neighbouring pixels differ.
	if rgbaAt(cur, 0, 0) == rgbaAt(cur, 1, 0) {
		t.Error("the inverted case is a flat colour, which is the thing FreeRDP warns about")
	}
}

// A two colour pointer is not stored bottom up, unlike a colour one: FreeRDP's
// decoder flips only when the depth is not one. Getting this wrong draws every
// two colour cursor upside down.
func TestCursorMonochromeIsNotFlipped(t *testing.T) {
	// Row 0 has x=0 set in the colour mask, row 1 has it clear.
	s := monoShape(2, 2,
		[]byte{0x80, 0x00}, // xor row 0 has the first bit
		[]byte{0x00, 0x00}, // both rows opaque
	)
	cur := cursorFromShape(s)
	if got := rgbaAt(cur, 0, 0); got != [4]byte{0xff, 0xff, 0xff, 0xff} {
		t.Errorf("first wire row = %v, want white: the rows are top down", got)
	}
	if got := rgbaAt(cur, 0, 1); got != [4]byte{0, 0, 0, 0xff} {
		t.Errorf("second wire row = %v, want black", got)
	}
}
