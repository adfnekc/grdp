package plugin

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
)

// fakeTransport is a channel plugin that does nothing, so that Register has
// something to register.
type fakeTransport struct {
	name    string
	options uint32
	sender  core.ChannelSender
}

func (f *fakeTransport) GetType() (string, uint32)   { return f.name, f.options }
func (f *fakeTransport) Sender(s core.ChannelSender) { f.sender = s }
func (f *fakeTransport) Process(s []byte)            {}

// fakeSender records the chunks it is given and can be told to fail, which is what
// makes the count and the error testable.
type fakeSender struct {
	chunks [][]byte
	failAt int // fail on the chunk of this index, counting from zero
}

func (f *fakeSender) SendToChannel(channel string, s []byte) (int, error) {
	if f.failAt >= 0 && len(f.chunks) == f.failAt {
		return 0, errors.New("link down")
	}
	f.chunks = append(f.chunks, append([]byte(nil), s...))
	return len(s), nil
}

type fakeConn struct{ emission.Emitter }

func (f *fakeConn) Read(b []byte) (int, error)  { return 0, nil }
func (f *fakeConn) Write(b []byte) (int, error) { return len(b), nil }
func (f *fakeConn) Close() error                { return nil }

func newTestChannels(t *testing.T, options uint32) (*Channels, *fakeSender) {
	t.Helper()
	c := NewChannels(&fakeConn{Emitter: *emission.NewEmitter()})
	c.Register(&fakeTransport{name: "cliprdr", options: options})
	sender := &fakeSender{failAt: -1}
	c.SetChannelSender(sender)
	return c, sender
}

// chunk is one chunk as the wire carries it: the total length, the flags, then the
// payload.
type chunk struct {
	total   uint32
	flags   uint32
	payload []byte
}

func parseChunks(t *testing.T, chunks [][]byte) []chunk {
	t.Helper()
	out := make([]chunk, 0, len(chunks))
	for _, raw := range chunks {
		if len(raw) < 8 {
			t.Fatalf("chunk is %d bytes, too short for a header", len(raw))
		}
		out = append(out, chunk{
			total:   binary.LittleEndian.Uint32(raw[0:4]),
			flags:   binary.LittleEndian.Uint32(raw[4:8]),
			payload: raw[8:],
		})
	}
	return out
}

// The count used to be zero always, because the function returned the variable its
// loop counted down, so a caller could not tell a delivered message from a dropped
// one.
func TestSendToChannelCountsWhatItSent(t *testing.T) {
	c, sender := newTestChannels(t, CHANNEL_OPTION_INITIALIZED)
	msg := []byte("hello channel")
	n, err := c.SendToChannel("cliprdr", msg)
	if err != nil {
		t.Fatalf("SendToChannel: %v", err)
	}
	if n != len(msg) {
		t.Errorf("SendToChannel reported %d bytes for a %d byte message", n, len(msg))
	}
	if len(sender.chunks) != 1 {
		t.Fatalf("sent %d chunks, want 1", len(sender.chunks))
	}
	got := parseChunks(t, sender.chunks)[0]
	if got.total != uint32(len(msg)) {
		t.Errorf("the chunk says the message is %d bytes, want %d", got.total, len(msg))
	}
	if got.flags&CHANNEL_FLAG_FIRST == 0 || got.flags&CHANNEL_FLAG_LAST == 0 {
		t.Errorf("a message that fits in one chunk carries both flags, got %#x", got.flags)
	}
}

// A message longer than the chunk size arrives in pieces, each carrying the total
// length of the whole message rather than its own.
func TestSendToChannelChunksLongMessages(t *testing.T) {
	c, sender := newTestChannels(t, CHANNEL_OPTION_INITIALIZED)
	msg := make([]byte, CHANNEL_CHUNK_LENGTH+10)
	for i := range msg {
		msg[i] = byte(i)
	}
	n, err := c.SendToChannel("cliprdr", msg)
	if err != nil {
		t.Fatalf("SendToChannel: %v", err)
	}
	if n != len(msg) {
		t.Errorf("reported %d bytes for a %d byte message", n, len(msg))
	}
	chunks := parseChunks(t, sender.chunks)
	if len(chunks) != 2 {
		t.Fatalf("sent %d chunks, want 2", len(chunks))
	}
	for i, ch := range chunks {
		if ch.total != uint32(len(msg)) {
			t.Errorf("chunk %d says the message is %d bytes, want %d", i, ch.total, len(msg))
		}
	}
	if chunks[0].flags&CHANNEL_FLAG_FIRST == 0 {
		t.Error("the first chunk does not carry FIRST")
	}
	if chunks[0].flags&CHANNEL_FLAG_LAST != 0 {
		t.Error("the first chunk carries LAST as well as FIRST, which would end the message early")
	}
	if chunks[1].flags&CHANNEL_FLAG_LAST == 0 {
		t.Error("the last chunk does not carry LAST")
	}

	// The payload has to come back in order and whole, which is what the receiver
	// joins up.
	var joined []byte
	for _, ch := range chunks {
		joined = append(joined, ch.payload...)
	}
	if string(joined) != string(msg) {
		t.Error("the chunks do not rejoin into the message that was sent")
	}
}

// A channel that asked for the header to be shown must have it, since it is what
// tells the channel where the message starts and ends.
func TestSendToChannelHonoursShowProtocol(t *testing.T) {
	c, sender := newTestChannels(t, CHANNEL_OPTION_INITIALIZED|CHANNEL_OPTION_SHOW_PROTOCOL)
	if _, err := c.SendToChannel("cliprdr", []byte("x")); err != nil {
		t.Fatalf("SendToChannel: %v", err)
	}
	if got := parseChunks(t, sender.chunks)[0]; got.flags&CHANNEL_FLAG_SHOW_PROTOCOL == 0 {
		t.Errorf("the flag was dropped, flags are %#x", got.flags)
	}
}

// The sender's error used to be discarded, so a send that failed looked like a send
// that worked.
func TestSendToChannelReportsTheSendersError(t *testing.T) {
	c, sender := newTestChannels(t, CHANNEL_OPTION_INITIALIZED)
	sender.failAt = 0
	n, err := c.SendToChannel("cliprdr", []byte("hello"))
	if err == nil {
		t.Fatal("SendToChannel reported success while the sender was failing")
	}
	if !errors.Is(err, errors.Unwrap(err)) && err.Error() == "" {
		t.Error("the error does not say what happened")
	}
	if n != 0 {
		t.Errorf("reported %d bytes sent, want 0", n)
	}
}

func TestSendToChannelUnregisteredAndNoSender(t *testing.T) {
	c, _ := newTestChannels(t, CHANNEL_OPTION_INITIALIZED)
	if _, err := c.SendToChannel("nonesuch", []byte("x")); err == nil {
		t.Error("sending on an unregistered channel reported success")
	}

	c2 := NewChannels(&fakeConn{Emitter: *emission.NewEmitter()})
	c2.Register(&fakeTransport{name: "cliprdr"})
	if _, err := c2.SendToChannel("cliprdr", []byte("x")); err == nil {
		t.Error("sending with no channel sender reported success")
	}
}
