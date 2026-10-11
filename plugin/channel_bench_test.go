package plugin

import (
	"encoding/binary"
	"testing"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
)

// sink is a channel plugin that counts what arrives, standing in for the clipboard.
type sink struct {
	name   string
	got    int
	chunks int
}

func (s *sink) GetType() (string, uint32) { return s.name, CHANNEL_OPTION_INITIALIZED }
func (s *sink) Sender(core.ChannelSender) {}
func (s *sink) Process(b []byte)          { s.got += len(b); s.chunks++ }

// chunked builds the wire form of a channel message of one chunk: the total
// length, the flags, then the payload.
func chunked(payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(out[4:8], CHANNEL_FLAG_FIRST|CHANNEL_FLAG_LAST)
	copy(out[8:], payload)
	return out
}

// This is the path a file transfer spends its time on, and the one the profile
// named: every chunk of the file arrives as a channel event, which is emitted,
// reassembled, and handed to the plugin. Emitting on the transport is what the
// security layer does, so the benchmark drives the same events it does.
func BenchmarkChannelDelivery(b *testing.B) {
	for _, size := range []int{1460, 8192} {
		// 1460 is what fits in a packet on a normal link, so it is the size a
		// transfer mostly sees; 8192 is what the clipboard asks for when it can.
		payload := make([]byte, size)
		msg := chunked(payload)

		conn := &fakeConn{Emitter: *emission.NewEmitter()}
		channels := NewChannels(conn)
		s := &sink{name: "cliprdr"}
		channels.Register(s)
		channels.SetChannelSender(&fakeSender{failAt: -1})

		b.Run(sizeName(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				conn.Emit("channel", "cliprdr", msg)
			}
		})
		if s.got == 0 {
			b.Fatal("the payload never reached the plugin")
		}
	}
}

func sizeName(n int) string {
	if n == 1460 {
		return "packet sized (1460)"
	}
	return "clipboard sized (8192)"
}
