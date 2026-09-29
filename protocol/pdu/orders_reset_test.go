package pdu

import (
	"bytes"
	"testing"
)

// A cache bitmap order declares its bitmap length in two bytes, and when a
// compression header is present its eight bytes are part of that length. A
// length below eight therefore describes no bitmap at all; the subtraction used
// to wrap around a uint16 and ask for nearly 64 KiB. The order has to be refused
// instead, which fails the batch, because its remaining fields then have no
// length this parser knows.
func TestCacheBitmapLengthBelowCompressionHeaderIsRefused(t *testing.T) {
	// cacheId, pad, width, height, bpp, then a declared length of 4 with the
	// eight byte compression header expected after it (the flags leave
	// NO_BITMAP_COMPRESSION_HDR clear).
	body := []byte{1, 0, 2, 2, 32, 4, 0, 0, 0}
	batch := secondaryBatch(ORDER_TYPE_CACHE_BITMAP_COMPRESSED, 0, body)

	var pdu FastPathOrdersPDU
	if err := pdu.Unpack(bytes.NewReader(batch)); err == nil {
		t.Fatal("a bitmap length below the compression header was accepted")
	}
}

// Eight is the smallest length that can hold the compression header and an
// empty bitmap, so the bound must not refuse it. This is the boundary the
// hostile case above sits just below.
func TestCacheBitmapLengthEqualToCompressionHeaderIsAccepted(t *testing.T) {
	body := []byte{1, 0, 2, 2, 32, 8, 0, 0, 0}
	body = append(body, make([]byte, 8)...) // the compression header
	batch := secondaryBatch(ORDER_TYPE_CACHE_BITMAP_COMPRESSED, 0, body)

	var pdu FastPathOrdersPDU
	if err := pdu.Unpack(bytes.NewReader(batch)); err != nil {
		t.Fatalf("a length of exactly the compression header was refused: %v", err)
	}
}

// The fields an order leaves out repeat the previous order's, but only within
// one session. A server that deactivates and starts a new demand active is
// starting over, so OrderState.Reset drops the carry over and the next order
// reads its own zero values rather than the dead session's.
func TestOrderStateResetDropsCarryOver(t *testing.T) {
	// A batch whose first MEMBLT states its cache and size and whose second
	// repeats them without saying so.
	var first bytes.Buffer
	writeU16 := func(v uint16) { first.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16(2)
	first.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	first.WriteByte(ORDER_TYPE_MEMBLT)
	writeU16(0x0001 | 0x0008 | 0x0010 | 0x0100)
	first.WriteByte(1) // cacheId
	first.WriteByte(0) // colour table
	writeU16(64)
	writeU16(64)
	writeU16(7)
	first.WriteByte(TS_STANDARD)
	writeU16(0x0002)
	writeU16(64) // left, absolute

	state := NewOrderState()
	var pdu FastPathOrdersPDU
	pdu.State = state
	if err := pdu.Unpack(bytes.NewReader(first.Bytes())); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if _, ok := state.delta[ORDER_TYPE_MEMBLT]; !ok {
		t.Fatal("no MEMBLT carry over was recorded to begin with")
	}

	state.Reset()
	if len(state.delta) != 0 {
		t.Fatalf("reset left %d carry over entries", len(state.delta))
	}

	// The next order states only where it goes; nothing may carry over from
	// before the reset.
	var second bytes.Buffer
	writeU16b := func(v uint16) { second.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16b(1)
	second.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	second.WriteByte(ORDER_TYPE_MEMBLT)
	writeU16b(0x0002)
	writeU16b(32) // left

	var after FastPathOrdersPDU
	after.State = state
	if err := after.Unpack(bytes.NewReader(second.Bytes())); err != nil {
		t.Fatalf("unpack after reset: %v", err)
	}
	o, ok := after.OrderPdus[0].Primary.Data.(*Memblt)
	if !ok {
		t.Fatalf("order is %T, want a MEMBLT", after.OrderPdus[0].Primary.Data)
	}
	if o.CacheId != 0 || o.Cx != 0 || o.Cy != 0 || o.CacheIdx != 0 {
		t.Errorf("after reset the order carries %+v, want zero values", o)
	}
	if o.X != 32 {
		t.Errorf("left coordinate is %d, want 32", o.X)
	}
}

// A deactivate-all is where the PDU layer sees the server starting a new
// session, and it is where the per connection order state has to be dropped.
// Without it the orders after the reconnect repeat fields from the session
// before it.
func TestDeactivateAllResetsOrderState(t *testing.T) {
	c := NewClient(newCaptureTransport())

	// Record a carry over entry the way a batch parsed on this connection
	// would, then deliver the deactivate-all the transport would.
	var b bytes.Buffer
	writeU16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	writeU16(1)
	b.WriteByte(TS_STANDARD | TS_TYPE_CHANGE)
	b.WriteByte(ORDER_TYPE_MEMBLT)
	writeU16(0x0001)
	b.WriteByte(3) // cacheId
	b.WriteByte(0) // colour table

	var batch FastPathOrdersPDU
	batch.State = c.orders
	if err := batch.Unpack(bytes.NewReader(b.Bytes())); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(c.orders.delta) != 1 {
		t.Fatalf("carry over has %d entries, want 1", len(c.orders.delta))
	}

	c.recvPDU(NewPDU(c.userId, &DeactiveAllPDU{}).serialize())

	if len(c.orders.delta) != 0 {
		t.Errorf("deactivate-all left %d carry over entries, want 0", len(c.orders.delta))
	}
}
