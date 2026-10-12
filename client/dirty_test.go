package client

import (
	"image"
	"testing"

	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/orders"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// newDirtyClient returns a client with a framebuffer and the accumulator wired to
// the same event OnFrame reports, without a session.
func newDirtyClient(w, h int) *RdpClient {
	c := &RdpClient{}
	c.screen = orders.NewScreen(w, h)
	c.fb = &Framebuffer{screen: c.screen}
	c.pdu = pdu.NewClient(&capTransport{Emitter: *emission.NewEmitter()})
	c.pdu.On("frame", c.accumulateDirty)
	return c
}

// TakeDirty exists because a caller that shows the screen on its own clock has to
// know what changed since it last showed it. Before this, such a caller had to
// union the per batch updates itself, which every one of them would have to get
// right and would get wrong in the same way: an update arriving between the union
// and the reset is lost, and the screen keeps a stale region until something else
// draws there.
func TestTakeDirtyAccumulatesUntilTaken(t *testing.T) {
	c := newDirtyClient(100, 100)

	c.pdu.Emit("frame", []image.Rectangle{image.Rect(0, 0, 10, 10)})
	c.pdu.Emit("frame", []image.Rectangle{image.Rect(10, 0, 20, 10)})

	got := c.takeDirty()
	if len(got) != 1 {
		t.Fatalf("got %d rectangles, want 1: adjacent updates merge", len(got))
	}
	if want := image.Rect(0, 0, 20, 10); got[0] != want {
		t.Errorf("got %v, want %v", got[0], want)
	}

	// Taking consumes: a caller that presents what it was told about is not told
	// about it again.
	if again := c.takeDirty(); len(again) != 0 {
		t.Errorf("the second call returned %v, want nothing", again)
	}
}

// What is accumulated is what the next present has to show, so a second batch
// after a take starts a new set rather than repeating the old one.
func TestTakeDirtyStartsAFreshSetAfterEachTake(t *testing.T) {
	c := newDirtyClient(100, 100)

	c.pdu.Emit("frame", []image.Rectangle{image.Rect(0, 0, 10, 10)})
	if got := c.takeDirty(); len(got) != 1 || got[0] != image.Rect(0, 0, 10, 10) {
		t.Fatalf("first take = %v", got)
	}
	c.pdu.Emit("frame", []image.Rectangle{image.Rect(50, 50, 60, 60)})
	got := c.takeDirty()
	if len(got) != 1 || got[0] != image.Rect(50, 50, 60, 60) {
		t.Errorf("second take = %v, want only the new region", got)
	}
}

// A window that covers most of the frame collapses to the whole frame, exactly as
// a single batch does, because at that point encoding the whole frame is cheaper
// than encoding the pieces.
func TestTakeDirtyCollapsesToTheWholeFrame(t *testing.T) {
	c := newDirtyClient(100, 100)
	// Four separate bands, together most of the frame.
	for _, r := range []image.Rectangle{
		image.Rect(0, 0, 100, 20),
		image.Rect(0, 20, 100, 40),
		image.Rect(0, 40, 100, 60),
		image.Rect(0, 60, 100, 80),
	} {
		c.pdu.Emit("frame", []image.Rectangle{r})
	}
	got := c.takeDirty()
	if len(got) != 1 || got[0] != image.Rect(0, 0, 100, 100) {
		t.Errorf("got %v, want the whole frame", got)
	}
}

// A still screen produces nothing to present, which is the common case at a
// display's cadence and has to stay cheap.
func TestTakeDirtyOnAStillScreenIsEmpty(t *testing.T) {
	c := newDirtyClient(100, 100)
	if got := c.takeDirty(); len(got) != 0 {
		t.Errorf("got %v on a screen nothing has drawn to", got)
	}
}
