package pdu

import (
	"sync"
	"testing"
)

// The capability map is written while the session's read goroutine is parsing
// messages, so a reader elsewhere in the program must not trip over it. A
// concurrent write to a Go map is a run time fatal error, not a panic, which is
// to say it takes the process and every session in it down and cannot be
// recovered; this is the shape that did exactly that.
func TestServerCapabilitiesIsSafeToReadWhileBeingWritten(t *testing.T) {
	c := NewClient(newCaptureTransport())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.capsMu.Lock()
				c.serverCapabilities[CAPSTYPE_GENERAL] = &GeneralCapability{ProtocolVersion: 0x0200}
				c.serverCapabilities[CAPSTYPE_BITMAP] = &BitmapCapability{}
				c.capsMu.Unlock()

				// And a reader, which is what the accessor is for.
				if got := c.ServerCapabilities(); len(got) == 0 {
					t.Error("the accessor returned nothing")
					return
				}
			}
		}()
	}
	wg.Wait()

	caps := c.ServerCapabilities()
	if _, ok := caps[CAPSTYPE_GENERAL]; !ok {
		t.Error("the general capability went missing")
	}
	// The accessor hands out a copy, so a caller cannot reach the map itself.
	before := len(caps)
	caps[CAPSTYPE_SHARE] = &ShareCapability{}
	caps[CAPSTYPE_SOUND] = &SoundCapability{}
	if after := len(c.ServerCapabilities()); after != before {
		t.Errorf("the accessor handed out its own map: %d entries became %d", before, after)
	}
}
