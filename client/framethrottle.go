package client

import (
	"image"
	"sync"
	"time"
)

// The refresh tiers a caller can ask for. They are names for the common display
// rates rather than a menu the client enforces, so a caller that wants something
// else, such as two per second on a slow link, passes the duration directly.
const (
	// FrameTierOff means every batch of updates is reported as it arrives,
	// which is what OnFrame has always done and is the default.
	FrameTierOff time.Duration = 0
	// FrameTier60 is a 60Hz display.
	FrameTier60 = time.Second / 60
	// FrameTier30 is a 30Hz display, or a link where a faster tier does more harm than good.
	FrameTier30 = time.Second / 30
	// FrameTier15 is a deliberately slow tier, for a link that cannot carry a
	// screen's worth of updates at 30.
	FrameTier15 = time.Second / 15
)

// frameThrottle rate limits frame reports.
//
// The policy is the whole point, so it is worth spelling out. A server sends one
// screen as a burst of updates, and a caller that encodes and pushes each one
// pushes parts of one frame at different times, which is the tearing this exists
// to bound. Capping the rate alone would be the wrong fix, because the first
// update of a burst would then wait a full interval before appearing, which
// adds latency to the common case to protect the rare one.
//
// So the first report of a burst is delivered immediately and the interval only
// starts then: a burst arriving inside that interval is coalesced into one
// report at its end, and nothing is ever held back by more than one interval.
// The report a caller gets is the union of what changed since the last report,
// merged the same way a single batch is, so the whole frame is visible at once
// rather than arriving as a series of rectangles the caller has to stitch.
type frameThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	pending  []image.Rectangle
	// cooling is true between the immediate report and the timer that follows
	// it. While it is set, incoming batches are merged instead of reported.
	cooling bool
	timer   *time.Timer
	// bounds is asked for rather than stored, because a Display Control resize
	// replaces the framebuffer while the session runs, and a throttle that
	// clipped against the size it started with would drop every update after
	// the first one.
	boundsFn func() image.Rectangle
	f        func([]image.Rectangle)
}

func newFrameThrottle(f func([]image.Rectangle), boundsFn func() image.Rectangle, interval time.Duration) *frameThrottle {
	return &frameThrottle{f: f, boundsFn: boundsFn, interval: interval}
}

// clipBounds returns the current desktop size, or a rectangle that clips
// nothing when the framebuffer does not exist yet.
func (t *frameThrottle) clipBounds() image.Rectangle {
	if t.boundsFn == nil {
		return image.Rect(0, 0, 1<<30, 1<<30)
	}
	if b := t.boundsFn(); !b.Empty() {
		return b
	}
	return image.Rect(0, 0, 1<<30, 1<<30)
}

// interval returns the current cap, which may have changed since construction.
func (t *frameThrottle) currentInterval() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.interval
}

// setInterval changes the cap. A change takes effect from the next window, and a
// new value of zero turns throttling off, which is what a caller that has found
// the right tier and wants to compare against it would ask for.
func (t *frameThrottle) setInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	t.mu.Lock()
	changed := t.interval != d
	t.interval = d
	running := t.timer != nil
	t.mu.Unlock()

	if !changed {
		return
	}
	// Switching to no throttle: flush what is waiting rather than leaving it
	// behind, and stop the window so the next batch is reported immediately.
	if d == 0 {
		t.stop()
		t.flush()
		return
	}
	// Switching to a shorter interval while cooling would let the pending wait
	// out the old, longer window. Restart it with the new one.
	if running {
		t.reschedule()
	}
}

// add merges a batch of changed regions, reporting it immediately when the tier
// allows.
func (t *frameThrottle) add(dirty []image.Rectangle) {
	if len(dirty) == 0 {
		return
	}
	t.mu.Lock()
	if t.f == nil {
		t.mu.Unlock()
		return
	}
	for _, r := range dirty {
		t.pending = addDirty(t.pending, r, t.clipBounds())
	}
	if t.interval == 0 || !t.cooling {
		// Report now and open the window.
		t.cooling = t.interval > 0
		out := t.pending
		t.pending = nil
		t.mu.Unlock()

		t.f(out)

		if t.cooling {
			t.startWindow()
		}
		return
	}
	// Cooling: the timer at the end of the window will report this.
	t.mu.Unlock()
}

func (t *frameThrottle) startWindow() {
	t.mu.Lock()
	interval := t.interval
	t.mu.Unlock()
	if interval <= 0 {
		return
	}
	t.mu.Lock()
	if t.timer != nil {
		t.timer.Stop()
	}
	t.timer = time.AfterFunc(interval, t.windowEnded)
	t.mu.Unlock()
}

func (t *frameThrottle) windowEnded() {
	t.flush()
	// More may have arrived while the callback ran.
	t.mu.Lock()
	again := t.timer != nil && len(t.pending) > 0
	interval := t.interval
	t.mu.Unlock()
	if again && interval > 0 {
		t.startWindow()
	}
}

func (t *frameThrottle) flush() {
	t.mu.Lock()
	if len(t.pending) == 0 || t.f == nil {
		t.mu.Unlock()
		return
	}
	out := t.pending
	t.pending = nil
	cooling := t.cooling
	t.mu.Unlock()

	t.f(out)

	// Only a throttling tier holds a window open. With the tier off, add has
	// already reported and nothing is left to arm.
	if cooling {
		t.startWindow()
	}
}

func (t *frameThrottle) stop() {
	t.mu.Lock()
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.cooling = false
	t.mu.Unlock()
}

// reschedule ends the current window early, so the pending regions are reported
// under the new interval rather than waiting out the old one.
func (t *frameThrottle) reschedule() {
	t.mu.Lock()
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.mu.Unlock()
	t.flush()
}
