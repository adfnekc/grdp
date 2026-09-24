package cliprdr

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/adfnekc/grdp/core"
)

// captureSender records the PDUs the client writes. It is safe for concurrent
// use because the client can write from a deferred goroutine.
type captureSender struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (c *captureSender) SendToChannel(_ string, s []byte) (int, error) {
	cp := make([]byte, len(s))
	copy(cp, s)
	c.mu.Lock()
	c.chunks = append(c.chunks, cp)
	c.mu.Unlock()
	return len(s), nil
}

// sent returns a snapshot of the recorded PDUs.
func (c *captureSender) sent() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.chunks))
	copy(out, c.chunks)
	return out
}

// reset drops everything recorded so far.
func (c *captureSender) reset() {
	c.mu.Lock()
	c.chunks = nil
	c.mu.Unlock()
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
func sentTypes(s *captureSender) []uint16 {
	chunks := s.sent()
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
	if len(sent.sent()) != 0 {
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
	sent.reset()

	c.Process(msg(CB_MONITOR_READY, 0, nil))

	types := sentTypes(sent)
	// Capabilities, then a format list carrying the text formats.
	if len(types) != 2 || types[0] != CB_CLIP_CAPS || types[1] != CB_FORMAT_LIST {
		t.Fatalf("got message types %v, want [7 2]", types)
	}
	fl := sent.sent()[1]
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

	fl := sent.sent()[1]
	if got := binary.LittleEndian.Uint32(fl[4:]); got != 0 {
		t.Fatalf("format list length is %d, want 0", got)
	}
}

func TestFormatListWhateverRequestsUnicodeText(t *testing.T) {
	c, sent := newTestClient()
	// A format list only ever follows the monitor ready message.
	c.Process(msg(CB_MONITOR_READY, 0, nil))
	sent.reset()

	// A list like a real server sends: unicode text, locale, plain text, oem.
	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT, 16, CF_TEXT, 7)))

	types := sentTypes(sent)
	if len(types) != 1 || types[0] != CB_FORMAT_LIST_RESPONSE {
		t.Fatalf("got message types %v, want just a format list response", types)
	}
	if got := binary.LittleEndian.Uint16(sent.sent()[0][2:]); got != CB_RESPONSE_OK {
		t.Fatalf("response flags are 0x%04x, want OK", got)
	}

	// With no handler registered nothing asks for the data, so an explicit
	// request is needed.
	if err := c.RequestClipboardText(); err != nil {
		t.Fatalf("RequestClipboardText: %v", err)
	}
	if got := sentTypes(sent); len(got) != 2 || got[1] != CB_FORMAT_DATA_REQUEST {
		t.Fatalf("got message types %v, want a data request", got)
	}
	req := sent.sent()[1]
	if got := binary.LittleEndian.Uint32(req[4:]); got != 4 {
		t.Fatalf("request length is %d, want 4", got)
	}
	if got := binary.LittleEndian.Uint32(req[8:]); got != CF_UNICODETEXT {
		t.Fatalf("requested format %d, want %d", got, CF_UNICODETEXT)
	}
}

// TestFormatListRequestsAutomatically covers the convenience path: a
// registered handler makes the client ask for announced text on its own,
// after a delay because a server may not be able to serve it immediately.
func TestFormatListRequestsAutomatically(t *testing.T) {
	old := clipboardRequestDelay
	clipboardRequestDelay = time.Millisecond
	defer func() { clipboardRequestDelay = old }()

	c, sent := newTestClient()
	c.OnText(func(string) {})

	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT)))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, t2 := range sentTypes(sent) {
			if t2 == CB_FORMAT_DATA_REQUEST {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no data request was sent; got %v", sentTypes(sent))
}

func TestRequestClipboardTextBeforeReadyFails(t *testing.T) {
	c, sent := newTestClient()
	if err := c.RequestClipboardText(); err == nil {
		t.Fatal("expected an error before the channel is ready")
	}
	if len(sent.sent()) != 0 {
		t.Fatal("nothing should have been written")
	}
}

// TestProcessWalksMultiplePdus covers a server batching several PDUs into one
// channel payload.
func TestProcessWalksMultiplePdus(t *testing.T) {
	c, sent := newTestClient()

	payload := msg(CB_MONITOR_READY, 0, nil)
	payload = append(payload, msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT))...)
	payload = append(payload, msg(CB_FORMAT_LIST, 0, formatList(CF_TEXT))...)

	c.Process(payload)

	responses := 0
	for _, t2 := range sentTypes(sent) {
		if t2 == CB_FORMAT_LIST_RESPONSE {
			responses++
		}
	}
	if responses != 2 {
		t.Fatalf("got %d format list responses, want 2; types %v", responses, sentTypes(sent))
	}
}

// TestProcessStopsAtTrailingGarbage covers what xrdp actually sends: a format
// list whose declared length is shorter than the bytes that follow it, with a
// zero format id terminator in between. Parsing must stop there instead of
// treating the leftovers as a message.
func TestProcessStopsAtTrailingGarbage(t *testing.T) {
	c, sent := newTestClient()

	// A real format list, then the four extra bytes xrdp appends.
	fl := msg(CB_FORMAT_LIST, 0, formatList(CF_UNICODETEXT, 16, CF_TEXT, 7))
	payload := append(append([]byte{}, fl...), 0, 0, 0, 0)

	c.Process(payload)

	types := sentTypes(sent)
	if len(types) != 1 || types[0] != CB_FORMAT_LIST_RESPONSE {
		t.Fatalf("got message types %v, want exactly one format list response", types)
	}
}

func TestFormatListWithoutTextDoesNotRequest(t *testing.T) {
	c, sent := newTestClient()
	// Only a non-text format is offered.
	c.Process(msg(CB_FORMAT_LIST, 0, formatList(CF_HDROP)))

	if got := sentTypes(sent); len(got) != 1 || got[0] != CB_FORMAT_LIST_RESPONSE {
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
	sent.reset()

	// The server asks for unicode text.
	req := make([]byte, 4)
	binary.LittleEndian.PutUint32(req, CF_UNICODETEXT)
	c.Process(msg(CB_FORMAT_DATA_REQUEST, 0, req))

	if len(sent.sent()) != 1 {
		t.Fatalf("got %d PDUs, want 1 data response", len(sent.chunks))
	}
	resp := sent.sent()[0]
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
	sent.reset()

	req := make([]byte, 4)
	binary.LittleEndian.PutUint32(req, CF_HDROP)
	c.Process(msg(CB_FORMAT_DATA_REQUEST, 0, req))

	if len(sent.sent()) != 1 {
		t.Fatalf("got %d PDUs, want 1", len(sent.chunks))
	}
	// Only the NUL terminator, so the server gets an empty string rather
	// than the wrong format's bytes.
	if got := sent.sent()[0][8:]; !bytes.Equal(got, []byte{0, 0}) {
		t.Fatalf("payload is %x, want just a terminator", got)
	}
}

func TestSetClipboardTextAfterReadyAnnouncesImmediately(t *testing.T) {
	c, sent := newTestClient()
	c.Process(msg(CB_MONITOR_READY, 0, nil))
	sent.reset()

	if err := c.SetClipboardText("later"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}

	if got := sentTypes(sent); len(got) != 1 || got[0] != CB_FORMAT_LIST {
		t.Fatalf("got message types %v, want a format list", got)
	}
}

// The next two fixtures are verbatim captures from xrdp. They are worth
// keeping because xrdp under-declares the length of both of them and appends
// four bytes beyond it, which a parser that trusts the declared length will
// quietly mis-handle.

// xrdp announcing text, unicode, locale and oem formats.
const xrdpFormatListHex = "02000000180000000d000000000010000000000001000000000007000000000000000000"

// xrdp answering a request for unicode text with "ABCDEFGHIJ".
const xrdpDataResponseHex = "05000100160000004100420043004400450046004700480049004a00000000000000"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return b
}

func TestRealFormatListFromXrdp(t *testing.T) {
	c, sent := newTestClient()
	c.Process(msg(CB_MONITOR_READY, 0, nil))
	sent.reset()

	c.Process(mustHex(t, xrdpFormatListHex))

	// The four formats must be decoded despite the declared length being four
	// bytes short of what actually follows.
	types := sentTypes(sent)
	if len(types) != 1 || types[0] != CB_FORMAT_LIST_RESPONSE {
		t.Fatalf("got message types %v, want one format list response", types)
	}
	if got := binary.LittleEndian.Uint16(sent.sent()[0][2:]); got != CB_RESPONSE_OK {
		t.Fatalf("response flags are 0x%04x", got)
	}
}

func TestRealDataResponseFromXrdp(t *testing.T) {
	c, _ := newTestClient()
	c.Process(msg(CB_MONITOR_READY, 0, nil))

	var got string
	c.OnText(func(s string) { got = s })

	// The announcement, then the answer, exactly as xrdp sent them.
	c.Process(mustHex(t, xrdpFormatListHex))
	c.Process(mustHex(t, xrdpDataResponseHex))

	if got != "ABCDEFGHIJ" {
		t.Fatalf("got %q, want %q", got, "ABCDEFGHIJ")
	}
}
