package pdu

import (
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
)

// The primary drawing orders this file parses: the four multi-rectangle orders
// and the FastIndex order. Before this file they either did not exist or had no
// parser, so a primary order of one of these types fell through
// processPrimaryOrder's default arm and returned an error. Returning an error is
// right (the order's length is unknown, so carrying on would read the rest of
// the batch as nonsense), but it throws away every later order in the same
// update as well.
const (
	ORDER_TYPE_MULTIDSTBLT     = 0x0F //15
	ORDER_TYPE_MULTIPATBLT     = 0x10 //16
	ORDER_TYPE_MULTISCRBLT     = 0x11 //17
	ORDER_TYPE_MULTIOPAQUERECT = 0x12 //18
	ORDER_TYPE_FAST_INDEX      = 0x13 //19
)

// MultiFieldBytes returns the number of field-flag bytes that follow the control
// flags for one of these orders, and whether orderType is one of them. This is
// not the same for all of them: FreeRDP's
// get_primary_drawing_order_field_bytes gives MULTI_DSTBLT one byte and
// MULTI_PATBLT, MULTI_SCRBLT, MULTI_OPAQUE_RECT and FAST_INDEX two. Read the
// wrong width and every field after the flags is shifted by a byte.
func MultiFieldBytes(orderType uint8) (int, bool) {
	switch orderType {
	case ORDER_TYPE_MULTIDSTBLT:
		return 1, true
	case ORDER_TYPE_MULTIPATBLT, ORDER_TYPE_MULTISCRBLT, ORDER_TYPE_MULTIOPAQUERECT, ORDER_TYPE_FAST_INDEX:
		return 2, true
	}
	return 0, false
}

// NewMultiOrder returns an empty order for one of the types this file parses,
// or nil for any other type.
//
// The caller has to keep using the concrete type for a given order type, because
// the fields an order leaves out repeat the previous order of the same type: a
// fresh value would lose them, and the symptom is not a parse error. A multi
// order that omits its rectangle list but not its count would otherwise read
// back zero rectangles and draw nothing.
func NewMultiOrder(orderType uint8) PrimaryOrder {
	switch orderType {
	case ORDER_TYPE_MULTIDSTBLT:
		return &MultiDstBlt{}
	case ORDER_TYPE_MULTIPATBLT:
		return &MultiPatBlt{}
	case ORDER_TYPE_MULTISCRBLT:
		return &MultiScrBlt{}
	case ORDER_TYPE_MULTIOPAQUERECT:
		return &MultiOpaqueRect{}
	case ORDER_TYPE_FAST_INDEX:
		return &FastIndex{}
	}
	return nil
}

// DeltaRect is one rectangle of a multi-rectangle order (MS-RDPEGDI's
// DELTA_RECT). A rectangle is (left, top, width, height); left and top are
// encoded relative to the previous rectangle, so the values here are already
// absolute.
//
// Width and height are not deltas, despite the encoding's name: FreeRDP reads
// them with update_read_delta and does not add the previous rectangle's value.
// What does carry over is an omitted width or height, which repeats the previous
// rectangle's.
type DeltaRect struct {
	Left   int32
	Top    int32
	Width  int32
	Height int32
}

// MultiDstBlt paints a set of rectangles with a destination-only raster
// operation. FreeRDP's parser is update_read_multi_dstblt_order.
type MultiDstBlt struct {
	// X, Y, Cx, Cy are the order's destination rectangle, which FreeRDP's
	// renderer ignores: it draws the rectangles below.
	X, Y, Cx, Cy int32
	Opcode       uint8

	NumRectangles int
	Rectangles    []DeltaRect
}

func (d *MultiDstBlt) Type() int {
	return ORDER_TYPE_MULTIDSTBLT
}

func (d *MultiDstBlt) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readPresentCoord(r, present, 0x0001, &d.X); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0002, &d.Y); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0004, &d.Cx); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0008, &d.Cy); err != nil {
		return err
	}
	if err := readPresentByte(r, present, 0x0010, &d.Opcode); err != nil {
		return err
	}
	n, rects, err := readMultiRects(r, present, 0x0020, 0x0040, d.NumRectangles, d.Rectangles)
	if err != nil {
		return err
	}
	d.NumRectangles, d.Rectangles = n, rects
	return nil
}

// MultiPatBlt paints a set of rectangles from a brush. FreeRDP's parser is
// update_read_multi_patblt_order.
type MultiPatBlt struct {
	X, Y, Cx, Cy int32
	Opcode       uint8
	BgColour     [4]uint8
	FgColour     [4]uint8
	Brush        Brush

	NumRectangles int
	Rectangles    []DeltaRect
}

func (d *MultiPatBlt) Type() int {
	return ORDER_TYPE_MULTIPATBLT
}

func (d *MultiPatBlt) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readPresentCoord(r, present, 0x0001, &d.X); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0002, &d.Y); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0004, &d.Cx); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0008, &d.Cy); err != nil {
		return err
	}
	if err := readPresentByte(r, present, 0x0010, &d.Opcode); err != nil {
		return err
	}
	if err := readPresentColour(r, present, 0x0020, &d.BgColour); err != nil {
		return err
	}
	if err := readPresentColour(r, present, 0x0040, &d.FgColour); err != nil {
		return err
	}
	// The brush fields sit in the second field-flag byte, starting at bit 7
	// (field 8), which is the shift FreeRDP passes to update_read_brush.
	brush, err := readBrush(r, present>>7)
	if err != nil {
		return err
	}
	d.Brush = brush

	// The count is field 13 and the rectangle list field 14, both past the
	// brush fields.
	n, rects, err := readMultiRects(r, present, 0x1000, 0x2000, d.NumRectangles, d.Rectangles)
	if err != nil {
		return err
	}
	d.NumRectangles, d.Rectangles = n, rects
	return nil
}

// MultiScrBlt copies a set of rectangles within the screen. FreeRDP's parser is
// update_read_multi_scrblt_order.
type MultiScrBlt struct {
	X, Y, Cx, Cy int32
	Opcode       uint8
	Srcx, Srcy   int32

	NumRectangles int
	Rectangles    []DeltaRect
}

func (d *MultiScrBlt) Type() int {
	return ORDER_TYPE_MULTISCRBLT
}

func (d *MultiScrBlt) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readPresentCoord(r, present, 0x0001, &d.X); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0002, &d.Y); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0004, &d.Cx); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0008, &d.Cy); err != nil {
		return err
	}
	if err := readPresentByte(r, present, 0x0010, &d.Opcode); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0020, &d.Srcx); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0040, &d.Srcy); err != nil {
		return err
	}
	n, rects, err := readMultiRects(r, present, 0x0080, 0x0100, d.NumRectangles, d.Rectangles)
	if err != nil {
		return err
	}
	d.NumRectangles, d.Rectangles = n, rects
	return nil
}

// MultiOpaqueRect fills a set of rectangles with one colour. FreeRDP's parser is
// update_read_multi_opaque_rect_order, and it is the only one of the four that
// FreeRDP also renders.
type MultiOpaqueRect struct {
	X, Y, Cx, Cy int32
	Colour       [4]uint8

	NumRectangles int
	Rectangles    []DeltaRect
}

func (d *MultiOpaqueRect) Type() int {
	return ORDER_TYPE_MULTIOPAQUERECT
}

func (d *MultiOpaqueRect) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readPresentCoord(r, present, 0x0001, &d.X); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0002, &d.Y); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0004, &d.Cx); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0008, &d.Cy); err != nil {
		return err
	}
	// The colour is carried as three separate one-byte fields, low byte first,
	// which is the same convention the single-rectangle OPAQUERECT_ORDER uses.
	if err := readPresentByte(r, present, 0x0010, &d.Colour[0]); err != nil {
		return err
	}
	if err := readPresentByte(r, present, 0x0020, &d.Colour[1]); err != nil {
		return err
	}
	if err := readPresentByte(r, present, 0x0040, &d.Colour[2]); err != nil {
		return err
	}
	n, rects, err := readMultiRects(r, present, 0x0080, 0x0100, d.NumRectangles, d.Rectangles)
	if err != nil {
		return err
	}
	d.NumRectangles, d.Rectangles = n, rects
	return nil
}

// FastIndex is the FAST_INDEX_ORDER, a glyph order that draws from the glyph
// cache. FreeRDP's parser is update_read_fast_index_order.
//
// It is not a rectangle order: the rectangles the multi orders carry have no
// equivalent here. It is parsed because leaving its fields unread leaves the
// stream out of step, but there is nothing to draw it with, because the glyph
// cache it names is not kept (the TEXT2 order has the same problem).
type FastIndex struct {
	CacheId uint8
	// CharInc is the width increment per character and FlAccel the text
	// acceleration flags; the two share one two-byte field.
	CharInc                          uint8
	FlAccel                          uint8
	BackColour                       [4]uint8
	ForeColour                       [4]uint8
	BkLeft, BkTop, BkRight, BkBottom int32
	OpLeft, OpTop, OpRight, OpBottom int32
	X, Y                             int32
	// Data is the variable length glyph fragment list the order ends with.
	Data []byte
}

func (d *FastIndex) Type() int {
	return ORDER_TYPE_FAST_INDEX
}

func (d *FastIndex) Unpack(r io.Reader, present uint32, delta bool) error {
	if err := readPresentByte(r, present, 0x0001, &d.CacheId); err != nil {
		return err
	}
	if present&0x0002 != 0 {
		charInc, err := core.ReadUInt8(r)
		if err != nil {
			return err
		}
		flAccel, err := core.ReadUInt8(r)
		if err != nil {
			return err
		}
		d.CharInc, d.FlAccel = charInc, flAccel
	}
	if err := readPresentColour(r, present, 0x0004, &d.BackColour); err != nil {
		return err
	}
	if err := readPresentColour(r, present, 0x0008, &d.ForeColour); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0010, &d.BkLeft); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0020, &d.BkTop); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0040, &d.BkRight); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0080, &d.BkBottom); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0100, &d.OpLeft); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0200, &d.OpTop); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0400, &d.OpRight); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x0800, &d.OpBottom); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x1000, &d.X); err != nil {
		return err
	}
	if err := readPresentCoord(r, present, 0x2000, &d.Y); err != nil {
		return err
	}
	if present&0x4000 != 0 {
		n, err := core.ReadUInt8(r)
		if err != nil {
			return err
		}
		data, err := core.ReadBytes(int(n), r)
		if err != nil {
			return err
		}
		d.Data = data
	}
	return nil
}

// readPresentCoord reads a coordinate field when its bit is set in the field
// flags. Multi-rectangle orders and FastIndex always state absolute
// coordinates: FreeRDP passes FALSE for the delta argument of every one of these
// fields, so the order's TS_DELTA_COORDINATES flag does not apply to them.
func readPresentCoord(r io.Reader, present, bit uint32, v *int32) error {
	if present&bit == 0 {
		return nil
	}
	b, err := core.ReadUint16LE(r)
	if err != nil {
		return err
	}
	*v = int32(int16(b))
	return nil
}

// readPresentByte reads a one-byte field when its bit is set in the field flags.
func readPresentByte(r io.Reader, present, bit uint32, v *uint8) error {
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

// readPresentColour reads a Generic Color field (three bytes, blue then green
// then red) when its bit is set. The bytes are kept as they arrive, which is the
// convention the single-rectangle orders use when they store a colour.
func readPresentColour(r io.Reader, present, bit uint32, c *[4]uint8) error {
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

// readBrush reads the brush a PATBLT-like order carries, returning the error the
// shared updateBrush drops. The seven pattern bytes plus the hatch make the
// eight byte tile the renderer understands.
func readBrush(r io.Reader, fieldFlags uint32) (Brush, error) {
	var b Brush
	if fieldFlags&0x01 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, err
		}
		b.X = v
	}
	if fieldFlags&0x02 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, err
		}
		b.Y = v
	}
	if fieldFlags&0x04 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, err
		}
		b.Style = v
	}
	if fieldFlags&0x08 != 0 {
		v, err := core.ReadUInt8(r)
		if err != nil {
			return b, err
		}
		b.Hatch = v
	}
	if fieldFlags&0x10 != 0 {
		data, err := core.ReadBytes(7, r)
		if err != nil {
			return b, err
		}
		b.Data = make([]byte, 0, 8)
		b.Data = append(b.Data, b.Hatch)
		b.Data = append(b.Data, data...)
	}
	return b, nil
}

// readMultiRects reads the rectangle count and the rectangle list that end every
// multi-rectangle order.
//
// count is the count carried from the previous order of the same type, because
// an order that leaves the count out repeats it. numBit is the field-flag bit of
// the count and listBit that of the CodedDeltaList. FreeRDP reads the count into
// a local initialised from the previous order and only commits it, together with
// the rectangles, when the list itself is present; when the list is absent the
// previous rectangles are reused, which is why the previous slice is returned
// unchanged.
func readMultiRects(r io.Reader, present, numBit, listBit uint32, prev int, prevRects []DeltaRect) (int, []DeltaRect, error) {
	count := prev
	if present&numBit != 0 {
		b, err := core.ReadUInt8(r)
		if err != nil {
			return 0, nil, err
		}
		count = int(b)
	}

	if present&listBit != 0 {
		// The CodedDeltaList is a two byte header length followed by the
		// Delta-Encoded Rectangles. The length is not needed: the count says
		// where the list ends.
		if _, err := core.ReadUint16LE(r); err != nil {
			return 0, nil, err
		}
		rects, err := readDeltaRects(r, count)
		if err != nil {
			return 0, nil, err
		}
		return count, rects, nil
	}

	if count > prev {
		return 0, nil, fmt.Errorf("multi order: %d rectangles but the previous order of this type had %d", count, prev)
	}
	return count, prevRects, nil
}

// readDeltaRects reads MS-RDPEGDI's Delta-Encoded Rectangles (FreeRDP's
// update_read_delta_rects).
//
// A zero bit per rectangle component says the component was left out: an omitted
// left or top means zero, an omitted width or height repeats the previous
// rectangle's. Left and top accumulate from the previous rectangle, starting
// from zero. Two rectangles share each byte of the zero bitmap, high nibble
// first.
func readDeltaRects(r io.Reader, count int) ([]DeltaRect, error) {
	if count < 0 || count > 45 {
		// FreeRDP refuses more than 45, which is what the fixed arrays in the
		// protocol allow.
		return nil, fmt.Errorf("multi order: %d rectangles, at most 45", count)
	}
	zeroBits, err := core.ReadBytes((count+1)/2, r)
	if err != nil {
		return nil, err
	}

	rects := make([]DeltaRect, count)
	var flags uint8
	for i := 0; i < count; i++ {
		if i%2 == 0 {
			flags = zeroBits[i/2]
		}
		if flags&0x80 == 0 {
			v, err := readDelta(r)
			if err != nil {
				return nil, err
			}
			rects[i].Left = v
		}
		if flags&0x40 == 0 {
			v, err := readDelta(r)
			if err != nil {
				return nil, err
			}
			rects[i].Top = v
		}
		if flags&0x20 == 0 {
			v, err := readDelta(r)
			if err != nil {
				return nil, err
			}
			rects[i].Width = v
		} else if i > 0 {
			rects[i].Width = rects[i-1].Width
		}
		if flags&0x10 == 0 {
			v, err := readDelta(r)
			if err != nil {
				return nil, err
			}
			rects[i].Height = v
		} else if i > 0 {
			rects[i].Height = rects[i-1].Height
		}
		if i > 0 {
			rects[i].Left += rects[i-1].Left
			rects[i].Top += rects[i-1].Top
		}
		flags <<= 4
	}
	return rects, nil
}

// readDelta reads MS-RDPEGDI's variable length signed delta: a six bit value in
// one byte, sign extended when bit 6 is set, with a second byte following when
// bit 7 is set. It is FreeRDP's update_read_delta, with the error it does check
// for.
func readDelta(r io.Reader) (int32, error) {
	b, err := core.ReadUInt8(r)
	if err != nil {
		return 0, err
	}
	var v int32
	if b&0x40 != 0 {
		v = int32(b) | ^int32(0x3F)
	} else {
		v = int32(b & 0x3F)
	}
	if b&0x80 != 0 {
		next, err := core.ReadUInt8(r)
		if err != nil {
			return 0, err
		}
		v = v<<8 | int32(next)
	}
	return v, nil
}
