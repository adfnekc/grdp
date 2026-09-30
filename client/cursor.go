package client

import (
	"image"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// Cursor is the pointer as the server wants it drawn.
//
// A gateway needs this because the pointer is not part of the desktop image: it
// is drawn over it, at a position the server sends separately, and a browser
// cannot be told to draw it out of a bitmap update. So the two halves arrive
// here rather than in Framebuffer.
type Cursor struct {
	// Image is the shape with the AND and XOR masks already applied, so that
	// alpha is what decides what shows through. It is nil for a system cursor,
	// which has no pixels.
	Image *image.RGBA

	// Width and Height are the shape's size, and are 0 for a system cursor.
	Width  int
	Height int

	// Hotspot is the point within the image that sits on the position
	// reported by OnCursorPos. It is always inside the image for a decoded
	// shape.
	Hotspot image.Point

	// System is true when the server named one of its built in cursors rather
	// than sending pixels. Image is then nil, and the caller is expected to
	// substitute its own artwork for SystemType. A system cursor is reported
	// rather than skipped because a caller that never hears about it keeps
	// drawing the last shape it was given, which is how a pointer ends up
	// looking like an hourglass forever.
	System bool

	// SystemType is the raw systemPointerType. SYSPTR_NULL means the pointer
	// should be hidden. Other values are passed through unnamed: the
	// specification's table lists only the two below, and inventing names for
	// the rest would be a guess about a value a caller is better placed to
	// interpret.
	SystemType uint32
}

// Hidden reports whether the server asked for no pointer at all.
func (c *Cursor) Hidden() bool {
	return c != nil && c.System && c.SystemType == pdu.SYSPTR_NULL
}

// cursorFromShape applies a shape's masks and returns the image to draw.
//
// The byte order and the row order differ between the two kinds of pointer, and
// both were taken from FreeRDP rather than reasoned out.
//
// A colour pointer's XOR mask is bottom up, so its rows are walked from the
// bottom, and the AND mask is one bit per pixel: zero means the colour is drawn
// as it is, one means the pixel is not drawn at all. A 32bpp shape carries its
// own alpha and that is used as it stands, which is how Windows sends a pointer
// with soft edges; a 24bpp one has no alpha, so a kept pixel is fully opaque.
//
// A two colour pointer (one bit per pixel, XorBpp of one) is the other way up:
// FreeRDP's decoder only flips the colour case, so the rows here are read top
// down. Its four cases are all different, and it has no colour of its own: the AND
// and XOR bits together mean black, white, transparent, or a pixel whose colour
// is the inverse of what is behind it. That last case cannot be resolved here,
// because this image is drawn over a desktop this package does not hold, so it
// becomes a black and white checkerboard, which is what FreeRDP substitutes for
// it, with a comment of its own saying that real inversion only appears to be
// supported on Windows and that a flat colour can leave a pointer invisible.
func cursorFromShape(s *pdu.PointerShape) *Cursor {
	if s == nil || s.Width <= 0 || s.Height <= 0 {
		return nil
	}
	w, h := s.Width, s.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	mono := s.XorBpp == 1
	xorRow := pdu.PointerXorRowBytes(w, s.XorBpp)
	andRow := pdu.AndMaskRowBytes(w)
	if xorRow <= 0 || andRow <= 0 {
		return nil
	}

	for y := 0; y < h; y++ {
		// A colour pointer's mask is bottom up. A two colour one is not.
		srcRow := y
		if !mono {
			srcRow = h - 1 - y
		}
		xo := srcRow * xorRow
		ao := srcRow * andRow
		for x := 0; x < w; x++ {
			// Both masks are stored most significant bit first within each
			// byte, which is the opposite of how a Go bitmap would be.
			andBit := bitAt(s.And, ao, x)
			var px [4]byte
			if mono {
				px = monoCursorPixel(andBit, bitAt(s.Xor, xo, x), x, y)
			} else {
				i := xo + x*s.XorBpp
				if i+s.XorBpp > len(s.Xor) {
					break
				}
				switch s.XorBpp {
				case 3:
					px = [4]byte{s.Xor[i+2], s.Xor[i+1], s.Xor[i], 0xff}
				case 4:
					px = [4]byte{s.Xor[i+2], s.Xor[i+1], s.Xor[i], s.Xor[i+3]}
				default:
					return nil
				}
				if andBit == 1 {
					px = [4]byte{}
				}
			}
			o := y*img.Stride + x*4
			copy(img.Pix[o:o+4], px[:])
		}
	}

	hot := image.Point{X: s.HotspotX, Y: s.HotspotY}
	// A hotspot outside the shape is a malformed update. Reporting it as it is
	// would have a caller position a cursor by a point that is not on it, so it
	// is clamped into the image, which at worst misplaces a pixel.
	if hot.X < 0 || hot.X >= w {
		hot.X = 0
	}
	if hot.Y < 0 || hot.Y >= h {
		hot.Y = 0
	}
	return &Cursor{Image: img, Width: w, Height: h, Hotspot: hot}
}

// bitAt reads one bit from a mask, in the order the masks are stored: the most
// significant bit of each byte first, and row by row from the given offset.
func bitAt(mask []byte, rowOffset, x int) byte {
	i := rowOffset + x/8
	if i < 0 || i >= len(mask) {
		return 0
	}
	return (mask[i] >> uint(7-x%8)) & 1
}

// monoCursorPixel applies the two colour pointer's rule. All four combinations
// mean something different, which is why this cannot go through the colour path:
// there, an AND bit of one is simply transparent.
func monoCursorPixel(andBit, xorBit byte, x, y int) [4]byte {
	switch {
	case andBit == 0 && xorBit == 0:
		return [4]byte{0, 0, 0, 0xff} // black
	case andBit == 0 && xorBit == 1:
		return [4]byte{0xff, 0xff, 0xff, 0xff} // white
	case andBit == 1 && xorBit == 0:
		return [4]byte{} // transparent
	default:
		// The inverse of whatever is behind it, which is not knowable here.
		// A checkerboard is what FreeRDP substitutes, and it stays visible
		// on any background, which a flat colour does not.
		if (x+y)&1 == 0 {
			return [4]byte{0xff, 0xff, 0xff, 0xff}
		}
		return [4]byte{0, 0, 0, 0xff}
	}
}

// OnCursor registers f to be called when the pointer's shape changes, or when
// the server names a system pointer instead.
//
// It does not fire for a position change: a pointer that moves keeps its shape,
// and a gateway re-encoding the shape on every mouse move would be doing work
// for nothing. Use OnCursorPos for that. The cursor is only valid for the
// duration of the call.
func (c *Client) OnCursor(f func(cur *Cursor)) {
	c.ctl.On("cursor", func(data interface{}) {
		f(data.(*Cursor))
	})
}

// OnCursorPos registers f to be called with the server's pointer position, in
// desktop pixels from the top left. It is the counterpart of OnCursor: the shape
// says what to draw and this says where.
//
// This is a different event from OnPointerPosition, which reports the raw
// fast-path update; both end up here, so a caller that only wants to place the
// pointer does not have to follow two of them.
func (c *Client) OnCursorPos(f func(x, y int)) {
	c.ctl.On("cursor-pos", func(data interface{}) {
		p := data.(image.Point)
		f(p.X, p.Y)
	})
}
