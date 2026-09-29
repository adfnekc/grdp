package rdpgfx

import (
	"bytes"
	"testing"
)

func u64(v uint64) []byte { return append(u32(uint32(v)), u32(uint32(v>>32))...) }

// newSurface feeds a CreateSurface for a surface of the given native size.
func newSurface(t *testing.T, c *GfxClient, id, w, h uint16) {
	t.Helper()
	feed(c, pdu(cmdCreateSurface, append(append(u16(id), u16(w)...), append(u16(h), 0x20)...)))
}

// wireUncompressed fills a surface with raw BGRA pixels in one Rectangle.
func wireUncompressed(t *testing.T, c *GfxClient, id, left, top, right, bottom uint16, pixels []byte) {
	t.Helper()
	body := append(append(append([]byte{}, u16(id)...), u16(codecUncompressed)...), 0x20)
	body = append(body, rect16(left, top, right, bottom)...)
	body = append(body, u32(uint32(len(pixels)))...)
	body = append(body, pixels...)
	feed(c, pdu(cmdWireToSurface1, body))
}

// TestMapSurfaceToScaledOutputAppliesScale is the one that matters: a surface
// mapped to the output at a target size has to be resampled to that size, or it
// is composited at the wrong size. The 2x factor makes the expected image
// exact, so a wrong mapping (off by a row or a column) fails rather than merely
// producing something.
func TestMapSurfaceToScaledOutputAppliesScale(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)

	pixels := []byte{
		1, 2, 3, 255, 4, 5, 6, 255,
		7, 8, 9, 255, 10, 11, 12, 255,
	}
	wireUncompressed(t, c, 1, 0, 0, 2, 2, pixels)

	// surfaceId(2) reserved(2) outputOriginX(4) outputOriginY(4)
	// targetWidth(4) targetHeight(4). Scale the 2x2 surface to 4x4 at (30,40).
	place := append(append(u16(1), u16(0)...), append(u32(30), u32(40)...)...)
	place = append(place, u32(4)...)
	place = append(place, u32(4)...)
	feed(c, pdu(cmdMapSurfaceToScaledOutput, place))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	if s == nil {
		t.Fatal("surface was not created")
	}

	if x, y := s.Origin(); x != 30 || y != 40 {
		t.Errorf("origin is (%d,%d), want (30,40)", x, y)
	}

	p := s.Placement()
	if p.Windowed || p.TargetWidth != 4 || p.TargetHeight != 4 {
		t.Fatalf("placement = %+v, want a 4x4 output scale", p)
	}

	got, w, h := s.CompositePixels()
	if w != 4 || h != 4 {
		t.Fatalf("composite size is %dx%d, want 4x4", w, h)
	}
	// Nearest neighbour at an exact 2x factor duplicates each source pixel,
	// so the expected image is exact.
	top := []byte{1, 2, 3, 255, 1, 2, 3, 255, 4, 5, 6, 255, 4, 5, 6, 255}
	bottom := []byte{7, 8, 9, 255, 7, 8, 9, 255, 10, 11, 12, 255, 10, 11, 12, 255}
	want := append(append(append(append([]byte{}, top...), top...), bottom...), bottom...)
	if !bytes.Equal(got, want) {
		t.Fatalf("resampled pixels =\n%v\nwant\n%v", got, want)
	}
}

// A downscale picks exact source pixels under nearest neighbour, so the result
// can be checked as values rather than as "it ran".
func TestMapSurfaceToScaledOutputDownscale(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 4, 4)

	// Red channel is the pixel index; the rest is fixed, to make each pixel
	// identifiable.
	pixels := make([]byte, 0, 4*4*4)
	for i := 0; i < 4*4; i++ {
		pixels = append(pixels, byte(i), 0, 0, 255)
	}
	wireUncompressed(t, c, 1, 0, 0, 4, 4, pixels)

	// 4x4 down to 2x2: nearest neighbour takes columns and rows 0 and 2, i.e.
	// indices 0, 2, 8, 10.
	place := append(append(u16(1), u16(0)...), append(u32(0), u32(0)...)...)
	place = append(place, u32(2)...)
	place = append(place, u32(2)...)
	feed(c, pdu(cmdMapSurfaceToScaledOutput, place))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	got, w, h := s.CompositePixels()
	if w != 2 || h != 2 {
		t.Fatalf("composite size is %dx%d, want 2x2", w, h)
	}
	want := []byte{0, 0, 0, 255, 2, 0, 0, 255, 8, 0, 0, 255, 10, 0, 0, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("downscaled pixels = %v, want %v", got, want)
	}
}

// A target size equal to the native size changes nothing and must not be sent
// through the resampler.
func TestMapSurfaceToScaledOutputSameSize(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)
	pixels := []byte{
		1, 2, 3, 255, 4, 5, 6, 255,
		7, 8, 9, 255, 10, 11, 12, 255,
	}
	wireUncompressed(t, c, 1, 0, 0, 2, 2, pixels)

	place := append(append(u16(1), u16(0)...), append(u32(5), u32(6)...)...)
	place = append(place, u32(2)...)
	place = append(place, u32(2)...)
	feed(c, pdu(cmdMapSurfaceToScaledOutput, place))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	got, w, h := s.CompositePixels()
	if w != 2 || h != 2 || !bytes.Equal(got, pixels) {
		t.Fatalf("same size scale changed the image: %dx%d %v", w, h, got)
	}
}

// The target dimensions are uint32 on the wire. Every one that is zero or
// beyond the bound must be refused, or resampling allocates whatever the server
// asked for.
func TestScaledTargetIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h uint32
	}{
		{"zero width", 0, 4},
		{"zero height", 4, 0},
		{"oversized width", maxScaledDimension + 1, 4},
		{"oversized height", 4, maxScaledDimension + 1},
		{"both maximal", 0xFFFFFFFF, 0xFFFFFFFF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t)
			newSurface(t, c, 1, 2, 2)

			place := append(append(u16(1), u16(0)...), append(u32(0), u32(0)...)...)
			place = append(place, u32(tc.w)...)
			place = append(place, u32(tc.h)...)
			if err := c.handle(headerBuf{cmdID: cmdMapSurfaceToScaledOutput}, place); err == nil {
				t.Fatalf("scaled output target %dx%d was accepted", tc.w, tc.h)
			}

			// The same bound applies to the scaled window form.
			win := u16(1)
			win = append(win, u64(7)...)
			win = append(win, u32(8)...)
			win = append(win, u32(8)...)
			win = append(win, u32(tc.w)...)
			win = append(win, u32(tc.h)...)
			if err := c.handle(headerBuf{cmdID: cmdMapSurfaceToScaledWindow}, win); err == nil {
				t.Fatalf("scaled window target %dx%d was accepted", tc.w, tc.h)
			}

			c.mu.Lock()
			s := c.surfaces[1]
			c.mu.Unlock()
			if p := s.Placement(); p.TargetWidth != 0 || p.TargetHeight != 0 || p.Windowed {
				t.Fatalf("a rejected target was still recorded: %+v", p)
			}
		})
	}
}

// The bound is inclusive: a target exactly at maxScaledDimension is accepted,
// so legitimate large outputs are not refused. CompositePixels is deliberately
// not called here, since its resample buffer would be large.
func TestScaledTargetBoundIsInclusive(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 1, 1)

	place := append(append(u16(1), u16(0)...), append(u32(0), u32(0)...)...)
	place = append(place, u32(maxScaledDimension)...)
	place = append(place, u32(1)...)
	if err := c.handle(headerBuf{cmdID: cmdMapSurfaceToScaledOutput}, place); err != nil {
		t.Fatalf("a target at the bound was rejected: %v", err)
	}

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	if p := s.Placement(); p.TargetWidth != maxScaledDimension || p.TargetHeight != 1 {
		t.Fatalf("placement = %+v, want target %dx1", p, maxScaledDimension)
	}
}

// TestMapSurfaceToWindowRecordsButDoesNotResample: the destination is a window,
// which this client cannot composite into, so the placement is recorded and
// exposed and the pixels are left at their native size.
func TestMapSurfaceToWindowRecordsButDoesNotResample(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)

	// surfaceId(2) windowId(8) mappedWidth(4) mappedHeight(4).
	body := u16(1)
	body = append(body, u64(0x1122334455667788)...)
	body = append(body, u32(640)...)
	body = append(body, u32(480)...)
	feed(c, pdu(cmdMapSurfaceToWindow, body))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	if s == nil {
		t.Fatal("surface was not created")
	}
	p := s.Placement()
	if !p.Windowed {
		t.Fatalf("windowed mapping was not recorded: %+v", p)
	}
	if p.WindowID != 0x1122334455667788 {
		t.Errorf("windowId = 0x%016x, want 0x1122334455667788", p.WindowID)
	}
	if p.MappedWidth != 640 || p.MappedHeight != 480 {
		t.Errorf("mapped size is %dx%d, want 640x480", p.MappedWidth, p.MappedHeight)
	}
	if p.TargetWidth != 0 || p.TargetHeight != 0 {
		t.Errorf("plain window mapping carries a target %dx%d", p.TargetWidth, p.TargetHeight)
	}

	px, w, h := s.CompositePixels()
	if w != 2 || h != 2 || len(px) != 16 {
		t.Fatalf("windowed surface composites as %dx%d (%d bytes), want native 2x2", w, h, len(px))
	}
}

// A scaled window carries both the mapped size and the target size. Both are
// recorded, and neither is applied.
func TestMapSurfaceToScaledWindowRecordsWindowAndTarget(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)

	// surfaceId(2) windowId(8) mappedWidth(4) mappedHeight(4) targetWidth(4)
	// targetHeight(4).
	body := u16(1)
	body = append(body, u64(0x0F1E2D3C4B5A6978)...)
	body = append(body, u32(64)...)
	body = append(body, u32(48)...)
	body = append(body, u32(128)...)
	body = append(body, u32(96)...)
	feed(c, pdu(cmdMapSurfaceToScaledWindow, body))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	if s == nil {
		t.Fatal("surface was not created")
	}
	p := s.Placement()
	if !p.Windowed || p.WindowID != 0x0F1E2D3C4B5A6978 {
		t.Fatalf("placement = %+v", p)
	}
	if p.MappedWidth != 64 || p.MappedHeight != 48 {
		t.Errorf("mapped size = %dx%d, want 64x48", p.MappedWidth, p.MappedHeight)
	}
	if p.TargetWidth != 128 || p.TargetHeight != 96 {
		t.Errorf("target size = %dx%d, want 128x96", p.TargetWidth, p.TargetHeight)
	}
	if _, w, h := s.CompositePixels(); w != 2 || h != 2 {
		t.Errorf("scaled window surface composites as %dx%d, want native 2x2", w, h)
	}
}

// A window mapping carries no desktop origin, so it must not move a surface
// that was already placed on the output to the top left corner.
func TestWindowMappingKeepsPreviousOrigin(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)

	place := append(append(u16(1), u16(0)...), append(u32(100), u32(200)...)...)
	feed(c, pdu(cmdMapSurfaceToOutput, place))
	feed(c, pdu(cmdMapSurfaceToWindow, append(append(u16(1), u64(7)...), append(u32(4), u32(4)...)...)))

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()
	if x, y := s.Origin(); x != 100 || y != 200 {
		t.Errorf("origin is (%d,%d), want the previous (100,200)", x, y)
	}
	p := s.Placement()
	if !p.Windowed || p.WindowID != 7 {
		t.Errorf("placement = %+v, want the window kept alongside the origin", p)
	}
	if p.X != 100 || p.Y != 200 {
		t.Errorf("placement origin is (%d,%d), want (100,200)", p.X, p.Y)
	}
}

// Every truncation of every body must be refused. A parser that reads past what
// it was given is exactly the trap this project has been caught by before; here
// it would read whatever follows on the channel.
func TestPlacementParsersRejectTruncatedBodies(t *testing.T) {
	full := []struct {
		name string
		cmd  uint16
		body []byte
	}{
		{"map surface to output", cmdMapSurfaceToOutput,
			append(append(u16(1), u16(0)...), append(u32(1), u32(2)...)...)},
		{"map surface to scaled output", cmdMapSurfaceToScaledOutput,
			append(append(u16(1), u16(0)...), append(append(u32(1), u32(2)...), append(u32(4), u32(4)...)...)...)},
		{"map surface to window", cmdMapSurfaceToWindow,
			append(append(u16(1), u64(7)...), append(u32(4), u32(4)...)...)},
		{"map surface to scaled window", cmdMapSurfaceToScaledWindow,
			append(append(u16(1), u64(7)...), append(append(u32(4), u32(4)...), append(u32(8), u32(8)...)...)...)},
	}
	for _, tc := range full {
		t.Run(tc.name, func(t *testing.T) {
			for n := 0; n < len(tc.body); n++ {
				c, _ := newTestClient(t)
				newSurface(t, c, 1, 2, 2)
				if err := c.handle(headerBuf{cmdID: tc.cmd}, tc.body[:n]); err == nil {
					t.Fatalf("a %d byte body was accepted, the layout needs %d", n, len(tc.body))
				}
			}
		})
	}
}

// A rejected placement must not stop the rest of the payload. The dispatcher
// frames by length, so the following command has to survive.
func TestRejectedPlacementDoesNotStopTheBatch(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)

	// A window mapping ten bytes into its eighteen byte layout.
	bad := pdu(cmdMapSurfaceToWindow, append(u16(1), make([]byte, 8)...))
	good := pdu(cmdDeleteSurface, u16(1))
	feed(c, append(bad, good...))

	c.mu.Lock()
	n := len(c.surfaces)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("the command after a rejected placement was not processed, %d surfaces left", n)
	}
}

// A mapping for a surface that does not exist is an error, whichever order it
// arrives in.
func TestPlacementsRejectUnknownSurface(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  uint16
		body []byte
	}{
		{"map surface to output", cmdMapSurfaceToOutput,
			append(append(u16(9), u16(0)...), append(u32(1), u32(2)...)...)},
		{"map surface to scaled output", cmdMapSurfaceToScaledOutput,
			append(append(u16(9), u16(0)...), append(append(u32(1), u32(2)...), append(u32(4), u32(4)...)...)...)},
		{"map surface to window", cmdMapSurfaceToWindow,
			append(append(u16(9), u64(7)...), append(u32(4), u32(4)...)...)},
		{"map surface to scaled window", cmdMapSurfaceToScaledWindow,
			append(append(u16(9), u64(7)...), append(append(u32(4), u32(4)...), append(u32(8), u32(8)...)...)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t)
			if err := c.handle(headerBuf{cmdID: tc.cmd}, tc.body); err == nil {
				t.Fatalf("a mapping for an unknown surface was accepted")
			}
		})
	}
}

// A surface with no placement composites at its native size and the desktop
// origin, so a consumer always has something to draw.
func TestCompositePixelsWithoutPlacementIsNative(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 2, 2)
	pixels := []byte{
		1, 2, 3, 255, 4, 5, 6, 255,
		7, 8, 9, 255, 10, 11, 12, 255,
	}
	wireUncompressed(t, c, 1, 0, 0, 2, 2, pixels)

	c.mu.Lock()
	s := c.surfaces[1]
	c.mu.Unlock()

	got, w, h := s.CompositePixels()
	if w != 2 || h != 2 || !bytes.Equal(got, pixels) {
		t.Fatalf("got %dx%d %v, want the native 2x2 image", w, h, got)
	}
	if x, y := s.Origin(); x != 0 || y != 0 {
		t.Errorf("origin is (%d,%d), want (0,0)", x, y)
	}
}
