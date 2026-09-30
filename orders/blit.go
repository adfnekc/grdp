package orders

import (
	"image"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/glog"
)

// Buffer returns the screen pixels themselves, as BGRA, top down, with a stride
// of four bytes per pixel and no padding. Unlike Pixels this does not copy, so
// it is the one to use for handing a frame to an encoder; the slice stays valid
// until the next Draw, Blit or Resize, and reading it while another goroutine
// draws is a data race.
func (s *Screen) Buffer() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pixels
}

// Blit copies a decoded bitmap into the screen at dst, converting the source
// pixels to the screen's BGRA, and returns the area that changed, clipped to the
// screen. An empty result means nothing landed.
//
// This is the bitmap update path's way in. Drawing orders do not come through
// here: they read the screen and the caches directly.
//
// srcPP is the source bytes per pixel, which is what client.Bpp returns: one,
// two, three or four. Three and four byte pixels are BGR and BGRA. Two byte
// pixels are RGB565 read most significant byte first, which is the byte order the
// RLE decoder emits them in (core/rle.go decompress2 finishes with PutUint16BE),
// and not the little endian order of a DIB. One byte pixels are palette indices,
// and the palette is not tracked here, so they are counted as unsupported rather
// than drawn in whatever colour the index happens to be: a wrong picture that
// looks plausible is worse than a missing one.
func (s *Screen) Blit(src []byte, srcW, srcH, srcPP int, dst image.Rectangle) image.Rectangle {
	if srcW <= 0 || srcH <= 0 {
		return image.Rectangle{}
	}
	if srcPP < 1 || srcPP > 4 {
		return image.Rectangle{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if srcPP == 1 {
		s.note("bitmap:8bpp-palette")
		return image.Rectangle{}
	}

	// The source rectangle is the bitmap's own size, and the destination is
	// where it goes. Both are clipped before anything is read.
	r := dst.Intersect(image.Rect(0, 0, s.width, s.height))
	if r.Empty() {
		return image.Rectangle{}
	}
	// Bytes needed for the pixels that will actually be read, so that a source
	// that runs short stops the copy instead of reading past the end.
	rows := r.Dy()
	cols := r.Dx()
	if rows > srcH {
		rows = srcH
	}
	if cols > srcW {
		cols = srcW
	}
	if rows <= 0 || cols <= 0 {
		return image.Rectangle{}
	}
	// No attempt is made to reconcile a declared width with the size of the
	// buffer it arrived with. Guessing a different width would invent a
	// geometry the server never sent and misplace every pixel after the first
	// row; the per pixel bound below simply stops where the data stops, which
	// is the same "keep what decoded" policy the RLE decoder uses.

	var written image.Rectangle
	for row := 0; row < rows; row++ {
		so := row * srcW * srcPP
		do := ((r.Min.Y+row)*s.width + r.Min.X) * 4
		n := cols
		for col := 0; col < n; col++ {
			if so+srcPP > len(src) || do+4 > len(s.pixels) {
				n = col
				break
			}
			switch srcPP {
			case 2:
				r8, g8, b8 := core.RGB565ToRGB(core.Uint16BE(src[so], src[so+1]))
				s.pixels[do] = b8
				s.pixels[do+1] = g8
				s.pixels[do+2] = r8
				s.pixels[do+3] = 0xff
			case 3:
				copy(s.pixels[do:do+3], src[so:so+3])
				s.pixels[do+3] = 0xff
			default:
				copy(s.pixels[do:do+4], src[so:so+4])
				s.pixels[do+3] = 0xff
			}
			so += srcPP
			do += 4
		}
		if n < cols {
			glog.Warnf("bitmap: source ran out %d pixels into row %d", n, row)
		}
		if n > 0 {
			written = union(written, image.Rect(r.Min.X, r.Min.Y+row, r.Min.X+n, r.Min.Y+row+1))
		}
	}
	return written
}
