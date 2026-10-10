package tpkt

import (
	"testing"

	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
)

// TestRecvDataLoggingDoesNotScaleWithPayload pins the cost this layer used to
// impose on every received PDU. `recvData` dumped the whole payload into a TRACE
// line by building the hexadecimal string at the call site, so the string was
// built and thrown away on every packet whether or not tracing was on, at this
// layer and at the four others above and below it. On a file transfer that was
// most of the work, and it grew with the read size, which is why a larger read
// moved data more slowly.
//
// The assertion is that the cost does not follow the payload. A call is not free
// and need not be: what it must not do is cost a megabyte for a megabyte.
func TestRecvDataLoggingDoesNotScaleWithPayload(t *testing.T) {
	oldLevel := glog.Level()
	glog.SetLogger(nil)
	glog.SetLevel(glog.NONE)
	t.Cleanup(func() { glog.SetLevel(oldLevel) })

	bytesPerCall := func(size int) uint64 {
		payload := make([]byte, size)
		// recvData arms the next read, so a paused read keeps it off a transport
		// this test does not have.
		packet := &TPKT{Emitter: *emission.NewEmitter()}
		packet.PauseRead()
		r := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				packet.recvData(payload, nil)
			}
		})
		return uint64(r.AllocedBytesPerOp())
	}

	small, large := bytesPerCall(4<<10), bytesPerCall(1<<20)
	if large > small {
		t.Errorf("recvData cost %d bytes for a 4 KiB payload and %d bytes for 1 MiB, want the same: the cost must not follow the payload", small, large)
	}
	if large > 4<<10 {
		t.Errorf("recvData cost %d bytes for a 1 MiB payload, want a small constant", large)
	}
}
