package orders

import (
	"bytes"
	"image"
	"testing"
)

// newTestScreen builds a screen with a known background, so that a test can
// tell "wrote this" from "left it alone".
func newTestScreen(w, h int) *Screen {
	s := NewScreen(w, h)
	for i := 0; i < len(s.pixels); i += 4 {
		s.pixels[i] = 1
		s.pixels[i+1] = 2
		s.pixels[i+2] = 3
		s.pixels[i+3] = 4
	}
	return s
}

func TestBlit32BitCopiesAndMakesOpaque(t *testing.T) {
	s := newTestScreen(4, 4)
	// BGRX, with a deliberately non-opaque fourth byte: RDP does not carry
	// per-pixel alpha for the desktop, so the framebuffer must not inherit
	// whatever the padding byte happened to be.
	src := []byte{10, 20, 30, 0}
	r := s.Blit(src, 1, 1, 4, image.Rect(1, 1, 2, 2))

	if want := image.Rect(1, 1, 2, 2); r != want {
		t.Fatalf("changed area %v, want %v", r, want)
	}
	if got := pixelAt(t, s, 1, 1); !bytes.Equal(got, []byte{10, 20, 30, 255}) {
		t.Errorf("pixel = %v, want [10 20 30 255]", got)
	}
	// And nothing else moved.
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("a neighbouring pixel was written: %v", got)
	}
}

func TestBlit24BitExpandsToBGRA(t *testing.T) {
	s := newTestScreen(2, 2)
	r := s.Blit([]byte{0x11, 0x22, 0x33}, 1, 1, 3, image.Rect(0, 0, 1, 1))

	if r.Empty() {
		t.Fatal("nothing was written")
	}
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, []byte{0x11, 0x22, 0x33, 255}) {
		t.Errorf("pixel = %v, want [17 34 51 255]", got)
	}
}

// The 16bpp values here are the ones core's expansion produces, verified
// separately: the expansion truncates, so full scale is 248.
func TestBlit16BitIsRGB565(t *testing.T) {
	cases := []struct {
		name string
		// The wire order is most significant byte first: the RLE decoder
		// emits 16bpp pixels with PutUint16BE, so this is not the byte order
		// a DIB would use.
		word uint16
		want []byte
	}{
		{"red", 0xF800, []byte{0, 0, 248, 255}},
		{"green", 0x07E0, []byte{0, 252, 0, 255}},
		{"blue", 0x001F, []byte{248, 0, 0, 255}},
		{"white", 0xFFFF, []byte{248, 252, 248, 255}},
	}
	for _, c := range cases {
		s := newTestScreen(1, 1)
		s.Blit([]byte{byte(c.word >> 8), byte(c.word & 0xFF)}, 1, 1, 2, image.Rect(0, 0, 1, 1))
		if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, c.want) {
			t.Errorf("%s: pixel = %v, want %v", c.name, got, c.want)
		}
	}
}

// An 8bpp bitmap is palette indices and the palette is not tracked, so drawing
// the index as if it were a colour would be plausible and wrong. Nothing is
// written and the order is counted as unsupported.
func TestBlit8BitWritesNothingAndIsCounted(t *testing.T) {
	s := newTestScreen(2, 2)
	r := s.Blit([]byte{5, 6, 7, 8}, 2, 2, 1, image.Rect(0, 0, 2, 2))

	if !r.Empty() {
		t.Errorf("8bpp bitmap wrote %v", r)
	}
	if got := pixelAt(t, s, 0, 0); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("8bpp bitmap modified the screen: %v", got)
	}
	if n := s.Unsupported()["bitmap:8bpp-palette"]; n != 1 {
		t.Errorf("unsupported count = %d, want 1", n)
	}
}

func TestBlitClipsToTheScreen(t *testing.T) {
	s := newTestScreen(4, 4)
	// A 4x4 bitmap whose destination runs off the screen: only the part that
	// fits may be written, and only that part reported.
	src := make([]byte, 4*4*4)
	for i := range src {
		src[i] = 0x77
	}
	r := s.Blit(src, 4, 4, 4, image.Rect(2, 2, 6, 6))

	if want := image.Rect(2, 2, 4, 4); r != want {
		t.Fatalf("changed area %v, want %v", r, want)
	}
	if got := pixelAt(t, s, 3, 3); !bytes.Equal(got, []byte{0x77, 0x77, 0x77, 255}) {
		t.Errorf("visible corner = %v", got)
	}
}

func TestBlitEntirelyOffScreenWritesNothing(t *testing.T) {
	s := newTestScreen(4, 4)
	for _, dst := range []image.Rectangle{
		image.Rect(10, 10, 20, 20),
		image.Rect(-10, -10, -1, -1),
		image.Rect(0, 0, 0, 0),
	} {
		if r := s.Blit(make([]byte, 64), 4, 4, 4, dst); !r.Empty() {
			t.Errorf("destination %v wrote %v", dst, r)
		}
	}
}

// A source that runs out mid-row must stop, report what it did write, and not
// read past the end. This is the shape of the bug that turns a malformed update
// into a panic in production rather than in a test.
func TestBlitStopsWhenTheSourceRunsOut(t *testing.T) {
	s := newTestScreen(8, 4)
	// Declares 4x4 pixels but carries only two rows.
	src := make([]byte, 4*2*4)
	for i := range src {
		src[i] = 0x55
	}
	r := s.Blit(src, 4, 4, 4, image.Rect(0, 0, 4, 4))

	if want := image.Rect(0, 0, 4, 2); r != want {
		t.Fatalf("changed area %v, want %v", r, want)
	}
	if got := pixelAt(t, s, 0, 2); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("the short source wrote past its data: %v", got)
	}
}

// A declared width larger than the buffer it came with is a malformed update.
// Blit must not invent a different width to make it fit, and must not walk off
// the end reading rows at the declared stride: it writes the pixels that are
// really there and stops.
func TestBlitDoesNotTrustAWidthLargerThanTheBuffer(t *testing.T) {
	s := newTestScreen(16, 4)
	// The first row of four pixels is genuinely present; the three rows the
	// declaration implies are not.
	src := []byte{1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4}
	r := s.Blit(src, 100, 4, 4, image.Rect(0, 0, 4, 4))

	if want := image.Rect(0, 0, 4, 1); r != want {
		t.Fatalf("changed area %v, want %v", r, want)
	}
	if got := pixelAt(t, s, 3, 0); !bytes.Equal(got, []byte{4, 4, 4, 255}) {
		t.Errorf("last pixel of the row = %v", got)
	}
	if got := pixelAt(t, s, 0, 1); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("a row past the data was written: %v", got)
	}
}

func TestBlitRejectsNonsensePixelSizes(t *testing.T) {
	s := newTestScreen(4, 4)
	for _, pp := range []int{0, -1, 5, 8} {
		if r := s.Blit([]byte{1, 2, 3, 4}, 1, 1, pp, image.Rect(0, 0, 1, 1)); !r.Empty() {
			t.Errorf("%d bytes per pixel wrote %v", pp, r)
		}
	}
}
