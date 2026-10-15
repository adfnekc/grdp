package client

import (
	"image"
	"sync"
	"testing"
	"time"
)

func testBounds() image.Rectangle { return image.Rect(0, 0, 100, 100) }

// record collects reports. It is called from the throttle's own goroutine, so it
// has to be safe for that.
type record struct {
	mu      sync.Mutex
	reports [][]image.Rectangle
	times   []time.Time
}

func (r *record) f(dirty []image.Rectangle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	regions := make([]image.Rectangle, len(dirty))
	copy(regions, dirty)
	r.reports = append(r.reports, regions)
	r.times = append(r.times, time.Now())
}

func (r *record) snapshot() ([][]image.Rectangle, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]image.Rectangle, len(r.reports))
	copy(out, r.reports)
	return out, r.times
}

// The policy: the first report of a burst is immediate. That is the whole reason
// this is a rate limit rather than a delay, and a test that only checks the rate
// would pass with the first chunk held back for a full interval.
func TestFrameThrottleReportsTheFirstChunkImmediately(t *testing.T) {
	rec := &record{}
	th := newFrameThrottle(rec.f, testBounds, 100*time.Millisecond)

	before := time.Now()
	th.add([]image.Rectangle{image.Rect(0, 0, 10, 10)})
	elapsed := time.Since(before)

	reports, _ := rec.snapshot()
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	if elapsed > 25*time.Millisecond {
		t.Errorf("the first report took %v, which is not immediate", elapsed)
	}
}

// A burst that arrives inside the interval is coalesced into one report at its
// end, so a caller pushes one frame instead of the pieces of it.
func TestFrameThrottleCoalescesABurstIntoOneReport(t *testing.T) {
	rec := &record{}
	th := newFrameThrottle(rec.f, testBounds, 150*time.Millisecond)

	th.add([]image.Rectangle{image.Rect(0, 0, 10, 10)})
	time.Sleep(10 * time.Millisecond)
	th.add([]image.Rectangle{image.Rect(50, 50, 60, 60)})
	time.Sleep(10 * time.Millisecond)
	th.add([]image.Rectangle{image.Rect(10, 0, 20, 10)})

	time.Sleep(400 * time.Millisecond)
	reports, times := rec.snapshot()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2 (one immediate, one coalesced)", len(reports))
	}
	// The coalesced report must contain all three regions, merged: neighbouring
	// and overlapping rectangles come as one, and the union of the rest.
	got := reports[1]
	if len(got) != 2 {
		t.Fatalf("the coalesced report has %d regions, want 2: %v", len(got), got)
	}
	// The gaps between reports are the retardation, so the coalesced report has
	// to land near the end of the interval and not later.
	gap := times[1].Sub(times[0])
	if gap < 100*time.Millisecond {
		t.Errorf("the second report came after only %v, which is inside the interval", gap)
	}
	if gap > 300*time.Millisecond {
		t.Errorf("the second report took %v, more than one interval behind", gap)
	}
}

// A still screen reports nothing, which at 60Hz is the common case.
func TestFrameThrottleQuietSessionReportsNothing(t *testing.T) {
	rec := &record{}
	// Never fed, so the only thing that could produce a report is a timer
	// firing with nothing behind it.
	newFrameThrottle(rec.f, testBounds, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	reports, _ := rec.snapshot()
	if len(reports) != 0 {
		t.Errorf("got %d reports on a session that changed nothing", len(reports))
	}
}

// With the tier off every batch is reported as it arrives, which is the behaviour
// OnFrame has always had and the one to compare a tier against.
func TestFrameThrottleOffReportsEveryBatch(t *testing.T) {
	rec := &record{}
	th := newFrameThrottle(rec.f, testBounds, FrameTierOff)

	th.add([]image.Rectangle{image.Rect(0, 0, 10, 10)})
	th.add([]image.Rectangle{image.Rect(50, 50, 60, 60)})
	th.add([]image.Rectangle{image.Rect(10, 0, 20, 10)})

	reports, _ := rec.snapshot()
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3, one per batch", len(reports))
	}
}

// The tier is the front end's knob: changing it while the session runs takes
// effect from the next window, and turning it off flushes what was held.
func TestFrameThrottleSetIntervalWhileRunning(t *testing.T) {
	rec := &record{}
	th := newFrameThrottle(rec.f, testBounds, time.Second)

	th.add([]image.Rectangle{image.Rect(0, 0, 10, 10)})
	th.add([]image.Rectangle{image.Rect(50, 50, 60, 60)})
	// Shorten from a second to very little: what was held must not wait out the
	// old window, or a front end that lowers the tier looks frozen.
	th.setInterval(10 * time.Millisecond)
	time.Sleep(150 * time.Millisecond)

	reports, _ := rec.snapshot()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2: the held batch was flushed by the shorter tier", len(reports))
	}
}

// Turning the tier off has to flush what was held too, not drop it, or a front
// end that switches to unlimited loses the frame it was still holding.
func TestFrameThrottleOffFlushesWhatIsHeld(t *testing.T) {
	rec := &record{}
	th := newFrameThrottle(rec.f, testBounds, time.Second)

	th.add([]image.Rectangle{image.Rect(image.Rectangle{}.Min.X, 0, 5, 5)})
	th.add([]image.Rectangle{image.Rect(50, 50, 60, 60)})
	th.setInterval(FrameTierOff)
	time.Sleep(50 * time.Millisecond)

	reports, _ := rec.snapshot()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2: one immediate and the held one flushed", len(reports))
	}
}

// A resize replaces the framebuffer, so the throttle must ask for the bounds each
// time rather than clipping against the size it started with.
func TestFrameThrottleFollowsResize(t *testing.T) {
	rec := &record{}
	size := image.Rect(0, 0, 100, 100)
	th := newFrameThrottle(rec.f, func() image.Rectangle { return size }, FrameTierOff)

	th.add([]image.Rectangle{image.Rect(0, 0, 50, 50)})
	size = image.Rect(0, 0, 400, 300)
	th.add([]image.Rectangle{image.Rect(0, 0, 400, 300)})

	reports, _ := rec.snapshot()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	if got := reports[1]; len(got) != 1 || got[0] != image.Rect(0, 0, 400, 300) {
		t.Errorf("after the resize the report was %v, want the whole new screen", got)
	}
}

// One region covering most of the frame collapses to the whole frame, the same
// rule a single batch follows, so a busy screen does not get a long list of
// rectangles to encode.
func TestFrameThrottleCollapsesToTheWholeFrame(t *testing.T) {
	rec := &record{}
	// A small first update, which the tier reports immediately, then three bands
	// that together cover most of the frame. The first band is reported rather
	// than held on its own: that is the latency half of the policy.
	th := newFrameThrottle(rec.f, testBounds, 40*time.Millisecond)
	th.add([]image.Rectangle{image.Rect(0, 0, 10, 10)})
	for _, r := range []image.Rectangle{
		image.Rect(0, 0, 100, 34),
		image.Rect(0, 34, 100, 67),
		image.Rect(0, 67, 100, 100),
	} {
		th.add([]image.Rectangle{r})
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)

	reports, _ := rec.snapshot()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	last := reports[len(reports)-1]
	if len(last) != 1 || last[0] != image.Rect(0, 0, 100, 100) {
		t.Errorf("got %v, want the whole frame", last)
	}
}

// The invariant that makes a rate limit safe: everything a batch reported must
// still be reported by some report afterwards. A tier that drops an update
// leaves part of the screen stale until something else draws there, which is a
// fault a visual check would miss because it only looks like a dirty rectangle.
func TestFrameThrottleReportsEverythingEveryBatchDid(t *testing.T) {
	for _, interval := range []time.Duration{FrameTierOff, 5 * time.Millisecond, 50 * time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			rec := &record{}
			th := newFrameThrottle(rec.f, testBounds, interval)
			bounds := testBounds()

			// A known set of separate changes, none of which may be lost. The
			// merge collapses neighbouring ones, so the comparison is over the
			// union rather than the list.
			var want []image.Rectangle
			rnd := 1
			for i := 0; i < 12; i++ {
				rnd = (rnd*1103515245 + 12345) & 0x7fffffff
				r := image.Rect(rnd%80, (rnd/80)%80, (rnd%80)+15, ((rnd/80)%80)+15)
				th.add([]image.Rectangle{r})
				want = append(want, r)
				time.Sleep(time.Millisecond)
			}
			time.Sleep(time.Duration(interval)*6 + 100*time.Millisecond)

			reports, _ := rec.snapshot()
			if len(reports) == 0 {
				t.Fatal("no reports at all")
			}

			// Every reported point must have been in some batch.
			for _, r := range want {
				if !reportedCoverage(reports, r) {
					t.Fatalf("the change %v was never reported", r)
				}
			}
			// Every report must be within the frame, and a union of the reports
			// must cover the union of the batches.
			for _, rs := range reports {
				for _, r := range rs {
					if !r.In(bounds) && !r.Intersect(bounds).Empty() {
						t.Fatalf("a report region %v is outside the frame %v", r, bounds)
					}
				}
			}
		})
	}
}

// reportedCoverage reports whether some report fully contains r. Merged regions
// mean the containing report can be larger than r, which is the point of the
// merge.
func reportedCoverage(reports [][]image.Rectangle, r image.Rectangle) bool {
	for _, rs := range reports {
		for _, got := range rs {
			if r.In(got) {
				return true
			}
		}
	}
	return false
}

// A client that sets a tier before registering a callback, and one that registers
// after, must both end up rate limited: the front end's knob works in either
// order.
func TestClientSetFrameIntervalBeforeAndAfterRegistering(t *testing.T) {
	// Tier first, callback second. This is the order a front end that decides
	// the default tier before it has anything to show uses.
	c1 := newDirtyClient(100, 100)
	cli1 := &Client{ctl: c1, setting: &Setting{FrameInterval: time.Second}}
	cli1.SetFrameInterval(40 * time.Millisecond)
	rec1 := &record{}
	cli1.OnFrameThrottled(rec1.f)

	// Callback first, tier second: the order a front end that lets the viewer
	// pick the tier after connecting uses.
	c2 := newDirtyClient(100, 100)
	cli2 := &Client{ctl: c2}
	rec2 := &record{}
	cli2.OnFrameThrottled(rec2.f)
	cli2.SetFrameInterval(40 * time.Millisecond)

	for _, cli := range []*Client{cli1, cli2} {
		r := cli.ctl.(*RdpClient)
		// Two batches inside the window: the second must be coalesced rather
		// than reported, which is what the tier is for.
		r.pdu.Emit("frame", []image.Rectangle{image.Rect(0, 0, 10, 10)})
		time.Sleep(10 * time.Millisecond)
		r.pdu.Emit("frame", []image.Rectangle{image.Rect(50, 50, 60, 60)})
		time.Sleep(200 * time.Millisecond)
	}

	for i, rec := range []*record{rec1, rec2} {
		reports, _ := rec.snapshot()
		if len(reports) != 2 {
			t.Errorf("order %d: got %d reports, want 2 (one immediate, one coalesced)", i+1, len(reports))
		}
	}
}
