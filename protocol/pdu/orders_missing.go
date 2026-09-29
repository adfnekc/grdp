package pdu

import (
	"bytes"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
)

// The primary drawing orders that had no parser. A primary order of one of
// these types used to reach processPrimaryOrder's default arm and return an
// error; an order nobody can read has an unknown length, so it does not merely
// fail to draw itself, it takes every later order in the same batch with it.
//
// These are parsed and not advertised in the order capability, so no server
// this project has reached sends them. The type numbers are FreeRDP's
// ORDER_TYPE_DRAW_NINE_GRID, ORDER_TYPE_MULTI_DRAW_NINE_GRID and
// ORDER_TYPE_FAST_GLYPH.
const (
	ORDER_TYPE_DRAWNINEGRID       = 0x07 // 7
	ORDER_TYPE_MULTI_DRAWNINEGRID = 0x08 // 8
	ORDER_TYPE_FAST_GLYPH         = 0x18 // 24
)

// MissingFieldBytes returns the number of field-flag bytes that follow the
// control flags for one of the orders this file parses, and whether orderType
// is one of them. It is the counterpart of MultiFieldBytes for these types and
// has to be consulted alongside it: a fast glyph order carries two field-flag
// bytes, where the default of one would shift every field after the flags.
//
// The values are FreeRDP's get_primary_drawing_order_field_bytes:
// DRAW_NINE_GRID_ORDER_FIELD_BYTES and MULTI_DRAW_NINE_GRID_ORDER_FIELD_BYTES
// are 1, FAST_GLYPH_ORDER_FIELD_BYTES is 2.
func MissingFieldBytes(orderType uint8) (int, bool) {
	switch orderType {
	case ORDER_TYPE_DRAWNINEGRID, ORDER_TYPE_MULTI_DRAWNINEGRID:
		return 1, true
	case ORDER_TYPE_FAST_GLYPH:
		return 2, true
	}
	return 0, false
}

// NewMissingOrder returns an empty order for one of the types this file parses,
// or nil for any other type.
//
// The caller has to keep using the concrete type for a given order type, because
// an order that leaves fields out repeats the previous order of the same type.
// A fresh value would lose them, and the symptom is not a parse error.
func NewMissingOrder(orderType uint8) PrimaryOrder {
	switch orderType {
	case ORDER_TYPE_DRAWNINEGRID:
		return &DrawNineGrid{}
	case ORDER_TYPE_MULTI_DRAWNINEGRID:
		return &MultiDrawNineGrid{}
	case ORDER_TYPE_FAST_GLYPH:
		return &FastGlyph{}
	}
	return nil
}

// readMissingPresentCoord reads a coordinate field when its bit is set in the
// field flags. FreeRDP's read_order_field_coord hands each coordinate to
// update_read_coord with the order's TS_DELTA_COORDINATES flag, so a delta
// order carries one signed byte per coordinate and the value accumulates on
// whatever the previous order of the same type left. That is not tied to the
// order type: every coordinate these orders carry is read that way.
//
// It is separate from the readPresentCoord the multi-rectangle orders use, which
// honours TS_DELTA_COORDINATES, as every other primary order here does: an order
// that sets it sends a coordinate as one byte relative to the previous order of
// the same type, not as two absolute ones.
// FreeRDP is the authority for the wire, and it applies the delta.
func readMissingPresentCoord(r io.Reader, present, bit uint32, v *int32, delta bool) error {
	if present&bit == 0 {
		return nil
	}
	if delta {
		change, err := core.ReadUInt8(r)
		if err != nil {
			return err
		}
		*v += int32(int8(change))
		return nil
	}
	change, err := core.ReadUint16LE(r)
	if err != nil {
		return err
	}
	*v = int32(int16(change))
	return nil
}

// readMissingPresentByte reads a one-byte field when its bit is set.
func readMissingPresentByte(r io.Reader, present, bit uint32, v *uint8) error {
	if present&bit == 0 {
		return nil
	}
	b, err := core.ReadUInt8(r)
	if err != nil {
		return err
	}
	*v = b
	return nil
}

// readMissingPresentUint16 reads a two-byte little endian field when its bit is
// set.
func readMissingPresentUint16(r io.Reader, present, bit uint32, v *uint16) error {
	if present&bit == 0 {
		return nil
	}
	b, err := core.ReadUint16LE(r)
	if err != nil {
		return err
	}
	*v = b
	return nil
}

// readMissingPresentColour reads a three-byte Generic Color field, blue then
// green then red, when its bit is set. The bytes are kept in the order they
// arrive, which is the convention the other orders use.
func readMissingPresentColour(r io.Reader, present, bit uint32, c *[4]uint8) error {
	if present&bit == 0 {
		return nil
	}
	b, err := core.ReadUInt8(r)
	if err != nil {
		return err
	}
	g, err := core.ReadUInt8(r)
	if err != nil {
		return err
	}
	red, err := core.ReadUInt8(r)
	if err != nil {
		return err
	}
	c[0], c[1], c[2], c[3] = b, g, red, 0xff
	return nil
}

// DrawNineGrid is the DRAW_NINE_GRID_ORDER: a bitmap drawn in a nine part grid,
// whose parts come from the nine-grid cache an alternate secondary order fills.
//
// FreeRDP's parser is update_read_draw_nine_grid_order. The order is not drawn
// here, because the nine-grid cache it names is not kept; it is parsed so that
// its fields do not leave the rest of the batch out of step.
type DrawNineGrid struct {
	SrcLeft   int32
	SrcTop    int32
	SrcRight  int32
	SrcBottom int32
	BitmapID  uint16
}

func (d *DrawNineGrid) Type() int {
	return ORDER_TYPE_DRAWNINEGRID
}

func (d *DrawNineGrid) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readMissingPresentCoord(r, present, 0x0001, &d.SrcLeft, delta); err != nil {
		return fmt.Errorf("draw nine grid: srcLeft: %w", err)
	}
	if err := readMissingPresentCoord(r, present, 0x0002, &d.SrcTop, delta); err != nil {
		return fmt.Errorf("draw nine grid: srcTop: %w", err)
	}
	if err := readMissingPresentCoord(r, present, 0x0004, &d.SrcRight, delta); err != nil {
		return fmt.Errorf("draw nine grid: srcRight: %w", err)
	}
	if err := readMissingPresentCoord(r, present, 0x0008, &d.SrcBottom, delta); err != nil {
		return fmt.Errorf("draw nine grid: srcBottom: %w", err)
	}
	if err := readMissingPresentUint16(r, present, 0x0010, &d.BitmapID); err != nil {
		return fmt.Errorf("draw nine grid: bitmapId: %w", err)
	}
	return nil
}

// MultiDrawNineGrid is the MULTI_DRAW_NINE_GRID_ORDER: a DrawNineGrid repeated
// over a list of rectangles. FreeRDP's parser is
// update_read_multi_draw_nine_grid_order.
//
// The bitmapId is field 5, the count field 6 and the CodedDeltaList field 7, the
// same shape MultiDstBlt uses, so the rectangle list is read with the shared
// readMultiRects. It is not drawn for the same reason DrawNineGrid is not.
type MultiDrawNineGrid struct {
	SrcLeft   int32
	SrcTop    int32
	SrcRight  int32
	SrcBottom int32
	BitmapID  uint16

	NumRectangles int
	Rectangles    []DeltaRect
}

func (d *MultiDrawNineGrid) Type() int {
	return ORDER_TYPE_MULTI_DRAWNINEGRID
}

func (d *MultiDrawNineGrid) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readMissingPresentCoord(r, present, 0x0001, &d.SrcLeft, delta); err != nil {
		return fmt.Errorf("multi draw nine grid: srcLeft: %w", err)
	}
	if err := readMissingPresentCoord(r, present, 0x0002, &d.SrcTop, delta); err != nil {
		return fmt.Errorf("multi draw nine grid: srcTop: %w", err)
	}
	if err := readMissingPresentCoord(r, present, 0x0004, &d.SrcRight, delta); err != nil {
		return fmt.Errorf("multi draw nine grid: srcRight: %w", err)
	}
	if err := readMissingPresentCoord(r, present, 0x0008, &d.SrcBottom, delta); err != nil {
		return fmt.Errorf("multi draw nine grid: srcBottom: %w", err)
	}
	if err := readMissingPresentUint16(r, present, 0x0010, &d.BitmapID); err != nil {
		return fmt.Errorf("multi draw nine grid: bitmapId: %w", err)
	}
	n, rects, err := readMultiRects(r, present, 0x0020, 0x0040, d.NumRectangles, d.Rectangles)
	if err != nil {
		return fmt.Errorf("multi draw nine grid: %w", err)
	}
	d.NumRectangles, d.Rectangles = n, rects
	return nil
}

// FastGlyph is the FAST_GLYPH_ORDER: a single glyph the server stores in one of
// its glyph caches and draws in the same order, with the bitmap itself following
// the order rather than an index into the cache. FreeRDP's parser is
// update_read_fast_glyph_order.
//
// The order is parsed but not drawn: the task that added it is the parse, and a
// server that sent it would also fill the cache through a Cache Glyph order.
type FastGlyph struct {
	CacheID uint8
	// CharInc is the width increment per character and FlAccel the text
	// acceleration flags; the two share one two-byte field, as in FastIndex.
	CharInc    uint8
	FlAccel    uint8
	BackColour [4]uint8
	ForeColour [4]uint8

	BkLeft, BkTop, BkRight, BkBottom int32
	OpLeft, OpTop, OpRight, OpBottom int32
	X, Y                             int32

	// Data is the length-prefixed glyph blob the order ends with, kept raw.
	// Glyph is that blob decoded as a GLYPH_DATA_V2. When the blob is a single
	// byte it carries only the cache index and Glyph holds just that.
	Data  []byte
	Glyph Glyph
}

func (d *FastGlyph) Type() int {
	return ORDER_TYPE_FAST_GLYPH
}

func (d *FastGlyph) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readMissingPresentByte(r, present, 0x0001, &d.CacheID); err != nil {
		return fmt.Errorf("fast glyph: cacheId: %w", err)
	}
	if d.CacheID > 9 {
		// There are ten glyph caches, numbered zero to nine, and FreeRDP
		// refuses an order that names another.
		return fmt.Errorf("fast glyph: cache id %d is outside the ten glyph caches", d.CacheID)
	}
	if present&0x0002 != 0 {
		charInc, err := core.ReadUInt8(r)
		if err != nil {
			return fmt.Errorf("fast glyph: ulCharInc: %w", err)
		}
		flAccel, err := core.ReadUInt8(r)
		if err != nil {
			return fmt.Errorf("fast glyph: flAccel: %w", err)
		}
		d.CharInc, d.FlAccel = charInc, flAccel
	}
	if err := readMissingPresentColour(r, present, 0x0004, &d.BackColour); err != nil {
		return fmt.Errorf("fast glyph: backColor: %w", err)
	}
	if err := readMissingPresentColour(r, present, 0x0008, &d.ForeColour); err != nil {
		return fmt.Errorf("fast glyph: foreColor: %w", err)
	}
	coords := []struct {
		bit   uint32
		field *int32
		name  string
	}{
		{0x0010, &d.BkLeft, "bkLeft"},
		{0x0020, &d.BkTop, "bkTop"},
		{0x0040, &d.BkRight, "bkRight"},
		{0x0080, &d.BkBottom, "bkBottom"},
		{0x0100, &d.OpLeft, "opLeft"},
		{0x0200, &d.OpTop, "opTop"},
		{0x0400, &d.OpRight, "opRight"},
		{0x0800, &d.OpBottom, "opBottom"},
		{0x1000, &d.X, "x"},
		{0x2000, &d.Y, "y"},
	}
	for _, c := range coords {
		if err := readMissingPresentCoord(r, present, c.bit, c.field, delta); err != nil {
			return fmt.Errorf("fast glyph: %s: %w", c.name, err)
		}
	}

	if present&0x4000 != 0 {
		n, err := core.ReadUInt8(r)
		if err != nil {
			return fmt.Errorf("fast glyph: cbData: %w", err)
		}
		data, err := core.ReadBytes(int(n), r)
		if err != nil {
			return fmt.Errorf("fast glyph: glyph data of %d bytes: %w", n, err)
		}
		d.Data = data
		if err := d.decodeGlyph(); err != nil {
			return err
		}
	}
	return nil
}

// decodeGlyph reads the GLYPH_DATA_V2 that Data carries. The blob is
// self-describing only up to its length, which is why it is decoded here rather
// than left to a renderer: a body that does not hold the fields it must is a
// malformed order, and FreeRDP fails the whole order for it.
func (d *FastGlyph) decodeGlyph() error {
	if len(d.Data) == 0 {
		return fmt.Errorf("fast glyph: glyph data is empty")
	}
	d.Glyph = Glyph{CacheIndex: uint32(d.Data[0])}
	if len(d.Data) == 1 {
		// A single byte is the cache index on its own, which is what the order
		// carries when the glyph is already in the cache.
		return nil
	}

	r := bytes.NewReader(d.Data[1:])
	x, err := read2ByteSigned(r)
	if err != nil {
		return fmt.Errorf("fast glyph: glyph x: %w", err)
	}
	y, err := read2ByteSigned(r)
	if err != nil {
		return fmt.Errorf("fast glyph: glyph y: %w", err)
	}
	cx, err := read2ByteUnsigned(r)
	if err != nil {
		return fmt.Errorf("fast glyph: glyph cx: %w", err)
	}
	cy, err := read2ByteUnsigned(r)
	if err != nil {
		return fmt.Errorf("fast glyph: glyph cy: %w", err)
	}
	if cx == 0 || cy == 0 {
		return fmt.Errorf("fast glyph: glyph is %dx%d", cx, cy)
	}
	// The remaining bytes are the bitmap. Its length is bounded by the cbData
	// byte above, so nothing is allocated from a value read off the wire here.
	bits := make([]byte, r.Len())
	if _, err := r.Read(bits); err != nil {
		return fmt.Errorf("fast glyph: glyph bits: %w", err)
	}

	d.Glyph.X = int16(x)
	d.Glyph.Y = int16(y)
	d.Glyph.Width = int(cx)
	d.Glyph.Height = int(cy)
	d.Glyph.Bits = bits
	return nil
}

// read2ByteSigned reads MS-RDPEGDI's two-byte signed value (FreeRDP's
// update_read_2byte_signed): six bits and a sign in one byte, with a second byte
// following when the first byte's top bit is set.
func read2ByteSigned(r io.Reader) (int32, error) {
	b, err := core.ReadUInt8(r)
	if err != nil {
		return 0, err
	}
	v := int32(b & 0x3f)
	if b&0x80 != 0 {
		next, err := core.ReadUInt8(r)
		if err != nil {
			return 0, err
		}
		v = v<<8 | int32(next)
	}
	if b&0x40 != 0 {
		v = -v
	}
	return v, nil
}

// read2ByteUnsigned reads MS-RDPEGDI's two-byte unsigned value (FreeRDP's
// update_read_2byte_unsigned): seven bits in one byte, with a second following
// when the first byte's top bit is set.
func read2ByteUnsigned(r io.Reader) (uint32, error) {
	b, err := core.ReadUInt8(r)
	if err != nil {
		return 0, err
	}
	if b&0x80 != 0 {
		next, err := core.ReadUInt8(r)
		if err != nil {
			return 0, err
		}
		return uint32(b&0x7f)<<8 | uint32(next), nil
	}
	return uint32(b & 0x7f), nil
}

// Operation returns a SAVE_BITMAP order's operation byte. The field is
// unexported in orders.go, and the renderer in the orders package has to read it
// to tell a save from a restore.
func (d *SaveBitmap) Operation() uint8 {
	return d.action
}
