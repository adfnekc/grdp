package pdu

import (
	"bytes"
	"math/rand"
	"testing"
)

// The three primary orders that had no parser: DrawNineGrid, MultiDrawNineGrid
// and FastGlyph. A primary order of one of these types used to reach
// processPrimaryOrder's default arm, which returns an error and so drops every
// later order in the same batch. These pin down the field order against
// FreeRDP's update_read_draw_nine_grid_order, update_read_multi_draw_nine_grid_order
// and update_read_fast_glyph_order.
//
// The batch dispatch itself lives in orders.go and is not this file's to
// change, so the parsers are called directly, exactly as processPrimaryOrder
// calls them: with the field flags already read and the TS_DELTA_COORDINATES
// decision passed in.

// sentinelMemblt is a MEMBLT with known values, used to prove that an order
// before it consumed exactly the right number of bytes. An order that reads the
// wrong length does not fail; it leaves the stream out of step and the MEMBLT
// comes out wrong or not at all.
func sentinelMemblt() []byte {
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	b.WriteByte(3) // cacheId
	b.WriteByte(0) // colour table
	writeU16(5)    // x
	writeU16(6)    // y
	writeU16(32)   // width
	writeU16(16)   // height
	writeU16(77)   // cacheIndex
	return b.Bytes()
}

const sentinelPresent = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0100

// checkSentinelMemblt parses the sentinel that must follow the order under test
// and reports whether it arrived intact.
func checkSentinelMemblt(t *testing.T, r *bytes.Reader) {
	t.Helper()
	var m Memblt
	if err := m.Unpack(r, sentinelPresent, false); err != nil {
		t.Fatalf("the MEMBLT after the order did not parse: %v", err)
	}
	if m.CacheId != 3 || m.CacheIdx != 77 || m.Cx != 32 || m.Cy != 16 || m.X != 5 || m.Y != 6 {
		t.Errorf("the MEMBLT after the order is %+v, so the order consumed the wrong number of bytes", m)
	}
}

// FreeRDP's get_primary_drawing_order_field_bytes gives DRAW_NINE_GRID and
// MULTI_DRAW_NINE_GRID one field-flag byte and FAST_GLYPH two. The default in
// processPrimaryOrder is one, so a fast glyph order without this would read its
// fields one byte early.
func TestMissingFieldBytes(t *testing.T) {
	want := map[uint8]int{
		ORDER_TYPE_DRAWNINEGRID:       1,
		ORDER_TYPE_MULTI_DRAWNINEGRID: 1,
		ORDER_TYPE_FAST_GLYPH:         2,
	}
	for orderType, n := range want {
		got, ok := MissingFieldBytes(orderType)
		if !ok || got != n {
			t.Errorf("order type %d: got %d bytes, ok=%v, want %d", orderType, got, ok, n)
		}
		if o := NewMissingOrder(orderType); o == nil || o.Type() != int(orderType) {
			t.Errorf("order type %d: NewMissingOrder gave %T", orderType, o)
		}
	}
	if _, ok := MissingFieldBytes(ORDER_TYPE_MEMBLT); ok {
		t.Error("a single-rectangle order was claimed as one of the missing orders")
	}
	if NewMissingOrder(ORDER_TYPE_MEMBLT) != nil {
		t.Error("NewMissingOrder returned an order for MEMBLT")
	}
}

func TestDrawNineGridParses(t *testing.T) {
	const present = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010
	body := []byte{
		10, 0, 20, 0, 30, 0, 40, 0, // srcLeft, srcTop, srcRight, srcBottom
		0x34, 0x12, // bitmapId
	}
	r := bytes.NewReader(append(body, sentinelMemblt()...))

	var d DrawNineGrid
	if err := d.Unpack(r, present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.SrcLeft != 10 || d.SrcTop != 20 || d.SrcRight != 30 || d.SrcBottom != 40 {
		t.Errorf("source rectangle is %d,%d,%d,%d", d.SrcLeft, d.SrcTop, d.SrcRight, d.SrcBottom)
	}
	if d.BitmapID != 0x1234 {
		t.Errorf("bitmap id is %#x, want 0x1234", d.BitmapID)
	}
	checkSentinelMemblt(t, r)
}

// With TS_DELTA_COORDINATES every coordinate of these orders is one signed byte
// added to the previous order's value, which is what FreeRDP's read_order_field_coord
// does for them. Reading two absolute bytes instead shifts the stream by a byte
// per present coordinate.
func TestDrawNineGridDeltaCoordinates(t *testing.T) {
	const present = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010
	body := []byte{
		0x02,       // srcLeft += 2
		0xff,       // srcTop += -1
		0x03,       // srcRight += 3
		0xfe,       // srcBottom += -2
		0x34, 0x12, // bitmapId
	}
	r := bytes.NewReader(append(body, sentinelMemblt()...))

	// The values a previous order of the same type left are carried in by the
	// dispatch before Unpack runs; a delta accumulates on them.
	d := DrawNineGrid{SrcLeft: 100, SrcTop: 100, SrcRight: 100, SrcBottom: 100}
	if err := d.Unpack(r, present, true); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.SrcLeft != 102 || d.SrcTop != 99 || d.SrcRight != 103 || d.SrcBottom != 98 {
		t.Errorf("delta coordinates gave %d,%d,%d,%d", d.SrcLeft, d.SrcTop, d.SrcRight, d.SrcBottom)
	}
	checkSentinelMemblt(t, r)
}

func TestMultiDrawNineGridParsesRectangles(t *testing.T) {
	const present = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0020 | 0x0040
	body := []byte{
		10, 0, 5, 0, 20, 0, 10, 0, // srcLeft, srcTop, srcRight, srcBottom
		0x34, 0x12, // bitmapId
		2,          // nDeltaEntries
		0x00, 0x00, // CodedDeltaList header length
		0x00,         // zero bitmap for both rectangles
		10, 20, 5, 8, // first rectangle
		3, 0, 5, 8, // second: left and top are deltas
	}
	r := bytes.NewReader(append(body, sentinelMemblt()...))

	var d MultiDrawNineGrid
	if err := d.Unpack(r, present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.SrcLeft != 10 || d.SrcTop != 5 || d.SrcRight != 20 || d.SrcBottom != 10 || d.BitmapID != 0x1234 {
		t.Errorf("order is %+v", d)
	}
	if d.NumRectangles != 2 || len(d.Rectangles) != 2 {
		t.Fatalf("got %d rectangles, %d stored", d.NumRectangles, len(d.Rectangles))
	}
	want := []DeltaRect{
		{Left: 10, Top: 20, Width: 5, Height: 8},
		{Left: 13, Top: 20, Width: 5, Height: 8},
	}
	for i := range want {
		if d.Rectangles[i] != want[i] {
			t.Errorf("rectangle %d is %+v, want %+v", i, d.Rectangles[i], want[i])
		}
	}
	checkSentinelMemblt(t, r)
}

// A full fast glyph order with a two by two glyph: the fields, then the
// length-prefixed GLYPH_DATA_V2 the order ends with.
func TestFastGlyphParsesGlyph(t *testing.T) {
	const present = 0x0001 | 0x0002 | 0x0004 | 0x0008 |
		0x0010 | 0x0020 | 0x0040 | 0x0080 | 0x0100 | 0x0200 | 0x0400 | 0x0800 |
		0x1000 | 0x2000 | 0x4000
	body := []byte{
		2,    // cacheId
		3, 4, // ulCharInc, flAccel
		0x11, 0x22, 0x33, // backColor
		0x44, 0x55, 0x66, // foreColor
		1, 0, 2, 0, 3, 0, 4, 0, // bkLeft, bkTop, bkRight, bkBottom
		5, 0, 6, 0, 7, 0, 8, 0, // opLeft, opTop, opRight, opBottom
		9, 0, 10, 0, // x, y
		7,          // cbData
		5,          // glyph cache index
		0x01,       // x = 1
		0x42,       // y = -2
		0x02,       // cx = 2
		0x02,       // cy = 2
		0xC0, 0x80, // the packed bitmap
	}
	r := bytes.NewReader(append(body, sentinelMemblt()...))

	var d FastGlyph
	if err := d.Unpack(r, present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.CacheID != 2 || d.CharInc != 3 || d.FlAccel != 4 {
		t.Errorf("header is %+v", d)
	}
	if d.BackColour != [4]uint8{0x11, 0x22, 0x33, 0xff} {
		t.Errorf("background colour is %v", d.BackColour)
	}
	if d.ForeColour != [4]uint8{0x44, 0x55, 0x66, 0xff} {
		t.Errorf("foreground colour is %v", d.ForeColour)
	}
	if d.BkLeft != 1 || d.BkRight != 3 || d.OpBottom != 8 || d.X != 9 || d.Y != 10 {
		t.Errorf("coordinates came out as %+v", d)
	}
	if !bytes.Equal(d.Data, body[len(body)-7:]) {
		t.Errorf("raw glyph data is %v", d.Data)
	}
	if d.Glyph.CacheIndex != 5 || d.Glyph.X != 1 || d.Glyph.Y != -2 ||
		d.Glyph.Width != 2 || d.Glyph.Height != 2 {
		t.Errorf("glyph is %+v", d.Glyph)
	}
	if !bytes.Equal(d.Glyph.Bits, []byte{0xC0, 0x80}) {
		t.Errorf("glyph bits are %v", d.Glyph.Bits)
	}
	checkSentinelMemblt(t, r)
}

// A glyph blob of a single byte carries only the cache index, which is the case
// of a glyph already in the cache.
func TestFastGlyphSingleByteGlyph(t *testing.T) {
	const present = 0x0001 | 0x4000
	body := []byte{2, 1, 5} // cacheId, cbData, glyph cache index
	r := bytes.NewReader(append(body, sentinelMemblt()...))

	var d FastGlyph
	if err := d.Unpack(r, present, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.Glyph.CacheIndex != 5 || len(d.Glyph.Bits) != 0 {
		t.Errorf("glyph is %+v, want just cache index 5", d.Glyph)
	}
	checkSentinelMemblt(t, r)
}

// An order that cannot be read has an unknown length, so it must return an
// error rather than read nothing and let the rest of the batch be parsed as
// nonsense.
func TestMissingOrdersTruncatedIsRejected(t *testing.T) {
	cases := []struct {
		name    string
		order   PrimaryOrder
		present uint32
		raw     []byte
	}{
		{"nine grid short coordinate", &DrawNineGrid{}, 0x0001, []byte{0x10}},
		{"nine grid short bitmap id", &DrawNineGrid{}, 0x0010, []byte{0x10}},
		{"multi nine grid short bitmap id", &MultiDrawNineGrid{}, 0x0010, []byte{0x10}},
		{"fast glyph short coordinate", &FastGlyph{}, 0x0010, []byte{0x01}},
		{"fast glyph short colour", &FastGlyph{}, 0x0004, []byte{0x01}},
		{"fast glyph empty data", &FastGlyph{}, 0x4001, []byte{2, 0}},
		{"fast glyph data longer than the blob", &FastGlyph{}, 0x4001, []byte{2, 4, 1, 2}},
		{"fast glyph blob too short for its fields", &FastGlyph{}, 0x4001, []byte{2, 3, 5, 1, 1}},
		{"fast glyph zero sized", &FastGlyph{}, 0x4001, []byte{2, 5, 5, 0, 0, 0, 0}},
		{"fast glyph cache id out of range", &FastGlyph{}, 0x0001, []byte{10}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.order.Unpack(bytes.NewReader(c.raw), c.present, false); err == nil {
				t.Error("expected an error, got a silent read")
			}
		})
	}
}

// FastGlyph carries its coordinates the same way DrawNineGrid does, one byte per
// coordinate under TS_DELTA_COORDINATES.
func TestFastGlyphDeltaCoordinates(t *testing.T) {
	const present = 0x0010 | 0x1000 // bkLeft and x
	body := []byte{0x05, 0x03}      // bkLeft += 5, x += 3
	r := bytes.NewReader(append(body, sentinelMemblt()...))

	d := FastGlyph{BkLeft: 10, X: 10}
	if err := d.Unpack(r, present, true); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.BkLeft != 15 || d.X != 13 {
		t.Errorf("delta coordinates gave bkLeft %d, x %d", d.BkLeft, d.X)
	}
	checkSentinelMemblt(t, r)
}

// The renderer in the orders package has to read the save bitmap operation, and
// its field is unexported, so the accessor has to return what the parser read.
func TestSaveBitmapOperationAccessor(t *testing.T) {
	var d SaveBitmap
	if err := d.Unpack(bytes.NewReader([]byte{0x02}), 0x0020, false); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if d.Operation() != 0x02 {
		t.Errorf("operation is %#x, want 0x02", d.Operation())
	}
}

// These orders arrive from the server, so their parsers are fed bytes this
// project did not choose. Every field is length checked, but the point of this
// test is that no combination of flags and bytes makes one of them panic, loop
// or allocate from a value it read.
func TestMissingOrderParsersSurviveNoise(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		data := make([]byte, random.Intn(96))
		random.Read(data)
		present := uint32(random.Uint32())
		delta := random.Intn(2) == 0
		for _, o := range []PrimaryOrder{&DrawNineGrid{}, &MultiDrawNineGrid{}, &FastGlyph{}} {
			if err := o.Unpack(bytes.NewReader(data), present, delta); err != nil {
				continue
			}
		}
	}
}
