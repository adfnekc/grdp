package pdu

import (
	"bytes"
	"testing"
)

// A frame marker arriving as an alternate secondary order used to be read and
// thrown away, so a session that draws with orders had no way to tell where its
// frames began. The field is four bytes and FreeRDP reads exactly that much
// (update_read_frame_marker_order), so the length was never wrong; only the value
// was lost.
func TestAltSecFrameMarkerIsKept(t *testing.T) {
	o := &OrderPdu{ControlFlags: uint8(ORDER_TYPE_FRAME_MARKER << 2)}
	r := bytes.NewReader([]byte{0x0B, 0x00, 0x00, 0x00})
	if err := o.processAltsecOrder(r); err != nil {
		t.Fatalf("processAltsecOrder: %v", err)
	}
	if o.Altsec == nil || !o.Altsec.FrameMarker {
		t.Fatal("the frame marker was dropped")
	}
	if o.Altsec.FrameMarkerAction != 0x0B {
		t.Errorf("action = %#x, want 0x0b", o.Altsec.FrameMarkerAction)
	}
	// Four bytes, no more and no less: an order that reads the wrong length does
	// not fail, it leaves everything after it out of step.
	if r.Len() != 0 {
		t.Errorf("%d bytes left unread", r.Len())
	}
}

// Another alternate secondary order must not be reported as a frame marker, or a
// caller counting frames would count things that are not frames.
func TestAltSecOtherOrderIsNotAFrameMarker(t *testing.T) {
	o := &OrderPdu{ControlFlags: uint8(ORDER_TYPE_SWITCH_SURFACE << 2)}
	if err := o.processAltsecOrder(bytes.NewReader(nil)); err != nil {
		t.Fatalf("processAltsecOrder: %v", err)
	}
	if o.Altsec != nil && o.Altsec.FrameMarker {
		t.Error("a switch surface order was reported as a frame marker")
	}
}

// The marker has to reach a caller, in one shape whichever form it arrived in.
func TestFrameMarkerFromOrdersIsEmitted(t *testing.T) {
	c := NewClient(newCaptureTransport())
	var got []FrameMarker
	c.On("frame-marker", func(data interface{}) {
		got = append(got, data.(FrameMarker))
	})

	c.emitOrders([]OrderPdu{
		{},
		{Altsec: &Altsec{FrameMarker: true, FrameMarkerAction: 3}},
		{Altsec: &Altsec{}},
	})

	if len(got) != 1 {
		t.Fatalf("got %d frame markers, want 1", len(got))
	}
	if got[0].Action != 3 || !got[0].FromOrder {
		t.Errorf("marker = %+v, want action 3 from an order", got[0])
	}
	if got[0].FrameID != 0 {
		t.Errorf("an order carries no frame id, got %d", got[0].FrameID)
	}
}

// The surface command form carries an action and a frame id, and arrives in the
// same shape so that a caller does not have to care which form the server chose.
func TestFrameMarkerFromSurfaceCommandsIsEmitted(t *testing.T) {
	c := NewClient(newCaptureTransport())
	var got []FrameMarker
	c.On("frame-marker", func(data interface{}) {
		got = append(got, data.(FrameMarker))
	})

	c.emitSurfaceCommands([]SurfaceCommand{
		{Type: CMDTYPE_FRAME_MARKER, Marker: &FrameMarkerCommand{FrameAction: 2, FrameID: 7}},
	})

	if len(got) != 1 {
		t.Fatalf("got %d frame markers, want 1", len(got))
	}
	if got[0].Action != 2 || got[0].FrameID != 7 || got[0].FromOrder {
		t.Errorf("marker = %+v, want action 2, id 7, from a surface command", got[0])
	}
}

// A slow path surface command update used to be rejected outright: the update
// reader had no case for the type, so it returned "Unsupport slow update type
// 0x4" and the whole data PDU was lost, frame markers included. This builds the
// bytes and walks the whole receive path.
func TestSlowPathSurfaceCommandFrameMarker(t *testing.T) {
	c := NewClient(newCaptureTransport())
	var got []FrameMarker
	c.On("frame-marker", func(data interface{}) {
		got = append(got, data.(FrameMarker))
	})

	// The update body: the update type, then one frame marker command.
	body := &bytes.Buffer{}
	put16 := func(v uint16) { body.Write([]byte{byte(v), byte(v >> 8)}) }
	put32 := func(v uint32) { body.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}) }
	put16(FASTPATH_UPDATETYPE_SURFCMDS)
	put16(CMDTYPE_FRAME_MARKER)
	put16(2) // frame action
	put32(9) // frame id

	// The share data header, whose uncompressed length counts its own four byte
	// tail, then the body.
	put32fn := func(b *bytes.Buffer, v uint32) { b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}) }
	hdr := &bytes.Buffer{}
	put32fn(hdr, c.sharedId)
	hdr.WriteByte(0)
	hdr.WriteByte(STREAM_LOW)
	put16fn := func(b *bytes.Buffer, v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
	put16fn(hdr, uint16(body.Len()+4))
	hdr.WriteByte(PDUTYPE2_UPDATE)
	hdr.WriteByte(0)
	put16fn(hdr, 0)

	// The share control header, whose length counts everything.
	payload := append(hdr.Bytes(), body.Bytes()...)
	ctrl := &bytes.Buffer{}
	put16fn(ctrl, uint16(len(payload)+6))
	put16fn(ctrl, PDUTYPE_DATAPDU)
	put16fn(ctrl, c.userId)

	c.recvPDU(append(ctrl.Bytes(), payload...))

	if len(got) != 1 {
		t.Fatalf("got %d frame markers, want 1", len(got))
	}
	if got[0].Action != 2 || got[0].FrameID != 9 {
		t.Errorf("marker = %+v, want action 2, id 9", got[0])
	}
}
