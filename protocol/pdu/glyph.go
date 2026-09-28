package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
)

// CG_GLYPH_UNICODE_PRESENT is the extraFlags bit a Cache Glyph secondary order
// sets when it ends with the Unicode characters of the glyphs it carries.
const CG_GLYPH_UNICODE_PRESENT = 0x0100

// Text output accelerators from a GlyphIndex order's flAccel field. They decide
// whether the packed glyph run carries an advance and where it moves the pen,
// so the renderer needs them as well as the parser.
const (
	SO_HORIZONTAL             = 0x02
	SO_VERTICAL               = 0x04
	SO_CHAR_INC_EQUAL_BM_BASE = 0x20
)

// Glyph is one monochrome glyph from a glyph cache.
//
// The bitmap is one bit per pixel, top down, each row byte aligned with the
// most significant bit at the left, which is how TS_CACHE_GLYPH_DATA packs it.
type Glyph struct {
	// CacheIndex is the slot the glyph fills inside its glyph cache, and is the
	// index a GlyphIndex order names to draw it.
	CacheIndex uint32
	// X and Y are the origin of the character within the bitmap, a signed
	// offset added to the pen position when the glyph is drawn.
	X, Y int16
	// Width and Height are the bitmap size in pixels.
	Width, Height int
	// Bits is the packed bitmap, (Width+7)/8 bytes per row. The double word
	// padding the order applies is not included.
	Bits []byte
}

// GlyphCacheOrder is a Cache Glyph secondary order: the bitmaps a server stores
// in one of its glyph caches for a later GlyphIndex order to draw.
type GlyphCacheOrder struct {
	// CacheID is the glyph cache the glyphs belong to, 0 to 9.
	CacheID uint32
	Glyphs  []Glyph
	// Unicode is the diagnostic character list, present only when the order set
	// CG_GLYPH_UNICODE_PRESENT. It is not needed to draw the glyphs.
	Unicode []uint16
}

// ParseGlyphCacheOrder reads a Cache Glyph (revision 1) secondary order, whose
// payload starts at its cacheId byte.
//
// The layout is FreeRDP's update_read_cache_glyph_order: a two byte header, one
// GLYPH_DATA per glyph, then the characters when the order announced them.
func ParseGlyphCacheOrder(data []byte, flags uint16) (*GlyphCacheOrder, error) {
	r := bytes.NewReader(data)
	cacheID, err := core.ReadUInt8(r)
	if err != nil {
		return nil, fmt.Errorf("cache glyph order: cacheId: %w", err)
	}
	count, err := core.ReadUInt8(r)
	if err != nil {
		return nil, fmt.Errorf("cache glyph order: cGlyphs: %w", err)
	}

	order := &GlyphCacheOrder{CacheID: uint32(cacheID)}
	order.Glyphs = make([]Glyph, 0, int(count))
	for i := 0; i < int(count); i++ {
		g, err := readCacheGlyph(r)
		if err != nil {
			return nil, fmt.Errorf("cache glyph order: glyph %d of %d: %w", i+1, count, err)
		}
		order.Glyphs = append(order.Glyphs, g)
	}

	if flags&CG_GLYPH_UNICODE_PRESENT != 0 && count > 0 {
		want := int(count) * 2
		if want > r.Len() {
			return nil, fmt.Errorf("cache glyph order: unicode characters: want %d bytes, have %d",
				want, r.Len())
		}
		b, err := core.ReadBytes(want, r)
		if err != nil {
			return nil, fmt.Errorf("cache glyph order: unicode characters: %w", err)
		}
		order.Unicode = make([]uint16, count)
		for i := range order.Unicode {
			order.Unicode[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
	}
	return order, nil
}

// readCacheGlyph reads one TS_CACHE_GLYPH_DATA. The bitmap is padded to a double
// word boundary on the wire, which is what FreeRDP's glyph->cb computes; the
// padding is discarded.
func readCacheGlyph(r *bytes.Reader) (Glyph, error) {
	index, err := core.ReadUint16LE(r)
	if err != nil {
		return Glyph{}, fmt.Errorf("cacheIndex: %w", err)
	}
	x, err := core.ReadUint16LE(r)
	if err != nil {
		return Glyph{}, fmt.Errorf("x: %w", err)
	}
	y, err := core.ReadUint16LE(r)
	if err != nil {
		return Glyph{}, fmt.Errorf("y: %w", err)
	}
	cx, err := core.ReadUint16LE(r)
	if err != nil {
		return Glyph{}, fmt.Errorf("cx: %w", err)
	}
	cy, err := core.ReadUint16LE(r)
	if err != nil {
		return Glyph{}, fmt.Errorf("cy: %w", err)
	}

	size := ((int(cx) + 7) / 8) * int(cy)
	padded := size
	if rem := padded % 4; rem != 0 {
		padded += 4 - rem
	}
	// Check the size before reading it. A malformed width and height would
	// otherwise ask for a buffer far larger than the order can hold, and the
	// bitmap body has no length field of its own to catch it.
	if padded > r.Len() {
		return Glyph{}, fmt.Errorf("bitmap of %dx%d: want %d bytes, have %d",
			cx, cy, padded, r.Len())
	}
	raw, err := core.ReadBytes(padded, r)
	if err != nil {
		return Glyph{}, fmt.Errorf("bitmap of %dx%d: %w", cx, cy, err)
	}

	return Glyph{
		CacheIndex: uint32(index),
		X:          int16(x),
		Y:          int16(y),
		Width:      int(cx),
		Height:     int(cy),
		Bits:       raw[:size],
	}, nil
}

// GlyphIndex is the TEXT2 (GlyphIndex) primary order. It draws glyphs already
// in a glyph cache at a pen position, with the advance between them packed into
// the order rather than kept with the glyph.
//
// The fields are FreeRDP's update_read_glyph_index_order. Unlike the drawing
// orders, none of them are coordinate compressed: every one is read at its full
// width even when the order carries TS_DELTA_COORDINATES.
type GlyphIndex struct {
	CacheID      uint8
	FlAccel      uint8
	UlCharInc    uint8
	FOpRedundant uint8

	// BackColor and ForeColor are TS_COLOR: red, green and blue. The renderer
	// turns them into BGRA.
	BackColor [4]uint8
	ForeColor [4]uint8

	BkLeft, BkTop, BkRight, BkBottom int32
	OpLeft, OpTop, OpRight, OpBottom int32

	Brush Brush

	// X and Y are the pen position of the first glyph.
	X, Y int32
	// Data is the packed glyph run: glyph cache indices and USE and ADD
	// fragment instructions with the advances between them.
	Data []byte
}

func (d *GlyphIndex) Type() int {
	return ORDER_TYPE_TEXT2
}

// GlyphIndex is the primary order the TEXT2 dispatch has to build, so it has to
// satisfy PrimaryOrder.
var _ PrimaryOrder = (*GlyphIndex)(nil)

func (d *GlyphIndex) Unpack(r io.Reader, present uint32, _ bool) error {
	if err := d.read(r, present); err != nil {
		return fmt.Errorf("text2 order: %w", err)
	}
	return nil
}

func (d *GlyphIndex) read(r io.Reader, present uint32) error {
	readByte := func(dst *uint8, bit uint32, name string) error {
		if present&bit == 0 {
			return nil
		}
		v, err := core.ReadUInt8(r)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = v
		return nil
	}
	readInt16 := func(dst *int32, bit uint32, name string) error {
		if present&bit == 0 {
			return nil
		}
		v, err := core.ReadUint16LE(r)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = int32(int16(v))
		return nil
	}
	readColor := func(dst *[4]uint8, bit uint32, name string) error {
		if present&bit == 0 {
			return nil
		}
		b, err := core.ReadBytes(3, r)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		// TS_COLOR is 3 bytes, red then green then blue: FreeRDP's
		// update_read_color and the spec's Generic Color agree, and the
		// annotated dumps show no fourth byte.
		*dst = [4]uint8{b[0], b[1], b[2], 0xff}
		return nil
	}

	if err := readByte(&d.CacheID, 0x000001, "cacheId"); err != nil {
		return err
	}
	if err := readByte(&d.FlAccel, 0x000002, "flAccel"); err != nil {
		return err
	}
	if err := readByte(&d.UlCharInc, 0x000004, "ulCharInc"); err != nil {
		return err
	}
	if err := readByte(&d.FOpRedundant, 0x000008, "fOpRedundant"); err != nil {
		return err
	}
	if err := readColor(&d.BackColor, 0x000010, "backColor"); err != nil {
		return err
	}
	if err := readColor(&d.ForeColor, 0x000020, "foreColor"); err != nil {
		return err
	}
	if err := readInt16(&d.BkLeft, 0x000040, "bkLeft"); err != nil {
		return err
	}
	if err := readInt16(&d.BkTop, 0x000080, "bkTop"); err != nil {
		return err
	}
	if err := readInt16(&d.BkRight, 0x000100, "bkRight"); err != nil {
		return err
	}
	if err := readInt16(&d.BkBottom, 0x000200, "bkBottom"); err != nil {
		return err
	}
	if err := readInt16(&d.OpLeft, 0x000400, "opLeft"); err != nil {
		return err
	}
	if err := readInt16(&d.OpTop, 0x000800, "opTop"); err != nil {
		return err
	}
	if err := readInt16(&d.OpRight, 0x001000, "opRight"); err != nil {
		return err
	}
	if err := readInt16(&d.OpBottom, 0x002000, "opBottom"); err != nil {
		return err
	}

	brush, err := readGlyphBrush(r, present>>14)
	if err != nil {
		return fmt.Errorf("brush: %w", err)
	}
	d.Brush = brush

	if err := readInt16(&d.X, 0x080000, "x"); err != nil {
		return err
	}
	if err := readInt16(&d.Y, 0x100000, "y"); err != nil {
		return err
	}
	if present&0x200000 != 0 {
		n, err := core.ReadUInt8(r)
		if err != nil {
			return fmt.Errorf("cbData: %w", err)
		}
		data, err := core.ReadBytes(int(n), r)
		if err != nil {
			return fmt.Errorf("glyph data of %d bytes: %w", n, err)
		}
		d.Data = data
	}
	return nil
}

// readGlyphBrush reads the five brush fields a GlyphIndex order may carry,
// whose presence bits are the top five of the field flags. It is the same
// layout as Brush.updateBrush, repeated here so that a short read is reported
// instead of silently leaving the stream out of step.
func readGlyphBrush(r io.Reader, present uint32) (Brush, error) {
	var b Brush
	if present&1 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, fmt.Errorf("brushOrgX: %w", err)
		}
		b.X = v
	}
	if present&2 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, fmt.Errorf("brushOrgY: %w", err)
		}
		b.Y = v
	}
	if present&4 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, fmt.Errorf("brushStyle: %w", err)
		}
		b.Style = v
	}
	if present&8 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, fmt.Errorf("brushHatch: %w", err)
		}
		b.Hatch = v
	}
	if present&16 != 0 {
		extra, err := core.ReadBytes(7, r)
		if err != nil {
			return b, fmt.Errorf("brushExtra: %w", err)
		}
		b.Data = make([]byte, 0, 8)
		b.Data = append(b.Data, b.Hatch)
		b.Data = append(b.Data, extra...)
	}
	return b, nil
}
