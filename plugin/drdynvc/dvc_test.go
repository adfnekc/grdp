package drdynvc

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fakeSender captures what the client writes to the static drdynvc channel.
type fakeSender struct {
	chunks [][]byte
}

func (f *fakeSender) SendToChannel(string, []byte) (int, error) { return 0, nil }

func (f *fakeSender) SendToChannelCapture(channel string, s []byte) (int, error) {
	cp := make([]byte, len(s))
	copy(cp, s)
	f.chunks = append(f.chunks, cp)
	return len(s), nil
}

// recorder is a DynChannel that remembers the callbacks it received.
type recorder struct {
	opened   bool
	closed   bool
	channel  uint32
	payloads [][]byte
}

func (r *recorder) OnOpen(id uint32) { r.opened = true; r.channel = id }
func (r *recorder) OnData(data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	r.payloads = append(r.payloads, cp)
}
func (r *recorder) OnClose() { r.closed = true }

// capture installs a sender that records outbound chunks.
type capSender struct{ chunks [][]byte }

func (c *capSender) SendToChannel(_ string, s []byte) (int, error) {
	cp := make([]byte, len(s))
	copy(cp, s)
	c.chunks = append(c.chunks, cp)
	return len(s), nil
}

func newTestClient() (*DvcClient, *capSender) {
	s := &capSender{}
	c := NewDvcClient()
	c.Sender(s)
	return c, s
}

func buildHeader(cmd uint8, cbChID uint8, channelID uint32) []byte {
	h := header{cmd: cmd, cbChID: cbChID}
	return h.serialize(channelID)
}

func buildHeader2(cmd, sp, cbChID uint8, channelID uint32) []byte {
	h := header{cmd: cmd, sp: sp, cbChID: cbChID}
	return h.serialize(channelID)
}

func TestHeaderRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		cbChID uint8
		id     uint32
		width  int
	}{
		{0, 0x7F, 1},
		{1, 0x1234, 2},
		{2, 0xDEADBEEF, 4},
	} {
		raw := buildHeader(cmdData, tc.cbChID, tc.id)
		if len(raw) != 1+tc.width {
			t.Fatalf("cbChID %d: header is %d bytes, want %d", tc.cbChID, len(raw), 1+tc.width)
		}
		h, err := readHeader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("readHeader: %v", err)
		}
		if h.cmd != cmdData || h.cbChID != tc.cbChID {
			t.Fatalf("got cmd 0x%x cbChID %d", h.cmd, h.cbChID)
		}
		id, err := readChannelID(bytes.NewReader(raw[1:]), h.cbChID)
		if err != nil {
			t.Fatalf("readChannelID: %v", err)
		}
		if id != tc.id {
			t.Fatalf("got id 0x%x, want 0x%x", id, tc.id)
		}
	}
}

func TestCapabilitiesResponse(t *testing.T) {
	c, sent := newTestClient()

	// The server announces version 2.
	req := []byte{0x50}
	req = append(req, 0x02, 0x00, 0x00, 0x00)
	c.Process(req)

	if len(sent.chunks) != 1 {
		t.Fatalf("got %d responses, want 1", len(sent.chunks))
	}
	resp := sent.chunks[0]
	if len(resp) != 4 {
		t.Fatalf("capabilities response is %d bytes, want 4", len(resp))
	}
	// The literal 0x0050 the reference client sends: command 5, sp 0, cbChId 0.
	if binary.LittleEndian.Uint16(resp) != 0x0050 {
		t.Fatalf("got header 0x%04x, want 0x0050", binary.LittleEndian.Uint16(resp))
	}
	// The negotiated version is the lower of the two, so 2 here.
	if got := binary.LittleEndian.Uint16(resp[2:]); got != 2 {
		t.Fatalf("got version %d, want 2", got)
	}
}

func TestServerCreatesKnownChannel(t *testing.T) {
	c, sent := newTestClient()
	rec := &recorder{}
	c.Register("TestChannel", rec)

	req := append(buildHeader(cmdCreateRequest, 0, 9), []byte("TestChannel")...)
	c.Process(req)

	if !rec.opened || rec.channel != 9 {
		t.Fatalf("handler was not opened for channel 9: %+v", rec)
	}
	if len(sent.chunks) != 1 {
		t.Fatalf("got %d responses, want 1", len(sent.chunks))
	}
	resp := sent.chunks[0]
	h, _ := readHeader(bytes.NewReader(resp))
	if h.cmd != cmdCreateRequest {
		t.Fatalf("response command is 0x%x, want 0x%x", h.cmd, cmdCreateRequest)
	}
	id, _ := readChannelID(bytes.NewReader(resp[1:]), h.cbChID)
	if id != 9 {
		t.Fatalf("response channel id is %d, want 9", id)
	}
	// A successful creation reports status 0 where the request had a name.
	status := binary.LittleEndian.Uint32(resp[2:])
	if status != 0 {
		t.Fatalf("status is 0x%08x, want 0", status)
	}
}

func TestServerCreatesUnknownChannelIsRefused(t *testing.T) {
	c, sent := newTestClient()

	req := append(buildHeader(cmdCreateRequest, 0, 4), []byte("NotRegistered")...)
	c.Process(req)

	if len(sent.chunks) != 1 {
		t.Fatalf("got %d responses, want 1", len(sent.chunks))
	}
	resp := sent.chunks[0]
	status := binary.LittleEndian.Uint32(resp[2:])
	if status != 0xC0000001 {
		t.Fatalf("status is 0x%08x, want 0xC0000001", status)
	}
}

func TestDataFirstReassembly(t *testing.T) {
	c, _ := newTestClient()
	rec := &recorder{}
	c.Register("TestChannel", rec)
	c.Process(append(buildHeader(cmdCreateRequest, 0, 3), []byte("TestChannel")...))

	// A DATA_FIRST with sp 0, so the announced length is one byte: 11 bytes
	// total, of which 6 arrive here.
	first := append(buildHeader(cmdDataFirst, 1, 3), 11)
	first = append(first, []byte("hello ")...)
	c.Process(first)

	if len(rec.payloads) != 0 {
		t.Fatalf("payload delivered before it was complete: %q", rec.payloads)
	}

	// The remaining 4 bytes arrive in a plain DATA.
	c.Process(append(buildHeader(cmdData, 1, 3), []byte("world")...))

	if len(rec.payloads) != 1 {
		t.Fatalf("got %d payloads, want 1", len(rec.payloads))
	}
	if got := string(rec.payloads[0]); got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestDataFirstFourByteLength(t *testing.T) {
	c, _ := newTestClient()
	rec := &recorder{}
	c.Register("TestChannel", rec)
	c.Process(append(buildHeader(cmdCreateRequest, 0, 5), []byte("TestChannel")...))

	// sp is 2 here, so the announced length is four bytes.
	first := append(buildHeader2(cmdDataFirst, 2, 0, 5), 0x00, 0x00, 0x00, 0x00)
	binary.LittleEndian.PutUint32(first[len(first)-4:], 4)
	first = append(first, []byte("abcd")...)
	c.Process(first)

	if len(rec.payloads) != 1 || string(rec.payloads[0]) != "abcd" {
		t.Fatalf("got %q", rec.payloads)
	}
}

func TestStandaloneDataDeliversImmediately(t *testing.T) {
	c, _ := newTestClient()
	rec := &recorder{}
	c.Register("TestChannel", rec)
	c.Process(append(buildHeader(cmdCreateRequest, 0, 1), []byte("TestChannel")...))

	c.Process(append(buildHeader(cmdData, 0, 1), []byte("payload")...))

	if len(rec.payloads) != 1 || string(rec.payloads[0]) != "payload" {
		t.Fatalf("got %q", rec.payloads)
	}
	// A second standalone payload must not be appended to the first.
	c.Process(append(buildHeader(cmdData, 0, 1), []byte("second")...))
	if len(rec.payloads) != 2 || string(rec.payloads[1]) != "second" {
		t.Fatalf("got %q", rec.payloads)
	}
}

func TestDataForUnknownChannelIsIgnored(t *testing.T) {
	c, _ := newTestClient()
	// No channel created; this must not panic or deliver anything.
	c.Process(append(buildHeader(cmdData, 0, 200), []byte("payload")...))
}

func TestCloseNotifiesHandler(t *testing.T) {
	c, _ := newTestClient()
	rec := &recorder{}
	c.Register("TestChannel", rec)
	c.Process(append(buildHeader(cmdCreateRequest, 0, 2), []byte("TestChannel")...))
	c.Process(buildHeader(cmdClose, 0, 2))

	if !rec.closed {
		t.Fatal("handler was not notified of the close")
	}
}

func TestOpenSendsCreateRequest(t *testing.T) {
	c, sent := newTestClient()
	c.Register("TestChannel", &recorder{})

	id, err := c.Open("TestChannel")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(sent.chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(sent.chunks))
	}
	chunk := sent.chunks[0]
	h, _ := readHeader(bytes.NewReader(chunk))
	if h.cmd != cmdCreateRequest {
		t.Fatalf("command is 0x%x, want 0x%x", h.cmd, cmdCreateRequest)
	}
	gotID, _ := readChannelID(bytes.NewReader(chunk[1:]), h.cbChID)
	if gotID != id {
		t.Fatalf("requested id %d, returned %d", gotID, id)
	}
	if name := string(chunk[1+int(h.cbChID)+1:]); name != "TestChannel" {
		t.Fatalf("got channel name %q", name)
	}
}

func TestOpenUnregisteredChannelFails(t *testing.T) {
	c, sent := newTestClient()
	if _, err := c.Open("Missing"); err == nil {
		t.Fatal("expected an error")
	}
	if len(sent.chunks) != 0 {
		t.Fatal("nothing should have been written")
	}
}

func TestSendDataSplitsLargePayloads(t *testing.T) {
	c, sent := newTestClient()

	payload := bytes.Repeat([]byte{0xAB}, maxDvcDataSize+100)
	if err := c.SendData(1, payload); err != nil {
		t.Fatalf("SendData: %v", err)
	}
	if len(sent.chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (DATA_FIRST + one DATA)", len(sent.chunks))
	}

	first := sent.chunks[0]
	h, _ := readHeader(bytes.NewReader(first))
	if h.cmd != cmdDataFirst {
		t.Fatalf("first chunk command is 0x%x, want 0x%x", h.cmd, cmdDataFirst)
	}
	// sp is 2, so the announced length is a four byte value.
	if h.sp != 2 {
		t.Fatalf("first chunk sp is %d, want 2", h.sp)
	}
	total := binary.LittleEndian.Uint32(first[2:])
	if int(total) != len(payload) {
		t.Fatalf("announced %d bytes, want %d", total, len(payload))
	}

	second := sent.chunks[1]
	h2, _ := readHeader(bytes.NewReader(second))
	if h2.cmd != cmdData {
		t.Fatalf("second chunk command is 0x%x, want 0x%x", h2.cmd, cmdData)
	}
	if len(second) != 1+1+100 {
		t.Fatalf("second chunk is %d bytes, want %d", len(second), 102)
	}
}

func TestSendDataSinglePdu(t *testing.T) {
	c, sent := newTestClient()
	if err := c.SendData(0x42, []byte("short")); err != nil {
		t.Fatalf("SendData: %v", err)
	}
	if len(sent.chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(sent.chunks))
	}
	h, _ := readHeader(bytes.NewReader(sent.chunks[0]))
	if h.cmd != cmdData {
		t.Fatalf("command is 0x%x", h.cmd)
	}
	if got := string(sent.chunks[0][2:]); got != "short" {
		t.Fatalf("payload is %q", got)
	}
}

func TestCompressedDataIsRejectedNotMisparsed(t *testing.T) {
	c, _ := newTestClient()
	rec := &recorder{}
	c.Register("TestChannel", rec)
	c.Process(append(buildHeader(cmdCreateRequest, 0, 1), []byte("TestChannel")...))

	// Compressed data needs the MPPC dictionary; it must not be emitted as if
	// it were plain data.
	c.Process(append(buildHeader(cmdDataCompressed, 0, 1), []byte{0x01, 0x02, 0x03}...))
	if len(rec.payloads) != 0 {
		t.Fatalf("compressed payload was delivered: %q", rec.payloads)
	}
}

func TestCloseAll(t *testing.T) {
	c, _ := newTestClient()
	a, b := &recorder{}, &recorder{}
	c.Register("A", a)
	c.Register("B", b)
	c.Process(append(buildHeader(cmdCreateRequest, 0, 1), []byte("A")...))
	c.Process(append(buildHeader(cmdCreateRequest, 0, 2), []byte("B")...))

	c.CloseAll()

	if !a.closed || !b.closed {
		t.Fatalf("channels were not closed: A=%v B=%v", a.closed, b.closed)
	}
}
