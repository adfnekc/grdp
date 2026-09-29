package pdu

import (
	"bytes"
	"hash/fnv"
	"os"
	"testing"
)

// The bitmap cache is what makes orders work: secondary orders fill it and
// MEMBLT reads it back. These cover decoding one cache entry, which is the part
// that can be checked without a server.

func TestCachePixelsUncompressed(t *testing.T) {
	// 2x1 at 32bpp, stored rather than compressed.
	raw := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	got := cachePixels(raw, 2, 1, 32, false)
	if !bytes.Equal(got, raw) {
		t.Fatalf("got %v, want %v", got, raw)
	}
	// It has to be a copy: the caller keeps these bytes after the order buffer
	// has been reused.
	got[0] = 99
	if raw[0] == 99 {
		t.Error("the stored bitmap was aliased rather than copied")
	}
}

func TestCachePixelsRejectsImpossibleShape(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		bpp  int
		raw  []byte
	}{
		{"zero width", 0, 4, 32, []byte{1, 2, 3, 4}},
		{"negative height", 4, -1, 32, []byte{1, 2, 3, 4}},
		{"bpp is not a whole byte", 2, 2, 15, []byte{1, 2, 3, 4}},
		{"too short for its size", 4, 4, 32, []byte{1, 2, 3, 4}},
		{"no data at all", 4, 4, 32, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cachePixels(c.raw, c.w, c.h, c.bpp, false); got != nil {
				t.Errorf("got %d bytes, want nil", len(got))
			}
		})
	}
}

// The compressed path is deliberately not unit tested here: it is the same
// decoder bitmap updates use, and its own tests cover it. What is worth
// recording is that it is not safe on malformed input, see the note in
// docs/windows-verification.md.

// The captured order below is one Cache Bitmap Revision 2 order taken off a real
// Windows server, exactly as it arrived after the order header.
//
// It is here because the fields in this order are not fixed width: bitmapWidth,
// bitmapHeight and cacheIndex use the compact two byte form and bitmapLength the
// compact four byte form (MS-RDPEGDI). Reading them as fixed width shifts
// everything after them, and the symptom is not an obviously broken parse but
// one wrong byte at the front of the bitmap, which decodes to nothing at all.
// That is precisely what happened, and hand written test bytes had the same
// mistake as the parser.
func TestCacheBitmapV2RealWindowsOrder(t *testing.T) {
	body, err := os.ReadFile("testdata/cache_v2_order.bin")
	if err != nil {
		t.Skipf("missing testdata/cache_v2_order.bin: %v", err)
	}
	if len(body) != 522 {
		t.Fatalf("fixture is %d bytes, expected 522", len(body))
	}

	// From the order header: cacheId 2, 32bpp, and the flags
	// HEIGHT_SAME_AS_WIDTH, NO_BITMAP_COMPRESSION_HDR and DO_NOT_CACHE set.
	const flags = 0x0cb2
	var sec Secondary
	sec.updateCacheBitmapV2Order(bytes.NewReader(body), true, flags)

	cb := sec.CacheBitmap
	if cb == nil {
		t.Fatal("the captured order produced no cache bitmap")
	}
	if cb.Width != 64 || cb.Height != 64 || cb.Bpp != 32 {
		t.Fatalf("geometry is %dx%d at %d bpp, want 64x64 at 32", cb.Width, cb.Height, cb.Bpp)
	}
	if cb.CacheIndex != 0x7FFF {
		// DO_NOT_CACHE maps the index to the waiting list.
		t.Errorf("cache index is %d, want 0x7FFF", cb.CacheIndex)
	}
	if len(cb.Pixels) != 64*64*4 {
		t.Fatalf("decoded %d bytes, want %d", len(cb.Pixels), 64*64*4)
	}

	// A checksum pins the pixels down: a one byte shift in the fields above
	// produces a different image, and "it decoded to something" would not
	// notice.
	nonzero := 0
	h := fnv.New32a()
	h.Write(cb.Pixels)
	for _, v := range cb.Pixels {
		if v != 0 {
			nonzero++
		}
	}
	// The checksum pins the exact pixels. A field read one byte short shifts the
	// bitmap and changes them, which is the failure this fixture exists for, and
	// "it decoded to something" would not see it.
	if nonzero != 8192 {
		t.Errorf("decoded bitmap has %d non-zero bytes, want 8192", nonzero)
	}
	if got, want := h.Sum32(), uint32(0xe3ef1dc5); got != want {
		t.Errorf("decoded bitmap checksum is %#08x, want %#08x", got, want)
	}
}

// With TS_DELTA_COORDINATES, the fields an order leaves out repeat the values
// from the previous order of the same type. They are not zero, and a parser that
// treats them as zero does not fail: it looks in the wrong place. A MEMBLT that
// omits its cache id reads cache 0 and finds nothing there, which is a black
// screen rather than an error.
func TestOrderDeltaCoordinatesCarryFieldsForward(t *testing.T) {
	// Two MEMBLTs in one batch. The first states its cache and size, the second
	// only moves along, so everything else has to come from the first.
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16(2) // numberOrders

	// First order: new type, cacheId/width/height/cacheIndex present, absolute
	// coordinates so the values are two bytes each.
	b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	b.WriteByte(ORDER_TYPE_MEMBLT)
	writeU16(0x0001 | 0x0008 | 0x0010 | 0x0100)
	b.WriteByte(1) // cacheId
	b.WriteByte(0) // colour table
	writeU16(64)   // width
	writeU16(64)   // height
	writeU16(7)    // cache index

	// Second order: same type, delta coordinates, only the left coordinate.
	b.WriteByte(TS_STANDARD | TS_DELTA_COORDINATES)
	writeU16(0x0002)
	b.WriteByte(64) // left, as a signed change

	state := NewOrderState()
	var pdu FastPathOrdersPDU
	pdu.State = state
	if err := pdu.Unpack(bytes.NewReader(b.Bytes())); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(pdu.OrderPdus) != 2 {
		t.Fatalf("parsed %d orders, want 2", len(pdu.OrderPdus))
	}

	first, ok := pdu.OrderPdus[0].Primary.Data.(*Memblt)
	if !ok {
		t.Fatalf("first order is %T, want a MEMBLT", pdu.OrderPdus[0].Primary.Data)
	}
	if first.CacheId != 1 || first.Cx != 64 || first.Cy != 64 || first.CacheIdx != 7 {
		t.Fatalf("first order is %+v", first)
	}

	second, ok := pdu.OrderPdus[1].Primary.Data.(*Memblt)
	if !ok {
		t.Fatalf("second order is %T, want a MEMBLT", pdu.OrderPdus[1].Primary.Data)
	}
	if second.CacheId != first.CacheId {
		t.Errorf("cache id is %d, want %d from the previous order", second.CacheId, first.CacheId)
	}
	if second.Cx != first.Cx || second.Cy != first.Cy {
		t.Errorf("size is %dx%d, want %dx%d from the previous order", second.Cx, second.Cy, first.Cx, first.Cy)
	}
	if second.CacheIdx != first.CacheIdx {
		t.Errorf("cache index is %d, want %d from the previous order", second.CacheIdx, first.CacheIdx)
	}
	if second.X != 64 {
		t.Errorf("left coordinate is %d, want 64", second.X)
	}
}

// Two connections must not share parsing state: one connection's orders would
// otherwise fill in the gaps in the other's.
func TestOrderStateIsPerConnection(t *testing.T) {
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }

	// One order that states a cache id and nothing else that matters.
	writeU16(1)
	b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	b.WriteByte(ORDER_TYPE_MEMBLT)
	writeU16(0x0001)
	b.WriteByte(3) // cacheId
	b.WriteByte(0) // colour table

	other := NewOrderState()
	var pdu FastPathOrdersPDU
	pdu.State = other
	if err := pdu.Unpack(bytes.NewReader(b.Bytes())); err != nil {
		t.Fatalf("unpack: %v", err)
	}

	// A different connection must start from nothing.
	if len(other.delta) != 1 {
		t.Errorf("the parsing connection holds %d delta entries, want 1", len(other.delta))
	}
	fresh := NewOrderState()
	if len(fresh.delta) != 0 {
		t.Errorf("a new connection starts with %d delta entries, want 0", len(fresh.delta))
	}
}

// The carry over of absent fields is not tied to TS_DELTA_COORDINATES.
//
// That flag only says the coordinates that are present are relative to the last
// one; which fields are present at all is the field flags' business. Gating the
// carry over on the delta flag meant that an order which merely moved along kept
// nothing from its predecessor: its cache id and its size came back as zero, so
// it drew nothing, and a session of 1300 batches produced one frame. This test
// is that case: no delta flag, and fields left out all the same.
func TestOrderCarryOverWithoutDeltaCoordinates(t *testing.T) {
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16(2)

	// First order states its cache, size and index.
	b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	b.WriteByte(ORDER_TYPE_MEMBLT)
	writeU16(0x0001 | 0x0008 | 0x0010 | 0x0100)
	b.WriteByte(1) // cacheId
	b.WriteByte(0) // colour table
	writeU16(64)
	writeU16(64)
	writeU16(7)

	// Second order moves along with absolute coordinates and says nothing else.
	// No TS_DELTA_COORDINATES here on purpose.
	b.WriteByte(TS_STANDARD)
	writeU16(0x0002)
	writeU16(64) // left, absolute

	// One state for the batch, as a connection supplies. Without it each order
	// would be parsed against a fresh state and nothing could carry over.
	state := NewOrderState()
	var pdu FastPathOrdersPDU
	pdu.State = state
	if err := pdu.Unpack(bytes.NewReader(b.Bytes())); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(pdu.OrderPdus) != 2 {
		t.Fatalf("parsed %d orders, want 2", len(pdu.OrderPdus))
	}
	second, ok := pdu.OrderPdus[1].Primary.Data.(*Memblt)
	if !ok {
		t.Fatalf("second order is %T", pdu.OrderPdus[1].Primary.Data)
	}
	if second.CacheId != 1 {
		t.Errorf("cache id is %d, want 1 from the previous order", second.CacheId)
	}
	if second.Cx != 64 || second.Cy != 64 {
		t.Errorf("size is %dx%d, want 64x64 from the previous order", second.Cx, second.Cy)
	}
	if second.CacheIdx != 7 {
		t.Errorf("cache index is %d, want 7 from the previous order", second.CacheIdx)
	}
	if second.X != 64 {
		t.Errorf("left coordinate is %d, want 64", second.X)
	}
}

// An order that cannot be parsed must not be parsed as nothing. Reading no
// fields leaves the stream out of step for every order after it in the same
// batch, so the batch has to fail instead.
func TestUnparsableOrderIsRejected(t *testing.T) {
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16(1)
	// A TEXT2 order, whose rendering needs a glyph cache this does not have.
	b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	b.WriteByte(ORDER_TYPE_TEXT2)
	writeU16(0x0001)

	var pdu FastPathOrdersPDU
	if err := pdu.Unpack(bytes.NewReader(b.Bytes())); err == nil {
		t.Error("expected an error rather than a silent misread")
	}
}

// The point list a polygon or polyline carries is deltas from the last point,
// with a bitmap saying which of them are zero and therefore absent.
func TestReadDeltaPoints(t *testing.T) {
	// Three points after the start. The zero bitmap is two bits per point, and
	// 0x00 means neither coordinate of any of them is omitted.
	// A one byte delta is a seven bit twos complement value: bit 6 is the sign,
	// and bit 7 instead means a second byte follows. So -1 is 0x7f, not 0x41.
	raw := []byte{
		0x00,       // zero bitmap for three points (one byte)
		0x02, 0x03, // +2, +3
		0x01, 0x7f, // +1, -1
		0x00, 0x02, // +0, +2
	}
	got := readDeltaPoints(bytes.NewReader(raw), 3, Point{X: 10, Y: 20})
	want := []Point{{X: 10, Y: 20}, {X: 12, Y: 23}, {X: 13, Y: 22}, {X: 13, Y: 24}}
	if len(got) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d is %+v, want %+v", i, got[i], want[i])
		}
	}

	// A set flag means that coordinate is zero and was not sent.
	raw = []byte{0x80, 0x05} // first point: x omitted, y is +5
	got = readDeltaPoints(bytes.NewReader(raw), 1, Point{X: 7, Y: 7})
	if got[1] != (Point{X: 7, Y: 12}) {
		t.Errorf("got %+v, want the x to be unchanged and the y advanced", got[1])
	}
}

// secondaryBatch frames one secondary order, which is the shape a server sends
// them in: a count, then the control flags, the order length, the extra flags,
// the order type and the body.
func secondaryBatch(orderType uint8, flags uint16, body []byte) []byte {
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16(1) // numberOrders
	b.WriteByte(TS_STANDARD | TS_SECONDARY)
	writeU16(uint16(7 + len(body))) // order length includes its own header
	writeU16(flags)
	b.WriteByte(orderType)
	b.Write(body)
	return b.Bytes()
}

// A colour table of the mandated 256 colours used to write four bytes from each
// offset, which ran one past the end of the array on the last colour. Three
// bytes of input were enough to panic.
func TestCacheColorTableDoesNotOverrun(t *testing.T) {
	// A colour count of 256 and then nothing: the parser must stop, not read on.
	body := []byte{0x00, 0x00, 0x01}
	batch := secondaryBatch(ORDER_TYPE_CACHE_COLOR_TABLE, 0, body)

	var pdu FastPathOrdersPDU
	_ = pdu.Unpack(bytes.NewReader(batch))
}

// A monochrome brush wrote into a slice that was never allocated, so the first
// assignment indexed a nil slice.
func TestCacheBrushMonochromeDoesNotPanic(t *testing.T) {
	// index, bpp 1, cx 8, cy 8, style, length 8, then the pattern.
	body := []byte{0x00, 0x01, 0x08, 0x08, 0x00, 0x08, 0x00}
	batch := secondaryBatch(ORDER_TYPE_CACHE_BRUSH, 0, body)

	var pdu FastPathOrdersPDU
	_ = pdu.Unpack(bytes.NewReader(batch))
}

// A compressed brush whose declared length is longer than the bytes that follow
// used to be decoded from a shortened slice, and a twelve byte order was enough
// to run off the end of the palette.
func TestTruncatedCompressedBrushDoesNotPanic(t *testing.T) {
	// index, bpp 3, cx 8, cy 8, style, length 20, then a single byte.
	body := []byte{0x00, 0x03, 0x08, 0x08, 0x00, 0x14, 0x30}
	batch := secondaryBatch(ORDER_TYPE_CACHE_BRUSH, 0, body)

	var pdu FastPathOrdersPDU
	if err := pdu.Unpack(bytes.NewReader(batch)); err != nil {
		t.Fatalf("a short brush should be dropped without failing the batch: %v", err)
	}
}

// A cache order may declare a length far larger than any bitmap cell, and
// core.ReadBytes allocates before it reads, so the length has to be refused
// rather than reserved.
func TestAbsurdCacheBitmapLengthIsRefused(t *testing.T) {
	// Cache id 2, 32bpp, height-same-as-width and no compression header, a width
	// of 64, a length of 0x3fffffff, then nothing at all.
	body := []byte{0x40, 0xff, 0xff, 0xff, 0x3f, 0x00}
	batch := secondaryBatch(ORDER_TYPE_BITMAP_COMPRESSED_V2, 0x0cb2, body)

	var pdu FastPathOrdersPDU
	_ = pdu.Unpack(bytes.NewReader(batch))
}

// An order whose parser reads the wrong number of bytes does not fail. It shifts
// the stream, so the next order in the batch is read from the wrong offset and
// gets nonsense. That is how a missing cbData byte, a missing brush and a colour
// read one byte too long all presented, and none of them was visible to a test
// that built the order's struct directly.
//
// So this puts a MEMBLT with known values after an order of another type and
// requires the MEMBLT to arrive intact. Anything the order before it leaves
// unread, or reads past, shows up as a MEMBLT with the wrong cache or size.
func TestOrderLengthsDoNotShiftTheNextOrder(t *testing.T) {
	// The sentinel: cache 3, index 77, 32x16 at (5,6) from (0,0).
	sentinel := func() []byte {
		var b bytes.Buffer
		writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
		b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
		b.WriteByte(ORDER_TYPE_MEMBLT)
		writeU16(0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0100)
		b.WriteByte(3) // cacheId
		b.WriteByte(0) // colour table
		writeU16(5)    // x
		writeU16(6)    // y
		writeU16(32)   // width
		writeU16(16)   // height
		writeU16(77)   // cacheIndex
		return b.Bytes()
	}

	cases := []struct {
		name      string
		orderType uint8
		body      []byte
	}{
		{
			// fields 1,2,6 and 7: x, y, numPoints, cbData and the point list.
			name: "polygon sc", orderType: ORDER_TYPE_POLYGON_SC,
			body: []byte{
				0x63, // field flags: x, y, numPoints, points. One byte, because
				// POLYGON_SC carries a single flag byte.
				0x40, 0x00, // x = 64
				0x40, 0x00, // y = 64
				0x01,       // numPoints
				0x00,       // cbData, which the parser used to skip
				0x00,       // one point: its zero bit bitmap
				0x01, 0x01, // the point's deltas
			},
		},
		{
			// fields 1,2,6,7 again, on the polyline.
			name: "polyline", orderType: ORDER_TYPE_POLYLINE,
			body: []byte{
				0x63, // one flag byte, as above
				0x40, 0x00,
				0x40, 0x00,
				0x01,
				0x00,       // cbData
				0x00,       // zero bit bitmap
				0x01, 0x01, // the point's deltas
			},
		},
		{
			// fields 1,2,5 then 12 and 13: a polygon in a colour brush, with no
			// brush of its own, which is the case the parser skipped a field on.
			name: "polygon cb", orderType: ORDER_TYPE_POLYGON_CB,
			body: []byte{
				0x13, 0x18, // field flags: x, y, fore colour, numPoints, points
				0x40, 0x00,
				0x40, 0x00,
				0x00, 0x00, 0x00, // the fore colour, three bytes
				0x01,       // numPoints
				0x00,       // cbData
				0x00,       // zero bit bitmap
				0x01, 0x01, // the point's deltas
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			b.Write([]byte{0x02, 0x00}) // two orders
			b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
			b.WriteByte(c.orderType)
			b.Write(c.body)
			b.Write(sentinel())

			var pdu FastPathOrdersPDU
			if err := pdu.Unpack(bytes.NewReader(b.Bytes())); err != nil {
				t.Fatalf("unpack: %v", err)
			}
			if len(pdu.OrderPdus) != 2 {
				t.Fatalf("parsed %d orders, want 2", len(pdu.OrderPdus))
			}
			if pdu.OrderPdus[1].Primary == nil {
				t.Fatalf("the second order came out as type %d with no primary payload, so the %s order before it consumed the wrong number of bytes",
					pdu.OrderPdus[1].Type, c.name)
			}
			m, ok := pdu.OrderPdus[1].Primary.Data.(*Memblt)
			if !ok {
				t.Fatalf("the second order is %T, want a MEMBLT: the %s order before it consumed the wrong number of bytes",
					pdu.OrderPdus[1].Primary.Data, c.name)
			}
			if m.CacheId != 3 || m.CacheIdx != 77 || m.Cx != 32 || m.Cy != 16 || m.X != 5 || m.Y != 6 {
				t.Errorf("the MEMBLT after the %s order is %+v, so that order consumed the wrong number of bytes",
					c.name, m)
			}
		})
	}
}
