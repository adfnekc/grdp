package client

import (
	"image"

	"github.com/adfnekc/grdp/orders"
)

// dirtyFrameRatio is how much of the frame a merged dirty region may cover
// before it is reported as the whole frame instead. A caller that has to decide
// between "encode these rectangles" and "encode everything" should not have to
// make that judgement itself, and it is a judgement this package is in a better
// position to make.
const dirtyFrameRatio = 0.6

// Framebuffer is where decoded pixels land, whichever way the server chose to
// draw them.
//
// It exists so that a caller does not have to care whether the session is
// running on bitmap updates, drawing orders or the graphics channel: the pixels
// end up here either way, and OnFrame says what changed. A framebuffer exists
// for every session, whether or not Setting.EnableOrders was set, because the
// bitmap path composites into it too. It is reachable from Client.Framebuffer
// once Login has been called.
//
// The pixels are BGRA, top down, with a stride of four bytes per pixel and no
// padding between rows, so the byte at offset y*Stride()+x*4 is the blue channel
// of the pixel at (x, y). The alpha byte is 255 everywhere: RDP does not carry
// per-pixel alpha for the desktop.
type Framebuffer struct {
	screen *orders.Screen
}

// Bounds is the whole framebuffer, which is the desktop size. A zero rectangle
// means there is no framebuffer yet.
func (f *Framebuffer) Bounds() image.Rectangle {
	if f == nil || f.screen == nil {
		return image.Rectangle{}
	}
	w, h := f.screen.Size()
	return image.Rect(0, 0, w, h)
}

// Size is Bounds().Dx() and Bounds().Dy().
func (f *Framebuffer) Size() (w, h int) {
	if f == nil || f.screen == nil {
		return 0, 0
	}
	return f.screen.Size()
}

// Stride is the number of bytes between the start of one row and the next.
func (f *Framebuffer) Stride() int {
	w, _ := f.Size()
	return w * 4
}

// Pix returns the pixel buffer itself, not a copy, so that handing a frame to an
// encoder costs nothing. It stays valid, with the frame as drawn, until the next
// OnFrame callback, and it is only safe to read from the callback's own
// goroutine while the session is running. Copy it if it is needed for longer.
//
// Callers must not write to it.
func (f *Framebuffer) Pix() []byte {
	if f == nil || f.screen == nil {
		return nil
	}
	return f.screen.Buffer()
}

// MergeDirty adds r to a list of changed rectangles, merging it with anything
// it overlaps or shares an edge with and collapsing to the whole of bounds once
// the result covers most of it.
//
// It is what the dirty regions OnFrame reports were built with, exported for a
// caller that accumulates them across frames: a gateway pacing its encoding on a
// timer has to fold each frame's regions into what it has not sent yet, and
// doing that by simply appending would send the same overlapping rectangles
// again and again.
func MergeDirty(dirty []image.Rectangle, r, bounds image.Rectangle) []image.Rectangle {
	return addDirty(dirty, r, bounds)
}

// addDirty adds r to the list of changed rectangles, merging it with anything it
// overlaps or shares an edge with, and collapsing to the whole frame once the
// result covers most of it.
//
// Merging repeats until nothing more is absorbed, because absorbing one
// rectangle can bring another into range: two separate updates straddled by a
// third become one region.
func addDirty(dirty []image.Rectangle, r, bounds image.Rectangle) []image.Rectangle {
	// An inverted or zero-sized rectangle contains no points, so Empty is true
	// for it and it is dropped here. That check has to come before anything
	// normalises the value: Canon would swap the corners of a malformed
	// rectangle and turn it into a plausible looking one, and the caller would
	// then be told to encode a region the server never drew.
	if r.Empty() {
		return dirty
	}
	r = r.Intersect(bounds)
	if r.Empty() {
		return dirty
	}

	for merged := true; merged; {
		merged = false
		for i := 0; i < len(dirty); i++ {
			if !touches(dirty[i], r) {
				continue
			}
			r = r.Union(dirty[i])
			dirty = append(dirty[:i], dirty[i+1:]...)
			merged = true
			break
		}
	}

	if float64(rectArea(r)) > dirtyFrameRatio*float64(rectArea(bounds)) {
		return []image.Rectangle{bounds}
	}
	return append(dirty, r)
}

// touches reports whether a and b overlap or share an edge. Growing one of them
// by a pixel makes adjacency an overlap, which is what makes a row of separate
// updates merge into one region. Rectangles that meet only at a corner also
// count, which costs nothing.
func touches(a, b image.Rectangle) bool {
	return a.Inset(-1).Overlaps(b)
}

// rectArea is the pixel count of r, or zero when it is empty.
func rectArea(r image.Rectangle) int {
	r = r.Canon()
	if r.Empty() {
		return 0
	}
	return r.Dx() * r.Dy()
}
