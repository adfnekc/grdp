package client

import (
	"image"
	"testing"
)

func TestAddDirtyMergesAdjacentRectangles(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	// Two rectangles that share an edge describe one region. A caller encoding
	// them separately sends the seam twice and gets a visible tile boundary.
	dirty := addDirty(nil, image.Rect(10, 10, 20, 20), bounds)
	dirty = addDirty(dirty, image.Rect(20, 10, 30, 20), bounds)

	if len(dirty) != 1 {
		t.Fatalf("adjacent rectangles did not merge: %v", dirty)
	}
	if want := image.Rect(10, 10, 30, 20); dirty[0] != want {
		t.Errorf("merged to %v, want %v", dirty[0], want)
	}
}

func TestAddDirtyMergesOverlappingRectangles(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	dirty := addDirty(nil, image.Rect(10, 10, 30, 30), bounds)
	dirty = addDirty(dirty, image.Rect(20, 20, 40, 40), bounds)

	if len(dirty) != 1 {
		t.Fatalf("overlapping rectangles did not merge: %v", dirty)
	}
	if want := image.Rect(10, 10, 40, 40); dirty[0] != want {
		t.Errorf("merged to %v, want %v", dirty[0], want)
	}
}

// Merging one rectangle can bring another into range, which is why the merge
// repeats rather than making a single pass. A bridge rectangle between two
// islands has to collapse all three, not two and then leave the third.
func TestAddDirtyMergesTransitively(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 1000)
	dirty := addDirty(nil, image.Rect(0, 0, 10, 10), bounds)
	dirty = addDirty(dirty, image.Rect(500, 500, 510, 510), bounds)
	if len(dirty) != 2 {
		t.Fatalf("expected two separate regions, got %v", dirty)
	}
	dirty = addDirty(dirty, image.Rect(10, 0, 500, 510), bounds)

	if len(dirty) != 1 {
		t.Fatalf("bridge rectangle did not merge everything: %v", dirty)
	}
	if want := image.Rect(0, 0, 510, 510); dirty[0] != want {
		t.Errorf("merged to %v, want %v", dirty[0], want)
	}
}

// Once a merged region covers most of the frame, describing it as a list of
// rectangles stops being worth the bookkeeping on either side.
func TestAddDirtyCollapsesToWholeFrame(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	// One rectangle covering 70% of the frame.
	dirty := addDirty(nil, image.Rect(0, 0, 100, 70), bounds)

	if len(dirty) != 1 {
		t.Fatalf("expected one region, got %v", dirty)
	}
	if dirty[0] != bounds {
		t.Errorf("70%% of the frame was not collapsed to the whole frame: %v", dirty[0])
	}
}

// Just under the ratio it stays a rectangle, so the collapse is not simply
// "always return the frame".
func TestAddDirtyKeepsASmallRectangleSmall(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	dirty := addDirty(nil, image.Rect(0, 0, 100, 50), bounds)

	if len(dirty) != 1 {
		t.Fatalf("expected one region, got %v", dirty)
	}
	if dirty[0] == bounds {
		t.Errorf("half the frame was collapsed to the whole frame, which is more to encode")
	}
}

func TestAddDirtyClipsToBounds(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	// A rectangle running off the right and bottom edges, which a server is
	// free to send.
	dirty := addDirty(nil, image.Rect(90, 90, 200, 200), bounds)

	if len(dirty) != 1 {
		t.Fatalf("expected one region, got %v", dirty)
	}
	if want := image.Rect(90, 90, 100, 100); dirty[0] != want {
		t.Errorf("clipped to %v, want %v", dirty[0], want)
	}
}

// A rectangle that lies entirely outside changes nothing, and must not be
// reported: a caller told to encode a region past the edge has nothing to read.
func TestAddDirtyDropsRectanglesOutsideBounds(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	for _, r := range []image.Rectangle{
		image.Rect(200, 200, 300, 300), // right and below
		image.Rect(-50, -50, -10, -10), // left and above
		image.Rect(-50, 10, 0, 20),     // touching the left edge with no overlap
	} {
		if dirty := addDirty(nil, r, bounds); len(dirty) != 0 {
			t.Errorf("rectangle %v outside the frame was kept: %v", r, dirty)
		}
	}
}

// An inverted or zero-sized rectangle is a malformed update, not a giant one.
//
// These are built as struct literals on purpose: image.Rect swaps its arguments
// when the first corner is greater than the second, so it cannot produce an
// inverted rectangle at all. Only a literal (or arithmetic on points) can, which
// is why the check exists but cannot be provoked through the usual helpers.
func TestAddDirtyDropsInvertedRectangles(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	for _, r := range []image.Rectangle{
		{Min: image.Point{50, 50}, Max: image.Point{10, 10}},
		{Min: image.Point{10, 10}, Max: image.Point{-10, 20}},
		image.Rect(10, 10, 10, 20), // zero width
		image.Rect(10, 10, 20, 10), // zero height
	} {
		if dirty := addDirty(nil, r, bounds); len(dirty) != 0 {
			t.Errorf("inverted rectangle %v was kept: %v", r, dirty)
		}
	}
}

func TestAddDirtyBelowRatioStaysASmallList(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 1000)
	dirty := addDirty(nil, image.Rect(0, 0, 10, 10), bounds)
	dirty = addDirty(dirty, image.Rect(500, 500, 510, 510), bounds)
	dirty = addDirty(dirty, image.Rect(0, 500, 10, 510), bounds)

	if len(dirty) != 3 {
		t.Errorf("three separate small regions became %v", dirty)
	}
}
