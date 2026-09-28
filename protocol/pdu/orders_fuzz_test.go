package pdu

import (
	"bytes"
	"os"
	"testing"
)

// Order batches arrive from the server, so FastPathOrdersPDU.Unpack and every
// order parser behind it are fed bytes this project did not choose. The two
// decoder defects already found in this tree were a decoder that never returned
// and one that read the wrong byte; this target is for the first kind, applied
// to every order parser on the network path.
//
// It follows the model of core/rle_test.go and codec/zgfx_test.go: seeds that
// are real or realistically shaped, and properties that have to hold for every
// input rather than expected outputs.

// secondaryOrderBatch builds the batch and secondary order headers that carried
// the Cache Bitmap Revision 2 body captured from a real Windows server
// (testdata/cache_v2_order.bin), which is the body on its own.
//
// orderLength is the body length less the seven header bytes FreeRDP accounts
// for: it adds 13 to the order length for the whole order and has already read
// the six byte order header, so the body that follows is orderLength+7. That is
// what this parser reads too, which is why the length is derived rather than
// guessed.
func secondaryOrderBatch(body []byte, orderType uint8, flags uint16) []byte {
	var b bytes.Buffer
	b.Write([]byte{1, 0}) // numberOrders
	b.WriteByte(TS_STANDARD | TS_SECONDARY)
	length := uint16(len(body) - 7)
	b.Write([]byte{byte(length), byte(length >> 8)})
	b.Write([]byte{byte(flags), byte(flags >> 8)})
	b.WriteByte(orderType)
	b.Write(body)
	return b.Bytes()
}

// fuzzOrderSeeds returns the seed corpus: the captured order, plus hand-built
// batches that reach each of the three order families so the mutator has a
// foothold in every switch arm.
func fuzzOrderSeeds() [][]byte {
	var seeds [][]byte

	if body, err := os.ReadFile("testdata/cache_v2_order.bin"); err == nil {
		// cacheId 2, 32bpp, HEIGHT_SAME_AS_WIDTH | NO_BITMAP_COMPRESSION_HDR
		// | DO_NOT_CACHE, taken from the capture.
		seeds = append(seeds, secondaryOrderBatch(body, ORDER_TYPE_BITMAP_COMPRESSED_V2, 0x0cb2))
	}

	// A primary MEMBLT that states its cache, size and cache index.
	{
		var b bytes.Buffer
		b.Write([]byte{1, 0}) // numberOrders
		b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
		b.WriteByte(ORDER_TYPE_MEMBLT)
		// cacheId|colourTable, width, height, cacheIndex present.
		b.Write([]byte{0x19, 0x01})
		b.WriteByte(1)         // cacheId
		b.WriteByte(0)         // colour table
		b.Write([]byte{64, 0}) // width
		b.Write([]byte{64, 0}) // height
		b.Write([]byte{7, 0})  // cache index
		seeds = append(seeds, b.Bytes())
	}

	// An uncompressed Cache Bitmap Revision 2 that fills one small cache slot.
	{
		var body bytes.Buffer
		body.WriteByte(2)         // bitmapWidth, compact
		body.WriteByte(2)         // bitmapHeight, compact
		body.WriteByte(2 * 2 * 4) // bitmapLength, compact: two pixels of 32bpp
		body.WriteByte(0)         // cacheIndex, compact
		body.Write(make([]byte, 2*2*4))
		// cacheId 1, 32bpp, no revision 2 flags.
		seeds = append(seeds, secondaryOrderBatch(body.Bytes(), ORDER_TYPE_BITMAP_UNCOMPRESSED_V2, 0x31))
	}

	// A cache brush: 8x8, 8bpp, the compressed form. The brush decompressor is
	// the only order parser here that walks a palette, so it is worth a
	// foothold. The format and length are the pair FreeRDP treats as an 8bpp
	// compressed brush (iBitmapFormat 3, length 20).
	{
		var body bytes.Buffer
		body.WriteByte(0)  // cache index
		body.WriteByte(3)  // iBitmapFormat: 8bpp
		body.WriteByte(8)  // width
		body.WriteByte(8)  // height
		body.WriteByte(0)  // style
		body.WriteByte(20) // length
		body.Write(make([]byte, 20))
		seeds = append(seeds, secondaryOrderBatch(body.Bytes(), ORDER_TYPE_CACHE_BRUSH, 0))
	}

	// An alternate secondary order. TS_STANDARD clear makes it one, and the top
	// six bits are the order type; a frame marker carries a four byte field.
	seeds = append(seeds, []byte{
		1, 0,
		byte(ORDER_TYPE_FRAME_MARKER) << 2,
		0, 0, 0, 0,
	})

	// Degenerate headers, which used to be where an off-by-one hid.
	seeds = append(seeds, []byte{})
	seeds = append(seeds, []byte{0, 0})
	seeds = append(seeds, []byte{1, 0})
	seeds = append(seeds, []byte{0xff, 0xff})

	return seeds
}

// TestCapturedOrderSeedParses keeps the captured seed honest: the fixture is an
// order body, and if the headers rebuilt around it were wrong the seed would
// still "parse" as a rejection and the fuzzer would lose the one input that is
// known to have come off a real server.
func TestCapturedOrderSeedParses(t *testing.T) {
	body, err := os.ReadFile("testdata/cache_v2_order.bin")
	if err != nil {
		t.Skipf("missing testdata/cache_v2_order.bin: %v", err)
	}

	var pdu FastPathOrdersPDU
	pdu.State = NewOrderState()
	if err := pdu.Unpack(bytes.NewReader(
		secondaryOrderBatch(body, ORDER_TYPE_BITMAP_COMPRESSED_V2, 0x0cb2))); err != nil {
		t.Fatalf("the captured order did not parse: %v", err)
	}
	if len(pdu.OrderPdus) != 1 {
		t.Fatalf("parsed %d orders, want 1", len(pdu.OrderPdus))
	}
	o := pdu.OrderPdus[0]
	if o.Type != ORDER_SECONDARY || o.Secondary == nil || o.Secondary.CacheBitmap == nil {
		t.Fatalf("captured order parsed as %+v, want a secondary cache bitmap", o)
	}
	if cb := o.Secondary.CacheBitmap; cb.Width != 64 || cb.Height != 64 || len(cb.Pixels) != 64*64*4 {
		t.Fatalf("captured bitmap is %dx%d with %d bytes, want 64x64 with 16384",
			cb.Width, cb.Height, len(cb.Pixels))
	}
}

// FuzzFastPathOrdersPDU checks that a batch chosen by the server cannot make the
// parser panic, hang or run off the end, and that a batch it accepts is
// internally consistent.
//
// Consistency is the property the order header makes easy to lose: an order
// whose parser reads nothing has an unknown length, so the rest of the batch is
// then read as nonsense. A batch that returns success with an order whose
// payload does not match its Type is that same failure arriving silently.
func FuzzFastPathOrdersPDU(f *testing.F) {
	for _, seed := range fuzzOrderSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// One state per connection, as a real batch is given.
		var pdu FastPathOrdersPDU
		pdu.State = NewOrderState()

		err := pdu.Unpack(bytes.NewReader(data))

		// Whatever was parsed before a failure is still handed to a renderer, so
		// it has to be consistent either way.
		checkOrderTypes(t, &pdu)

		if err != nil {
			return
		}
		// Only a complete batch claims to hold all of its orders.
		if got, want := len(pdu.OrderPdus), int(pdu.NumberOrders); got != want {
			t.Fatalf("unpacked without error but holds %d orders, header claims %d", got, want)
		}
	})
}

// checkOrderTypes asserts that every parsed order carries exactly the payload
// its Type names, which is what a renderer dispatches on.
func checkOrderTypes(t *testing.T, pdu *FastPathOrdersPDU) {
	t.Helper()
	for i := range pdu.OrderPdus {
		o := &pdu.OrderPdus[i]
		switch o.Type {
		case ORDER_PRIMARY:
			if o.Primary == nil {
				t.Fatalf("order %d is ORDER_PRIMARY but carries no Primary", i)
			}
			if o.Secondary != nil {
				t.Fatalf("order %d is ORDER_PRIMARY but carries a Secondary", i)
			}
			if o.Primary.Data == nil {
				t.Fatalf("order %d is ORDER_PRIMARY but its Primary has no data", i)
			}
		case ORDER_SECONDARY:
			if o.Secondary == nil {
				t.Fatalf("order %d is ORDER_SECONDARY but carries no Secondary", i)
			}
			if o.Primary != nil {
				t.Fatalf("order %d is ORDER_SECONDARY but carries a Primary", i)
			}
		case ORDER_ALTSEC:
			if o.Primary != nil || o.Secondary != nil {
				t.Fatalf("order %d is ORDER_ALTSEC but carries a payload", i)
			}
		default:
			t.Fatalf("order %d has type %d, which is not one of the three", i, o.Type)
		}
	}
}
