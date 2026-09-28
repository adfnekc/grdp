package orders

import (
	"fmt"
	"image"
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

	// unsupported counts orders that were recognised but not drawn, so a caller
	// can tell "rendered" from "rendered what I could". Use Unsupported for the
	// breakdown.
	unsupported map[string]int
}

// NewScreen returns a black screen of the given size.
func NewScreen(width, height int) *Screen {
	s := &Screen{Cache: NewCache(), unsupported: make(map[string]int)}
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
		// Filling the cache changes nothing on screen.
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
func (s *Screen) blitBGRA(src []byte, srcStride int, srcX, srcY, cx, cy, dstX, dstY int,
	clip image.Rectangle) image.Rectangle {

	r := image.Rect(dstX, dstY, dstX+cx, dstY+cy).Intersect(clip)
	if r.Empty() {
		return image.Rectangle{}
	}
	// Work out where the visible part starts within the source.
	sx := srcX + r.Min.X - dstX
	sy := srcY + r.Min.Y - dstY

	for row := r.Min.Y; row < r.Max.Y; row++ {
		so := (sy*srcStride + sx) * 4
		do := (row*s.width + r.Min.X) * 4
		for col := r.Min.X; col < r.Max.X; col++ {
			if so+4 > len(src) || so < 0 {
				return r
			}
			copy(s.pixels[do:do+4], src[so:so+4])
			so += 4
			do += 4
		}
		sy++
	}
	return r
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
