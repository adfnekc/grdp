package pdu

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// writeGlyph appends one TS_CACHE_GLYPH_DATA, padded to the double word
// boundary the order uses, to a buffer.
func writeGlyph(b *bytes.Buffer, index uint16, x, y int16, cx, cy uint16, bits []byte) {
	var hdr [10]byte
	binary.LittleEndian.PutUint16(hdr[0:], index)
	binary.LittleEndian.PutUint16(hdr[2:], uint16(x))
	binary.LittleEndian.PutUint16(hdr[4:], uint16(y))
	binary.LittleEndian.PutUint16(hdr[6:], cx)
	binary.LittleEndian.PutUint16(hdr[8:], cy)
	b.Write(hdr[:])

	b.Write(bits)
	for i := len(bits); i%4 != 0; i++ {
		b.WriteByte(0)
	}
}

func TestParseGlyphCacheOrder(t *testing.T) {
	// The bytes of the "d" character from the specification's Glyph Image Data
	// example: five pixels wide, nine tall, one byte per row.
	dBitmap := []byte{0x08, 0x08, 0x08, 0x78, 0x88, 0x88, 0x88, 0x88, 0x78}

	var b bytes.Buffer
	b.WriteByte(2) // cacheId
	b.WriteByte(2) // cGlyphs
	writeGlyph(&b, 5, -1, 0, 5, 9, dBitmap)
	writeGlyph(&b, 6, 0, 0, 8, 1, []byte{0xAA})

	order, err := ParseGlyphCacheOrder(b.Bytes(), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if order.CacheID != 2 {
		t.Errorf("cache id is %d, want 2", order.CacheID)
	}
	if len(order.Glyphs) != 2 {
		t.Fatalf("parsed %d glyphs, want 2", len(order.Glyphs))
	}

	g := order.Glyphs[0]
	if g.CacheIndex != 5 {
		t.Errorf("cache index is %d, want 5", g.CacheIndex)
	}
	if g.X != -1 || g.Y != 0 {
		t.Errorf("origin is (%d,%d), want (-1,0)", g.X, g.Y)
	}
	if g.Width != 5 || g.Height != 9 {
		t.Errorf("size is %dx%d, want 5x9", g.Width, g.Height)
	}
	// The three padding bytes must not end up in the bitmap, or every row after
	// the first would be read from the wrong offset.
	if !bytes.Equal(g.Bits, dBitmap) {
		t.Errorf("bitmap is % x, want % x", g.Bits, dBitmap)
	}

	if got := order.Glyphs[1].Bits; !bytes.Equal(got, []byte{0xAA}) {
		t.Errorf("second bitmap is % x, want aa", got)
	}
	if len(order.Unicode) != 0 {
		t.Errorf("unicode characters present without the flag: %v", order.Unicode)
	}
}

func TestParseGlyphCacheOrderUnicode(t *testing.T) {
	var b bytes.Buffer
	b.WriteByte(0) // cacheId
	b.WriteByte(1) // cGlyphs
	writeGlyph(&b, 0, 0, 0, 1, 2, []byte{0x80, 0x80})
	b.WriteByte('A')
	b.WriteByte(0)

	order, err := ParseGlyphCacheOrder(b.Bytes(), CG_GLYPH_UNICODE_PRESENT)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(order.Unicode) != 1 || order.Unicode[0] != 'A' {
		t.Errorf("unicode characters are %v, want ['A']", order.Unicode)
	}
}

// An order that announces more glyphs than it carries must be rejected, not
// parsed as a shorter one, or the rest of the batch is read from the wrong
// offset.
func TestParseGlyphCacheOrderRejectsShortPayload(t *testing.T) {
	// One glyph announced, then only a cache index and no origin.
	data := []byte{0x00, 0x01, 0x07, 0x00}
	if _, err := ParseGlyphCacheOrder(data, 0); err == nil {
		t.Error("expected an error for a truncated glyph header")
	}

	// One glyph whose declared size runs past the end of the payload.
	var b bytes.Buffer
	b.WriteByte(0)
	b.WriteByte(1)
	writeGlyph(&b, 0, 0, 0, 64, 64, make([]byte, 512))
	if _, err := ParseGlyphCacheOrder(b.Bytes()[:b.Len()-256], 0); err == nil {
		t.Error("expected an error for a truncated bitmap")
	}

	// The unicode flag set but the characters missing.
	var c bytes.Buffer
	c.WriteByte(0)
	c.WriteByte(1)
	writeGlyph(&c, 0, 0, 0, 1, 1, []byte{0x80})
	if _, err := ParseGlyphCacheOrder(c.Bytes(), CG_GLYPH_UNICODE_PRESENT); err == nil {
		t.Error("expected an error for missing unicode characters")
	}
}

// The second GlyphIndex the specification annotates. Its fields are fixed
// width, so nothing depends on the delta coordinate flag the order carries.
func TestGlyphIndexUnpack(t *testing.T) {
	fields := []byte{
		0x00,             // fOpRedundant
		0xff, 0xff, 0xff, // ForeColor
		0x0c, 0x02, // BkLeft
		0x6e, 0x01, // BkTop
		0x4d, 0x02, // BkRight
		0x7b, 0x01, // BkBottom
		0x09, 0x02, // OpLeft
		0x6e, 0x01, // OpTop
		0xf6, 0x02, // OpRight
		0x7b, 0x01, // OpBottom
		0x0c, 0x02, // X
		0x79, 0x01, // Y
		0x03,             // cbData
		0xfe, 0x04, 0x00, // a USE of fragment 4 with a zero advance
	}

	var d GlyphIndex
	if err := d.Unpack(bytes.NewReader(fields), 0x383fe8, true); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.Type() != ORDER_TYPE_TEXT2 {
		t.Errorf("type is %d, want %d", d.Type(), ORDER_TYPE_TEXT2)
	}
	if d.FOpRedundant != 0 {
		t.Errorf("fOpRedundant is %d, want 0", d.FOpRedundant)
	}
	if d.ForeColor != [4]uint8{0xff, 0xff, 0xff, 0xff} {
		t.Errorf("foreground is %v, want opaque white", d.ForeColor)
	}
	if d.BackColor != [4]uint8{} {
		t.Errorf("background is %v, want absent", d.BackColor)
	}
	if d.BkLeft != 524 || d.BkTop != 366 || d.BkRight != 589 || d.BkBottom != 379 {
		t.Errorf("background rectangle is (%d,%d,%d,%d)", d.BkLeft, d.BkTop, d.BkRight, d.BkBottom)
	}
	if d.OpLeft != 521 || d.OpTop != 366 || d.OpRight != 758 || d.OpBottom != 379 {
		t.Errorf("opaque rectangle is (%d,%d,%d,%d)", d.OpLeft, d.OpTop, d.OpRight, d.OpBottom)
	}
	if d.X != 524 || d.Y != 377 {
		t.Errorf("pen is (%d,%d), want (524,377)", d.X, d.Y)
	}
	if !bytes.Equal(d.Data, []byte{0xfe, 0x04, 0x00}) {
		t.Errorf("data is % x", d.Data)
	}
}

// The first annotated GlyphIndex carries only a rectangle and the glyph run,
// which is the ADD that stores a fragment for later reuse.
func TestGlyphIndexUnpackGlyphRun(t *testing.T) {
	run := []byte{
		0x38, 0x00, 0x39, 0x07, 0x3a, 0x06, 0x3b, 0x07, 0x3c, 0x06, 0x3d, 0x06,
		0x18, 0x04, 0x1f, 0x06, 0x17, 0x02, 0x14, 0x04, 0x1b, 0x06, 0x19, 0x06,
		0x45, 0x05, 0x18, 0x06, 0x1f, 0x06, 0x1f, 0x02, 0x14, 0x02, 0x46, 0x06,
		0xff, 0x15, 0x24,
	}
	fields := append([]byte{0x6a, 0x02, byte(len(run))}, run...)

	var d GlyphIndex
	if err := d.Unpack(bytes.NewReader(fields), 0x200100, true); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.BkRight != 618 {
		t.Errorf("bkRight is %d, want 618", d.BkRight)
	}
	if !bytes.Equal(d.Data, run) {
		t.Errorf("data is % x, want % x", d.Data, run)
	}
}

// A field the flags say is present but the stream does not carry has to fail:
// the batch cannot be resynchronised after a short order.
func TestGlyphIndexUnpackRejectsShortFields(t *testing.T) {
	var d GlyphIndex
	if err := d.Unpack(bytes.NewReader(nil), 0x000001, false); err == nil {
		t.Error("expected an error for a missing cacheId")
	}

	// The glyph run says it is longer than it is.
	var e GlyphIndex
	if err := e.Unpack(bytes.NewReader([]byte{0x05, 0x01, 0x02}), 0x200000, false); err == nil {
		t.Error("expected an error for a truncated glyph run")
	}
}
