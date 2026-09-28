package pdu

import (
	"bytes"
	"testing"
)

// The multi-rectangle orders and FastIndex used to have no parser, so a primary
// order of one of these types made the whole batch fail. These tests pin down
// the field order and the rectangle encoding against FreeRDP's
// update_read_multi_* functions.

func TestMultiDstBltParsesRectangles(t *testing.T) {
	// All seven fields present: the destination rectangle, the rop, the count
	// and the two byte header followed by two rectangles. A zero bitmap of 0x00
	// says every component of both rectangles is written.
	raw := []byte{
		10, 0, // nLeftRect
		5, 0, // nTopRect
		20, 0, // nWidth
		10, 0, // nHeight
		0x00,       // bRop
		2,          // nDeltaEntries
		0x00, 0x00, // CodedDeltaList header length
		0x00,         // zero bitmap
		10, 20, 5, 8, // first rectangle: left, top, width, height
		3, 0, 5, 8, // second: left delta, top delta, width, height
	}

	var d MultiDstBlt
	if err := d.Unpack(bytes.NewReader(raw), 0x007F, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.X != 10 || d.Y != 5 || d.Cx != 20 || d.Cy != 10 || d.Opcode != 0x00 {
		t.Fatalf("document rectangle and rop are %+v", d)
	}
	if d.NumRectangles != 2 || len(d.Rectangles) != 2 {
		t.Fatalf("got %d rectangles, %d stored", d.NumRectangles, len(d.Rectangles))
	}
	// The second rectangle's left and top are deltas from the first: 3 added to
	// 10, 0 added to 20.
	want := []DeltaRect{
		{Left: 10, Top: 20, Width: 5, Height: 8},
		{Left: 13, Top: 20, Width: 5, Height: 8},
	}
	for i := range want {
		if d.Rectangles[i] != want[i] {
			t.Errorf("rectangle %d is %+v, want %+v", i, d.Rectangles[i], want[i])
		}
	}
}

func TestMultiPatBltParsesBrushAndRectangles(t *testing.T) {
	// Fields 1-4 coordinates, 5 rop, 6/7 colours, 10 the brush style (field 8
	// of the order, which is the second field-flag byte), 13 the count and 14
	// the rectangle list.
	const present = 0x000F | 0x0010 | 0x0020 | 0x0040 | 0x0200 | 0x1000 | 0x2000
	raw := []byte{
		1, 0, 2, 0, 3, 0, 4, 0, // rectangle
		0xF0,             // bRop, PATCOPY
		0x11, 0x22, 0x33, // back colour
		0x44, 0x55, 0x66, // fore colour
		0x00,       // brush style, solid
		1,          // nDeltaEntries
		0x00, 0x00, // CodedDeltaList header length
		0x00,       // zero bitmap
		2, 3, 8, 9, // one rectangle: left, top, width, height
	}

	var d MultiPatBlt
	if err := d.Unpack(bytes.NewReader(raw), present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.X != 1 || d.Y != 2 || d.Cx != 3 || d.Cy != 4 || d.Opcode != 0xF0 {
		t.Fatalf("document rectangle and rop are %+v", d)
	}
	if d.BgColour != [4]uint8{0x11, 0x22, 0x33, 0xff} {
		t.Errorf("background colour is %v", d.BgColour)
	}
	if d.FgColour != [4]uint8{0x44, 0x55, 0x66, 0xff} {
		t.Errorf("foreground colour is %v", d.FgColour)
	}
	if d.Brush.Style != 0 {
		t.Errorf("brush style is %d, want 0", d.Brush.Style)
	}
	if d.NumRectangles != 1 || len(d.Rectangles) != 1 {
		t.Fatalf("got %d rectangles, %d stored", d.NumRectangles, len(d.Rectangles))
	}
	if got := d.Rectangles[0]; got != (DeltaRect{Left: 2, Top: 3, Width: 8, Height: 9}) {
		t.Errorf("rectangle is %+v", got)
	}
}

func TestMultiScrBltParsesSourceAndRectangles(t *testing.T) {
	// Fields 1-4 coordinates, 5 rop, 6/7 source origin, 8 the count, 9 the list.
	const present = 0x000F | 0x0010 | 0x0020 | 0x0040 | 0x0080 | 0x0100
	raw := []byte{
		1, 0, 2, 0, 3, 0, 4, 0, // destination rectangle
		0xCC,       // bRop, SRCCOPY
		7, 0, 9, 0, // nXSrc, nYSrc
		1,          // nDeltaEntries
		0x00, 0x00, // CodedDeltaList header length
		0x00,       // zero bitmap
		5, 6, 2, 2, // one rectangle
	}

	var d MultiScrBlt
	if err := d.Unpack(bytes.NewReader(raw), present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.Srcx != 7 || d.Srcy != 9 {
		t.Errorf("source origin is (%d,%d), want (7,9)", d.Srcx, d.Srcy)
	}
	if d.Opcode != 0xCC {
		t.Errorf("rop is %#02x, want SRCCOPY", d.Opcode)
	}
	if d.NumRectangles != 1 || len(d.Rectangles) != 1 {
		t.Fatalf("got %d rectangles, %d stored", d.NumRectangles, len(d.Rectangles))
	}
	if got := d.Rectangles[0]; got != (DeltaRect{Left: 5, Top: 6, Width: 2, Height: 2}) {
		t.Errorf("rectangle is %+v", got)
	}
}

func TestMultiOpaqueRectParsesColour(t *testing.T) {
	// Fields 1-4 coordinates, 5/6/7 the three colour bytes, 8 the count, 9 the
	// list.
	const present = 0x000F | 0x0010 | 0x0020 | 0x0040 | 0x0080 | 0x0100
	raw := []byte{
		0, 0, 0, 0, 4, 0, 4, 0, // destination rectangle
		0x33, 0x22, 0x11, // colour bytes, low first
		1,          // nDeltaEntries
		0x00, 0x00, // CodedDeltaList header length
		0x00,       // zero bitmap
		1, 2, 3, 4, // one rectangle
	}

	var d MultiOpaqueRect
	if err := d.Unpack(bytes.NewReader(raw), present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.Colour != [4]uint8{0x33, 0x22, 0x11, 0} {
		t.Errorf("colour is %v", d.Colour)
	}
	if d.NumRectangles != 1 || len(d.Rectangles) != 1 {
		t.Fatalf("got %d rectangles, %d stored", d.NumRectangles, len(d.Rectangles))
	}
	if got := d.Rectangles[0]; got != (DeltaRect{Left: 1, Top: 2, Width: 3, Height: 4}) {
		t.Errorf("rectangle is %+v", got)
	}
}

// An omitted width or height is not zero: it repeats the previous rectangle's,
// and the values themselves are the high nibble of the zero bitmap for the even
// rectangle and the low nibble for the odd one.
func TestReadDeltaRectsInheritsOmittedSize(t *testing.T) {
	raw := []byte{
		0x03,         // second rectangle omits its width and its height
		10, 20, 5, 8, // first rectangle: all four present
		3, 0, // second: left and top only
	}
	rects, err := readDeltaRects(bytes.NewReader(raw), 2)
	if err != nil {
		t.Fatalf("readDeltaRects: %v", err)
	}
	want := []DeltaRect{
		{Left: 10, Top: 20, Width: 5, Height: 8},
		{Left: 13, Top: 20, Width: 5, Height: 8},
	}
	for i := range want {
		if rects[i] != want[i] {
			t.Errorf("rectangle %d is %+v, want %+v", i, rects[i], want[i])
		}
	}

	// More than 45 rectangles is refused rather than read into a list the
	// protocol cannot carry.
	if _, err := readDeltaRects(bytes.NewReader(nil), 46); err == nil {
		t.Error("46 rectangles were accepted, want an error")
	}
}

// An order that cannot be read has an unknown length, so it must return an error
// rather than read nothing and let the rest of the batch be parsed as nonsense.
func TestMultiOrderTruncatedIsRejected(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		present uint32
		order   PrimaryOrder
	}{
		{
			// The flags say the rectangle list follows, but the two byte
			// header is not there.
			name:    "missing rectangle list",
			raw:     []byte{1, 0, 2, 0, 3, 0, 4, 0, 0x00, 1},
			present: 0x007F,
			order:   &MultiDstBlt{},
		},
		{
			// A coordinate field is cut short.
			name:    "short coordinate",
			raw:     []byte{1},
			present: 0x0001,
			order:   &MultiOpaqueRect{},
		},
		{
			// The rectangle list is present but the delta bytes are gone.
			name:    "short rectangle list",
			raw:     []byte{1, 0, 2, 0, 3, 0, 4, 0, 0x00, 1, 0x00, 0x00, 0x00, 5},
			present: 0x007F,
			order:   &MultiDstBlt{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.order.Unpack(bytes.NewReader(c.raw), c.present, false); err == nil {
				t.Error("expected an error, got a silent read")
			}
		})
	}
}

// FreeRDP's get_primary_drawing_order_field_bytes is the authority for how many
// field-flag bytes each order has; reading the wrong width shifts every field.
func TestMultiFieldBytes(t *testing.T) {
	want := map[uint8]int{
		ORDER_TYPE_MULTIDSTBLT:     1,
		ORDER_TYPE_MULTIPATBLT:     2,
		ORDER_TYPE_MULTISCRBLT:     2,
		ORDER_TYPE_MULTIOPAQUERECT: 2,
		ORDER_TYPE_FAST_INDEX:      2,
	}
	for orderType, n := range want {
		got, ok := MultiFieldBytes(orderType)
		if !ok || got != n {
			t.Errorf("order type %d: got %d bytes, ok=%v, want %d", orderType, got, ok, n)
		}
		if o := NewMultiOrder(orderType); o == nil || o.Type() != int(orderType) {
			t.Errorf("order type %d: NewMultiOrder gave %T", orderType, o)
		}
	}
	if _, ok := MultiFieldBytes(ORDER_TYPE_DSTBLT); ok {
		t.Error("a single-rectangle order was claimed as a multi order")
	}
	if NewMultiOrder(ORDER_TYPE_DSTBLT) != nil {
		t.Error("NewMultiOrder returned an order for DSTBLT")
	}
}

// FastIndex ends with a length-prefixed glyph fragment list. It is parsed even
// though it is not drawn, because leaving its fields unread would take the rest
// of the batch with it.
func TestFastIndexParsesTrailingData(t *testing.T) {
	// Field 1 cacheId and field 15 the glyph data list.
	raw := []byte{2, 3, 0xAA, 0xBB, 0xCC}

	var d FastIndex
	if err := d.Unpack(bytes.NewReader(raw), 0x4001, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.CacheId != 2 {
		t.Errorf("cache id is %d, want 2", d.CacheId)
	}
	if !bytes.Equal(d.Data, []byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("glyph data is %v", d.Data)
	}

	// A declared length that is not there must be an error, not a short read
	// that leaves the stream out of step.
	if err := d.Unpack(bytes.NewReader([]byte{2, 4, 0xAA}), 0x4001, false); err == nil {
		t.Error("a truncated glyph fragment list was accepted")
	}
}
