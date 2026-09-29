package orders

import (
	"fmt"
	"image"
	"sort"
	"sync"

	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// ROP3 codes that turn up in the orders this renders. SRCCOPY is by far the
// most common; the others are the two solid fills.
const (
	ropSrcCopy uint8 = 0xCC
	ropBlack   uint8 = 0x00
	ropWhite   uint8 = 0xFF
	ropNoop    uint8 = 0xAA
	ropPatCopy uint8 = 0xF0
)

// Screen is a framebuffer that renders drawing orders. It holds BGRA pixels,
// top down, like the surfaces EGFX uses.
type Screen struct {
	mu     sync.Mutex
	width  int
	height int
	pixels []byte

	// Cache holds the bitmaps the server blits from. Without it MEMBLT has
	// nothing to read.
	Cache *Cache

	// Glyphs holds the glyph cache the TEXT2 order draws from. The server fills
	// it with cache glyph orders before it sends any text.
	Glyphs *GlyphCache

	// unsupported counts orders that were recognised but not drawn, so a caller
	// can tell "rendered" from "rendered what I could". Use Unsupported for the
	// breakdown.
	unsupported map[string]int
}

// NewScreen returns a black screen of the given size.
func NewScreen(width, height int) *Screen {
	s := &Screen{Cache: NewCache(), Glyphs: NewGlyphCache(), unsupported: make(map[string]int)}
	s.Resize(width, height)
	return s
}

// Resize discards the contents and changes the size. Servers send a fresh set
// of orders after a resize, so there is nothing worth keeping.
func (s *Screen) Resize(width, height int) {
	if width < 1 || height < 1 {
		return
	}
	s.mu.Lock()
	s.width, s.height = width, height
	s.pixels = make([]byte, width*height*4)
	// Opaque black rather than transparent, so that a screen which never gets
	// drawn reads as black and not as a hole.
	for i := 3; i < len(s.pixels); i += 4 {
		s.pixels[i] = 0xff
	}
	s.mu.Unlock()
}

// Size returns the screen size.
func (s *Screen) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.width, s.height
}

// Pixels returns a copy of the screen as BGRA, top down.
func (s *Screen) Pixels() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.pixels))
	copy(out, s.pixels)
	return out
}

// Unsupported reports how many orders were recognised but not drawn, by kind.
// A stream of MEMBLTs with an unhandled ROP shows up here rather than as a
// silently wrong screen.
func (s *Screen) Unsupported() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.unsupported))
	for k, v := range s.unsupported {
		out[k] = v
	}
	return out
}

// Draw renders a batch of orders and returns the area they changed, which is
// empty when nothing was drawn. The returned error is for a batch that could
// not be applied at all; orders that are merely unsupported are counted instead,
// since the rest of the batch is still worth drawing.
func (s *Screen) Draw(pdus []pdu.OrderPdu) (image.Rectangle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var dirty image.Rectangle
	for i := range pdus {
		r := s.drawOne(&pdus[i])
		if r.Empty() {
			continue
		}
		dirty = union(dirty, r)
	}
	return dirty.Intersect(image.Rect(0, 0, s.width, s.height)), nil
}

func (s *Screen) drawOne(o *pdu.OrderPdu) image.Rectangle {
	if o.Secondary != nil {
		if cb := o.Secondary.CacheBitmap; cb != nil {
			s.Cache.Put(cb)
		}
		if cg := o.Secondary.CacheGlyphs; cg != nil {
			s.Glyphs.Put(cg)
		}
		// Filling a cache changes nothing on screen.
		return image.Rectangle{}
	}
	if o.Primary == nil || o.Primary.Data == nil {
		if o.Type == pdu.ORDER_ALTSEC {
			// Frame markers and the like carry no drawing.
			return image.Rectangle{}
		}
		s.note("unparsed")
		return image.Rectangle{}
	}

	clip := s.clip(o)

	switch d := o.Primary.Data.(type) {
	case *pdu.Memblt:
		return s.memblt(d, clip)
	case *pdu.Scrblt:
		return s.scrblt(d, clip)
	case *pdu.Dstblt:
		return s.dstblt(d, clip)
	case *pdu.OpaqueRect:
		return s.opaqueRect(d, clip)
	case *pdu.Patblt:
		return s.patblt(d, clip)
	case *pdu.PolygonSc:
		return s.polygon(d.Points, d.Fgcolour, clip)
	case *pdu.PolygonCb:
		return s.polygon(d.Points, d.FgColour, clip)
	case *pdu.Polyline:
		return s.polyline(d.Points, d.Colour, clip)
	case *pdu.EllipeSc:
		return s.ellipse(d.Left, d.Top, d.Right, d.Bottom, d.Colour, false, clip)
	case *pdu.EllipeCb:
		return s.ellipse(d.Left, d.Top, d.Right, d.Bottom, d.FgColour, true, clip)
	case *pdu.GlyphIndex:
		return s.DrawText2(d, s.Glyphs, clip)
	case *pdu.Mem3blt, *pdu.MultiDstBlt, *pdu.MultiPatBlt, *pdu.MultiScrBlt, *pdu.MultiOpaqueRect:
		return s.DrawMulti(o, clip)
	case *pdu.LineTo:
		return s.lineTo(d, clip)
	case *pdu.SaveBitmap:
		return s.saveBitmap(d, clip)
	case *pdu.FastIndex:
		return s.drawFastIndex(d, clip)
	default:
		s.note(fmt.Sprintf("%T", o.Primary.Data))
		return image.Rectangle{}
	}
}

// clip returns the area an order may draw in: the bounds it carries, or the
// whole screen when it carries none.
func (s *Screen) clip(o *pdu.OrderPdu) image.Rectangle {
	full := image.Rect(0, 0, s.width, s.height)
	if !o.HasBounds() || o.Primary == nil {
		return full
	}
	left, top, right, bottom := o.Primary.Bounds.Rect()
	// The server can send bounds that are zero sized or inside out; treating
	// those as the full screen would paint outside where it asked.
	if right <= left || bottom <= top {
		return image.Rectangle{}
	}
	return image.Rect(left, top, right, bottom).Intersect(full)
}

// memblt copies a bitmap out of the cache onto the screen. This is the order
// that draws almost everything in a session that uses orders at all.
func (s *Screen) memblt(d *pdu.Memblt, clip image.Rectangle) image.Rectangle {
	if d.CacheId == 0xFF {
		// 0xFF means the bitmap follows in the order rather than coming from
		// the cache. Servers send this rarely and it is not implemented.
		s.note("memblt from the stream")
		return image.Rectangle{}
	}
	// MEMBLT carries no raster operation: it copies. The byte the order holds
	// is a colour index, used only for palettised bitmaps, and reading it as a
	// ROP made every one of these look like an unhandled BLACKNESS fill.
	entry := s.Cache.Get(uint32(d.CacheId), uint32(d.CacheIdx))
	if entry == nil {
		// Blitting a slot that was never filled leaves the screen as it is,
		// which is also what a client that missed the cache order would show.
		s.note("memblt of an empty cache slot")
		return image.Rectangle{}
	}

	// The source rectangle is clipped to the entry first, because a server may
	// ask for a piece that runs off the edge of the cached bitmap.
	srcX, srcY := int(d.Srcx), int(d.Srcy)
	cx, cy := int(d.Cx), int(d.Cy)
	if srcX < 0 {
		cx += srcX
		srcX = 0
	}
	if srcY < 0 {
		cy += srcY
		srcY = 0
	}
	if srcX+cx > entry.Width {
		cx = entry.Width - srcX
	}
	if srcY+cy > entry.Height {
		cy = entry.Height - srcY
	}
	if cx <= 0 || cy <= 0 {
		return image.Rectangle{}
	}

	return s.blitBGRA(entry.Pixels, entry.Width, srcX, srcY, cx, cy,
		int(d.X), int(d.Y), clip)
}

// scrblt copies a rectangle within the screen itself.
func (s *Screen) scrblt(d *pdu.Scrblt, clip image.Rectangle) image.Rectangle {
	if d.Opcode != ropSrcCopy {
		s.note(fmt.Sprintf("scrblt rop 0x%02x", d.Opcode))
		return image.Rectangle{}
	}
	return s.blitBGRA(s.pixels, s.width,
		int(d.Srcx), int(d.Srcy), int(d.Cx), int(d.Cy),
		int(d.X), int(d.Y), clip)
}

// dstblt fills a rectangle from the pattern, which for the codes that turn up
// means a solid colour.
func (s *Screen) dstblt(d *pdu.Dstblt, clip image.Rectangle) image.Rectangle {
	var c [4]uint8
	switch d.Opcode {
	case ropBlack:
		c = [4]uint8{0, 0, 0, 0xff}
	case ropWhite:
		c = [4]uint8{0xff, 0xff, 0xff, 0xff}
	case ropNoop:
		return image.Rectangle{}
	default:
		s.note(fmt.Sprintf("dstblt rop 0x%02x", d.Opcode))
		return image.Rectangle{}
	}
	return s.fill(int(d.X), int(d.Y), int(d.Cx), int(d.Cy), c, clip)
}

// patblt fills a rectangle from a brush, which is how a server draws a solid
// fill or a hatch.
//
// Almost every one of these is a solid brush with PATCOPY, which is a fill in a
// colour. A patterned brush is drawn as an eight by eight monochrome tile, with
// the background colour where the pattern has a zero and the foreground where it
// has a one.
func (s *Screen) patblt(d *pdu.Patblt, clip image.Rectangle) image.Rectangle {
	fg := inkColour(d.FgColour)
	bg := inkColour(d.BgColour)

	switch d.Opcode {
	case ropBlack:
		return s.fill(int(d.X), int(d.Y), int(d.Cx), int(d.Cy), [4]uint8{0, 0, 0, 0}, clip)
	case ropWhite:
		return s.fill(int(d.X), int(d.Y), int(d.Cx), int(d.Cy), [4]uint8{0xff, 0xff, 0xff, 0}, clip)
	case ropPatCopy:
		// Handled below.
	default:
		s.note(fmt.Sprintf("patblt rop 0x%02x", d.Opcode))
		return image.Rectangle{}
	}

	r := image.Rect(int(d.X), int(d.Y), int(d.X+d.Cx), int(d.Y+d.Cy)).Intersect(clip)
	if r.Empty() {
		return image.Rectangle{}
	}

	// The brush travels with the order rather than being named in a cache. Eight
	// bytes of pattern is an eight by eight monochrome tile, which is the only
	// shape that can be interpreted without more than the order carries; a solid
	// brush is a fill, and anything else falls back to the foreground colour
	// rather than painting something guessed.
	pattern := d.Brush.Data
	if len(pattern) != 8 {
		return s.fill(int(d.X), int(d.Y), int(d.Cx), int(d.Cy), d.FgColour, clip)
	}

	for row := r.Min.Y; row < r.Max.Y; row++ {
		bits := pattern[row%8]
		base := (row*s.width + r.Min.X) * 4
		for col := r.Min.X; col < r.Max.X; col++ {
			// The pattern's most significant bit is its leftmost pixel.
			c := fg
			if bits&(0x80>>uint(col%8)) == 0 {
				c = bg
			}
			copy(s.pixels[base:base+4], c[:])
			base += 4
		}
	}
	return r
}

// inkColour turns an order's colour into a BGRA pixel. TS_COLOR is red, green,
// blue in that order, so it is reversed on the way into a BGRA buffer.
func inkColour(c [4]uint8) [4]uint8 {
	return [4]uint8{c[2], c[1], c[0], 0xff}
}

// opaqueRect fills a rectangle with the colour the order carries.
func (s *Screen) opaqueRect(d *pdu.OpaqueRect, clip image.Rectangle) image.Rectangle {
	return s.fill(int(d.X), int(d.Y), int(d.Cx), int(d.Cy), d.Colour, clip)
}

// fill paints a rectangle, clipped both to the order's bounds and to the screen.
//
// The colour an order carries is RDP's TS_COLOR, which is red, green, blue in
// that order, so it is reversed on the way into a BGRA buffer.
func (s *Screen) fill(x, y, cx, cy int, colour [4]uint8, clip image.Rectangle) image.Rectangle {
	r := image.Rect(x, y, x+cx, y+cy).Intersect(clip)
	if r.Empty() {
		return image.Rectangle{}
	}
	bgra := [4]uint8{colour[2], colour[1], colour[0], 0xff}
	for row := r.Min.Y; row < r.Max.Y; row++ {
		base := (row*s.width + r.Min.X) * 4
		for col := r.Min.X; col < r.Max.X; col++ {
			copy(s.pixels[base:base+4], bgra[:])
			base += 4
		}
	}
	return r
}

// blitBGRA copies a rectangle out of a BGRA buffer onto the screen, clipped to
// both the order's bounds and the screen edge.
//
// The source is checked pixel by pixel, because the caller treats the returned
// rectangle as the area that changed and a source rectangle can run past its
// buffer: MEMBLT clamps its source to the cached bitmap, but SCRBLT copies from
// the screen at an origin the server picks. When the source runs out, only the
// pixels copied so far have changed, so that is what comes back; the whole
// destination rectangle would mark never written pixels dirty.
func (s *Screen) blitBGRA(src []byte, srcStride int, srcX, srcY, cx, cy, dstX, dstY int,
	clip image.Rectangle) image.Rectangle {

	r := image.Rect(dstX, dstY, dstX+cx, dstY+cy).Intersect(clip)
	if r.Empty() {
		return image.Rectangle{}
	}
	// Work out where the visible part starts within the source.
	sx := srcX + r.Min.X - dstX
	sy := srcY + r.Min.Y - dstY

	// The copied pixels, as a bounding box. A row that stops part way through
	// leaves a notch a rectangle cannot describe, so it is bounded rather than
	// reported exactly.
	var written image.Rectangle
	for row := r.Min.Y; row < r.Max.Y; row++ {
		so := (sy*srcStride + sx) * 4
		do := (row*s.width + r.Min.X) * 4
		col := r.Min.X
		for ; col < r.Max.X; col++ {
			if so < 0 || so+4 > len(src) {
				if col > r.Min.X {
					written = union(written, image.Rect(r.Min.X, row, col, row+1))
				}
				return written
			}
			copy(s.pixels[do:do+4], src[so:so+4])
			so += 4
			do += 4
		}
		written = union(written, image.Rect(r.Min.X, row, r.Max.X, row+1))
		sy++
	}
	return written
}

// polygon fills the shape the points describe, by scanline. The last point of
// the list is expected to be the first one again, so it is not treated as an
// extra edge.
func (s *Screen) polygon(points []pdu.Point, colour [4]uint8, clip image.Rectangle) image.Rectangle {
	if len(points) < 3 {
		return image.Rectangle{}
	}
	ink := inkColour(colour)

	// The rows are bounded by the clip before the sweep, not by the coordinates:
	// a polygon only carries its vertices, and a pair of them can be millions of
	// rows apart, so sweeping the whole span costs that many iterations for a
	// shape that is mostly off screen.
	y0, y1 := int(points[0].Y), int(points[0].Y)
	for _, p := range points[1:] {
		if int(p.Y) < y0 {
			y0 = int(p.Y)
		}
		if int(p.Y) > y1 {
			y1 = int(p.Y)
		}
	}
	if y0 < clip.Min.Y {
		y0 = clip.Min.Y
	}
	if y1 > clip.Max.Y-1 {
		y1 = clip.Max.Y - 1
	}

	var dirty image.Rectangle
	for y := y0; y <= y1; y++ {
		// The crossings of this scanline with each edge, at y plus a half so
		// that a vertex is counted once.
		var xs []int
		for i := 0; i < len(points); i++ {
			a := points[i]
			b := points[(i+1)%len(points)]
			if a.Y == b.Y {
				continue
			}
			lo, hi := int(a.Y), int(b.Y)
			if lo > hi {
				lo, hi = hi, lo
			}
			if y < lo || y >= hi {
				continue
			}
			// Interpolate for the centre of the pixel row.
			num := (y-int(a.Y))*(int(b.X)-int(a.X)) + (int(b.Y)-int(a.Y))/2
			xs = append(xs, int(a.X)+num/(int(b.Y)-int(a.Y)))
		}
		if len(xs) < 2 {
			continue
		}
		sort.Ints(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			r := s.fill(xs[i], y, xs[i+1]-xs[i]+1, 1, colour, clip)
			dirty = union(dirty, r)
		}
	}
	_ = ink
	return dirty
}

// polyline draws the segments the points describe.
func (s *Screen) polyline(points []pdu.Point, colour [4]uint8, clip image.Rectangle) image.Rectangle {
	if len(points) < 2 {
		return image.Rectangle{}
	}
	var dirty image.Rectangle
	for i := 0; i+1 < len(points); i++ {
		dirty = union(dirty, s.line(points[i], points[i+1], colour, clip))
	}
	return dirty
}

// line draws one segment, one pixel thick, which is what a thin pen is.
//
// The segment is clipped to the order's bounds first. The step count follows the
// coordinates, and delta coordinates accumulate across orders, so an unclipped
// segment can ask for a number of steps no screen has pixels for.
func (s *Screen) line(a, b pdu.Point, colour [4]uint8, clip image.Rectangle) image.Rectangle {
	var ok bool
	a, b, ok = clipSegment(a, b, clip)
	if !ok {
		return image.Rectangle{}
	}

	dx, dy := int(b.X-a.X), int(b.Y-a.Y)
	steps := abs(dx)
	if abs(dy) > steps {
		steps = abs(dy)
	}
	if steps == 0 {
		return s.fill(int(a.X), int(a.Y), 1, 1, colour, clip)
	}

	var dirty image.Rectangle
	for i := 0; i <= steps; i++ {
		x := int(a.X) + dx*i/steps
		y := int(a.Y) + dy*i/steps
		dirty = union(dirty, s.fill(x, y, 1, 1, colour, clip))
	}
	return dirty
}

// ellipse draws the ellipse the rectangle inscribes: its boundary, one pixel
// thick, or the whole shape when asked to fill it.
func (s *Screen) ellipse(left, top, right, bottom int32, colour [4]uint8, filled bool, clip image.Rectangle) image.Rectangle {
	// The rectangle comes from coordinates that delta encoding can grow without
	// limit, so it is bounded before radii are derived from it: a row loop of two
	// billion iterations is otherwise one order away.
	const maxEllipseSpan = 1 << 14
	r := image.Rect(int(left), int(top), int(right), int(bottom))
	if r.Empty() || r.Dx() > maxEllipseSpan || r.Dy() > maxEllipseSpan {
		return image.Rectangle{}
	}
	// Bound the rows by the clip before sweeping them, as the polygon does: the
	// order's own rectangle can be far larger than the area it may draw in, and
	// every row costs an inner scan proportional to its width.
	rowMin, rowMax := r.Min.Y, r.Max.Y
	if rowMin < clip.Min.Y {
		rowMin = clip.Min.Y
	}
	if rowMax > clip.Max.Y {
		rowMax = clip.Max.Y
	}
	if rowMin >= rowMax {
		return image.Rectangle{}
	}
	// Radii, so that the centre lands on a pixel whatever the parity of the size.
	w, h := (r.Dx()-1)/2, (r.Dy()-1)/2
	if w < 1 || h < 1 {
		return s.fill(r.Min.X, r.Min.Y, r.Dx(), r.Dy(), colour, clip)
	}
	cx, cy := r.Min.X+w, r.Min.Y+h
	w2, h2 := w*w, h*h

	var dirty image.Rectangle
	prevL, prevR := 0, 0
	for y := -h; y <= h; y++ {
		if cy+y < rowMin || cy+y >= rowMax {
			continue
		}
		// Half the width at this row, from the equation of the ellipse.
		inner := w2 * (1 - (y*y)/h2)
		span := 0
		for (span+1)*(span+1) <= inner {
			span++
		}
		l, rr := cx-span, cx+span

		if filled {
			dirty = union(dirty, s.fill(l, cy+y, 2*span+1, 1, colour, clip))
		} else {
			// The two ends of the row, plus whatever the previous row's ends
			// leave uncovered, so that the outline stays joined where it runs
			// sideways near the top and bottom.
			dirty = union(dirty, s.fill(l, cy+y, 1, 1, colour, clip))
			dirty = union(dirty, s.fill(rr, cy+y, 1, 1, colour, clip))
			if y > -h {
				if l < prevL {
					dirty = union(dirty, s.fill(l, cy+y, prevL-l, 1, colour, clip))
				}
				if rr > prevR {
					dirty = union(dirty, s.fill(prevR+1, cy+y, rr-prevR, 1, colour, clip))
				}
			}
		}
		prevL, prevR = l, rr
	}
	return dirty
}

// clipSegment clips a segment to a rectangle, returning false when none of it is
// inside. Liang-Barsky, which is enough for one pixel wide lines.
func clipSegment(a, b pdu.Point, clip image.Rectangle) (pdu.Point, pdu.Point, bool) {
	x0, y0 := float64(a.X), float64(a.Y)
	x1, y1 := float64(b.X), float64(b.Y)
	dx, dy := x1-x0, y1-y0

	left, top := float64(clip.Min.X), float64(clip.Min.Y)
	right, bottom := float64(clip.Max.X-1), float64(clip.Max.Y-1)

	t0, t1 := 0.0, 1.0
	for _, e := range [4][2]float64{{-dx, x0 - left}, {dx, right - x0}, {-dy, y0 - top}, {dy, bottom - y0}} {
		p, q := e[0], e[1]
		if p == 0 {
			if q < 0 {
				return a, b, false
			}
			continue
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return a, b, false
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return a, b, false
			}
			if r < t1 {
				t1 = r
			}
		}
	}
	return pdu.Point{X: int32(x0 + t0*dx), Y: int32(y0 + t0*dy)},
		pdu.Point{X: int32(x0 + t1*dx), Y: int32(y0 + t1*dy)}, true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (s *Screen) note(kind string) {
	s.unsupported[kind]++
	glog.Debugf("orders: not drawn: %s", kind)
}

func union(a, b image.Rectangle) image.Rectangle {
	if a.Empty() {
		return b
	}
	if b.Empty() {
		return a
	}
	return a.Union(b)
}
