package pdu

import (
	"bytes"
	"testing"
)

func TestMaskRowSizes(t *testing.T) {
	// The specification's own worked example: a 3x3 cursor at 24bpp has ten
	// byte scan lines, because three pixels of three bytes is nine and the row
	// is padded to the next even number. Sizing the mask as width*height*bpp
	// reads every row after the first from one byte too early.
	if got := XorMaskRowBytes(3, 3); got != 10 {
		t.Errorf("XorMaskRowBytes(3, 3) = %d, want 10", got)
	}
	cases := []struct {
		width, bpp, want int
	}{
		{3, 3, 10},   // the spec's example
		{4, 3, 12},   // already even
		{1, 3, 4},    // one pixel of three bytes, padded
		{32, 3, 96},  // a whole number of bytes, no padding needed
		{32, 4, 128}, // 32bpp
		{1, 3, 4},    // minimum
	}
	for _, c := range cases {
		if got := XorMaskRowBytes(c.width, c.bpp); got != c.want {
			t.Errorf("XorMaskRowBytes(%d, %d) = %d, want %d", c.width, c.bpp, got, c.want)
		}
	}
	// The AND mask is one bit per pixel, padded to two bytes. Three pixels need
	// one byte, so the row is two.
	for _, c := range []struct{ width, want int }{
		{1, 2}, {8, 2}, {9, 2}, {16, 2}, {17, 4}, {3, 2},
	} {
		if got := AndMaskRowBytes(c.width); got != c.want {
			t.Errorf("AndMaskRowBytes(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

// buildColorPointer builds a TS_PTRMSGTYPE_COLOR update on the wire, with the
// scan line padding the specification asks for. Every byte is written here
// rather than constructed in a struct, because a struct cannot be misread and
// the bug this guards against was a size calculation.
func buildColorPointer(width, height, bpp int, cacheIndex uint16) []byte {
	var b bytes.Buffer
	write16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	write16(TS_PTRMSGTYPE_COLOR)
	write16(cacheIndex)
	write16(1) // hotspotX
	write16(2) // hotspotY
	write16(uint16(width))
	write16(uint16(height))
	xorRow, andRow := XorMaskRowBytes(width, bpp), AndMaskRowBytes(width)
	for i := 0; i < xorRow*height; i++ {
		b.WriteByte(byte(0xA0 + i%16))
	}
	for i := 0; i < andRow*height; i++ {
		b.WriteByte(byte(0x10 + i%8))
	}
	// lengthAndMask and lengthXorMask follow the masks. They are declarative
	// rather than load bearing, since the masks are sized from the geometry,
	// but the stream carries them and a reader that ignores them is out of
	// step from the next field onwards.
	write16(uint16(andRow * height))
	write16(uint16(xorRow * height))
	return b.Bytes()
}

// The masks are read at their padded size, and the reader is left exactly at the
// end of the shape. A sentinel after it stands in for whatever the stream
// carries next: if the shape were read short or long, the sentinel would not
// come back intact.
func TestColorPointerShapeIsReadAtItsPaddedSize(t *testing.T) {
	for _, c := range []struct{ width, height, bpp int }{
		{3, 3, 3},  // the odd width the padding is about
		{1, 1, 3},  // the smallest odd case
		{5, 2, 3},  // another odd width
		{32, 8, 3}, // an even width, where nothing is padded
		{16, 4, 3}, // a width that is a whole number of bytes
	} {
		wire := buildColorPointer(c.width, c.height, c.bpp, 7)
		wire = append(wire, 0xDE, 0xAD, 0xBE, 0xEF)

		var p PointerDataPDU
		r := bytes.NewReader(wire)
		if err := p.Unpack(r); err != nil {
			t.Fatalf("%dx%d at %dbpp: %v", c.width, c.height, c.bpp, err)
		}
		xorRow, andRow := XorMaskRowBytes(c.width, c.bpp), AndMaskRowBytes(c.width)
		if len(p.Data) != xorRow*c.height {
			t.Errorf("%dx%d at %dbpp: XOR mask is %d bytes, want %d",
				c.width, c.height, c.bpp, len(p.Data), xorRow*c.height)
		}
		if len(p.Mask) != andRow*c.height {
			t.Errorf("%dx%d at %dbpp: AND mask is %d bytes, want %d",
				c.width, c.height, c.bpp, len(p.Mask), andRow*c.height)
		}
		if p.XorBpp != c.bpp {
			t.Errorf("%dx%d: XorBpp = %d, want %d", c.width, c.height, p.XorBpp, c.bpp)
		}
		if p.CacheIndex != 7 {
			t.Errorf("%dx%d: CacheIndex = %d, want 7", c.width, c.height, p.CacheIndex)
		}
		// The rest of the stream has to still be there.
		rest := make([]byte, 4)
		if _, err := r.Read(rest); err != nil {
			t.Fatalf("%dx%d: the shape consumed the sentinel: %v", c.width, c.height, err)
		}
		if !bytes.Equal(rest, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
			t.Errorf("%dx%d: the stream after the shape is % X, want DEADBEEF (the shape was read short or long)",
				c.width, c.height, rest)
		}
	}
}

// systemPointerType is four bytes, not two with padding. Reading half of it left
// the other half in the stream, and left no caller able to tell an arrow from an
// hourglass.
func TestSystemPointerTypeIsFourBytes(t *testing.T) {
	for _, want := range []uint32{SYSPTR_NULL, SYSPTR_DEFAULT, 0x00007F01} {
		wire := []byte{
			byte(TS_PTRMSGTYPE_SYSTEM), 0,
			byte(want), byte(want >> 8), byte(want >> 16), byte(want >> 24),
			0xDE, 0xAD,
		}
		var p PointerDataPDU
		r := bytes.NewReader(wire)
		if err := p.Unpack(r); err != nil {
			t.Fatalf("%#x: %v", want, err)
		}
		if p.SystemType != want {
			t.Errorf("SystemType = %#x, want %#x", p.SystemType, want)
		}
		rest := make([]byte, 2)
		if _, err := r.Read(rest); err != nil {
			t.Fatalf("%#x: consumed the sentinel: %v", want, err)
		}
		if !bytes.Equal(rest, []byte{0xDE, 0xAD}) {
			t.Errorf("%#x: stream after the type is % X, want DEAD", want, rest)
		}
	}
}

// A shape whose declared size is past any sane cursor is refused rather than
// allocated. These numbers come off the wire.
func TestPointerShapeSizeIsBounded(t *testing.T) {
	var b bytes.Buffer
	for _, v := range []uint16{TS_PTRMSGTYPE_COLOR, 0, 0, 0, 0xFFFF, 0xFFFF} {
		b.Write([]byte{byte(v), byte(v >> 8)})
	}
	var p PointerDataPDU
	if err := p.Unpack(bytes.NewReader(b.Bytes())); err == nil {
		t.Error("a 65535x65535 pointer was accepted")
	}
}

func TestPointerCacheStoresAndReturns(t *testing.T) {
	c := NewPointerCache(20)
	if c.Size() != 20 {
		t.Fatalf("Size = %d, want 20", c.Size())
	}
	// An untouched slot is a gap, not an empty shape: the caller has to be able
	// to tell "the server never sent this" from "this is a blank cursor".
	if _, ok := c.Get(3); ok {
		t.Error("an empty slot reported a shape")
	}
	want := &PointerShape{Width: 2, Height: 2, XorBpp: 3, Xor: []byte{1, 2, 3}}
	if err := c.Put(3, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := c.Get(3)
	if !ok {
		t.Fatal("Get after Put found nothing")
	}
	if got.Width != 2 || len(got.Xor) != 3 {
		t.Errorf("Get returned %+v", got)
	}
	// The cache must not be reachable through the caller's copy.
	want.Xor[0] = 0xFF
	if again, _ := c.Get(3); again.Xor[0] == 0xFF {
		t.Error("the cache handed out its own storage")
	}
}

// An index past the advertised slots is refused. Wrapping it would overwrite
// some other cursor, which is a worse outcome than dropping the shape.
func TestPointerCacheRefusesOutOfRangeIndex(t *testing.T) {
	c := NewPointerCache(20)
	if err := c.Put(20, &PointerShape{Width: 1, Height: 1}); err == nil {
		t.Error("slot 20 of 20 was accepted")
	}
	if err := c.Put(0, &PointerShape{Width: 1, Height: 1}); err != nil {
		t.Errorf("slot 0 was refused: %v", err)
	}
	if _, ok := c.Get(20); ok {
		t.Error("slot 20 reported a shape")
	}
}

// A zero sized cache would panic on index zero, which is a legal index.
func TestPointerCacheWithNoSlotsStillWorksForSlotZero(t *testing.T) {
	c := NewPointerCache(0)
	if c.Size() < 1 {
		t.Fatalf("Size = %d, want at least 1", c.Size())
	}
	if err := c.Put(0, &PointerShape{Width: 1, Height: 1}); err != nil {
		t.Errorf("Put(0) on a empty cache: %v", err)
	}
}

func TestPointerShapeFillIsACopy(t *testing.T) {
	s := &PointerShape{Xor: []byte{1}, And: []byte{2}}
	f := s.Fill()
	if f == s {
		t.Fatal("Fill returned the same pointer")
	}
	f.Xor[0], f.And[0] = 9, 9
	if s.Xor[0] != 1 || s.And[0] != 2 {
		t.Error("Fill shares storage with the original")
	}
	if (*PointerShape)(nil).Fill() != nil {
		t.Error("Fill on nil is not nil")
	}
}
