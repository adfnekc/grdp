package cliprdr

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/adfnekc/grdp/core"
)

// captureSender records the PDUs the client writes.
type captureSender struct{ chunks [][]byte }

func (c *captureSender) SendToChannel(_ string, s []byte) (int, error) {
	cp := make([]byte, len(s))
	copy(cp, s)
	c.chunks = append(c.chunks, cp)
	return len(s), nil
}

// resetLocalClipboard clears the process wide clipboard contents, which would
// otherwise leak from one test into the next.
func resetLocalClipboard() {
	localClipMu.Lock()
	localClipText, localClipSet = "", false
	localClipMu.Unlock()
}

func newTestClient() (*CliprdrClient, *captureSender) {
	resetLocalClipboard()
	s := &captureSender{}
	c := NewCliprdrClient()
	c.Sender(s)
	return c, s
}

// msg builds a cliprdr PDU: the 8 byte header then the body.
func msg(msgType, flags uint16, body []byte) []byte {
	out := append([]byte{byte(msgType), byte(msgType >> 8)}, byte(flags), byte(flags>>8))
	out = append(out, byte(len(body)), byte(len(body)>>8), 0, 0)
	return append(out, body...)
}

// formatList encodes a format list the way a server would, with the NUL
// terminator that an unnamed format still carries.
func formatList(ids ...uint32) []byte {
	var b []byte
	for _, id := range ids {
		b = append(b, byte(id), byte(id>>8), byte(id>>16), byte(id>>24))
		b = append(b, 0, 0) // empty name terminator
	}
	return b
}

// sentTypes returns the message types the client wrote, in order.
func sentTypes(chunks [][]byte) []uint16 {
	out := make([]uint16, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, binary.LittleEndian.Uint16(c))
	}
	return out
}

func TestSetClipboardTextBeforeReadyOnlyStores(t *testing.T) {
	c, sent := newTestClient()

	if err := c.SetClipboardText("hello"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	// The channel is not up yet, so nothing may be written.
	if len(sent.chunks) != 0 {
		t.Fatalf("wrote %d PDUs before the channel was ready", len(sent.chunks))
	}
	if got, ok := localText(); !ok || got != "hello" {
		t.Fatalf("local text is %q, %v", got, ok)
	}
}

func TestMonitorReadyAdvertisesLocalText(t *testing.T) {
	c, sent := newTestClient()
	if err := c.SetClipboardText("hello"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	sent.chunks = nil

	c.Process(msg(CB_MONITOR_READY, 0, nil))

	types := sentTypes(sent.chunks)
	// Capabilities, then a format list carrying the text formats.
	if len(types) != 2 || types[0] != CB_CLIP_CAPS || types[1] != CB_FORMAT_LIST {
		t.Fatalf("got message types %v, want [7 2]", types)
	}
	fl := sent.chunks[1]
	if got := binary.LittleEndian.Uint32(fl[4:]); got != 12 {
		t.Fatalf("format list length is %d, want 12 (two formats)", got)
	}
	if got := binary.LittleEndian.Uint32(fl[8:]); got != CF_UNICODETEXT {
		t.Fatalf("first format is %d, want %d", got, CF_UNICODETEXT)
	}
	if got := binary.LittleEndian.Uint32(fl[8+6:]); got != CF_TEXT {
		t.Fatalf("second format is %d, want %d", got, CF_TEXT)
	}
}

func TestEmptyClipboardAdvertisesNothing(t *testing.T) {
	c, sent := newTestClient()
	c.Process(msg(CB_MONITOR_READY, 0, nil))

	fl := sent.chunks[1]
	if got := binary.LittleEndian.Uint32(fl[4:]); got != 0 {
		t.Fatalf("format list length is %d, want 0", got)
	}
}

func TestFormatListRequestsUnicodeText(t *testing.T) {
	c, sent := newTestClient()

	// A list like a real server sends: unicode text, locale, plain text, oem.
	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT, 16, CF_TEXT, 7)))

	types := sentTypes(sent.chunks)
	if len(types) != 2 {
		t.Fatalf("got message types %v, want a response and a request", types)
	}
	if types[0] != CB_FORMAT_LIST_RESPONSE {
		t.Fatalf("first message is 0x%04x, want 0x%04x", types[0], CB_FORMAT_LIST_RESPONSE)
	}
	if got := binary.LittleEndian.Uint16(sent.chunks[0][2:]); got != CB_RESPONSE_OK {
		t.Fatalf("response flags are 0x%04x, want OK", got)
	}
	if types[1] != CB_FORMAT_DATA_REQUEST {
		t.Fatalf("second message is 0x%04x, want 0x%04x", types[1], CB_FORMAT_DATA_REQUEST)
	}
	req := sent.chunks[1]
	if got := binary.LittleEndian.Uint32(req[4:]); got != 4 {
		t.Fatalf("request length is %d, want 4", got)
	}
	if got := binary.LittleEndian.Uint32(req[8:]); got != CF_UNICODETEXT {
		t.Fatalf("requested format %d, want %d", got, CF_UNICODETEXT)
	}
}

func TestFormatListWithoutTextDoesNotRequest(t *testing.T) {
	c, sent := newTestClient()
	// Only a non-text format is offered.
	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_HDROP)))

	if got := sentTypes(sent.chunks); len(got) != 1 || got[0] != CB_FORMAT_LIST_RESPONSE {
		t.Fatalf("got message types %v, want just a format list response", got)
	}
}

func TestFormatDataResponseDeliversText(t *testing.T) {
	c, _ := newTestClient()

	var got string
	c.OnText(func(s string) { got = s })

	// A request has to be outstanding for the response to be treated as text.
	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT)))

	// The server replies with UTF-16LE and a NUL terminator.
	body := append(core.UnicodeEncode("XRDPCLIPTEST"), 0, 0)
	c.Process(msg(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, body))

	if got != "XRDPCLIPTEST" {
		t.Fatalf("handler got %q, want %q", got, "XRDPCLIPTEST")
	}
}

func TestFormatDataResponseWithoutRequestIsIgnored(t *testing.T) {
	c, _ := newTestClient()

	called := false
	c.OnText(func(string) { called = true })

	body := append(core.UnicodeEncode("surprise"), 0, 0)
	c.Process(msg(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, body))

	if called {
		t.Fatal("a response with no outstanding request must not be delivered")
	}
}

func TestFormatDataResponseFailureIsIgnored(t *testing.T) {
	c, _ := newTestClient()

	called := false
	c.OnText(func(string) { called = true })

	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT)))
	c.Process(msg(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_FAIL, nil))

	if called {
		t.Fatal("a failed response must not be delivered")
	}
}

func TestFormatDataRequestServesLocalText(t *testing.T) {
	c, sent := newTestClient()
	if err := c.SetClipboardText("local-text"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	sent.chunks = nil

	// The server asks for unicode text.
	req := make([]byte, 4)
	binary.LittleEndian.PutUint32(req, CF_UNICODETEXT)
	c.Process(msg(CB_FORMAT_DATA_REQUEST, 0, req))

	if len(sent.chunks) != 1 {
		t.Fatalf("got %d PDUs, want 1 data response", len(sent.chunks))
	}
	resp := sent.chunks[0]
	if got := binary.LittleEndian.Uint16(resp); got != CB_FORMAT_DATA_RESPONSE {
		t.Fatalf("message type is 0x%04x", got)
	}
	want := append(core.UnicodeEncode("local-text"), 0, 0)
	if !bytes.Equal(resp[8:], want) {
		t.Fatalf("payload is %x, want %x", resp[8:], want)
	}
}

func TestFormatDataRequestForNonTextServesNothing(t *testing.T) {
	c, sent := newTestClient()
	if err := c.SetClipboardText("local-text"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	sent.chunks = nil

	req := make([]byte, 4)
	binary.LittleEndian.PutUint32(req, CF_HDROP)
	c.Process(msg(CB_FORMAT_DATA_REQUEST, 0, req))

	if len(sent.chunks) != 1 {
		t.Fatalf("got %d PDUs, want 1", len(sent.chunks))
	}
	// Only the NUL terminator, so the server gets an empty string rather
	// than the wrong format's bytes.
	if got := sent.chunks[0][8:]; !bytes.Equal(got, []byte{0, 0}) {
		t.Fatalf("payload is %x, want just a terminator", got)
	}
}

func TestSetClipboardTextAfterReadyAnnouncesImmediately(t *testing.T) {
	c, sent := newTestClient()
	c.Process(msg(CB_MONITOR_READY, 0, nil))
	sent.chunks = nil

	if err := c.SetClipboardText("later"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}

	if got := sentTypes(sent.chunks); len(got) != 1 || got[0] != CB_FORMAT_LIST {
		t.Fatalf("got message types %v, want a format list", got)
	}
}
