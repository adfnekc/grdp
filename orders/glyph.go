package orders

import (
	"image"
	"sync"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// glyphKey identifies a slot in one of the ten glyph caches.
type glyphKey struct {
	cacheID uint32
	index   uint32
}

// GlyphCache holds the monochrome glyphs a server has cached and the glyph
// fragments it has stored while drawing them.
//
// A fragment is a run of glyph indices and their advances, stored by an ADD and
// replayed by a USE so a repeated string does not put every glyph in the cache
// again. Fragments and glyphs are indexed separately, which is why there are
// two lookups.
type GlyphCache struct {
	mu        sync.Mutex
	glyphs    map[glyphKey]*pdu.Glyph
	fragments map[uint32][]byte
}

// NewGlyphCache returns an empty glyph cache.
func NewGlyphCache() *GlyphCache {
	return &GlyphCache{
		glyphs:    make(map[glyphKey]*pdu.Glyph),
		fragments: make(map[uint32][]byte),
	}
}

// Put stores the glyphs of a Cache Glyph secondary order. A slot the server
// fills again is replaced, which is all the protocol offers: there is no
// eviction message for the client.
func (c *GlyphCache) Put(order *pdu.GlyphCacheOrder) {
	if c == nil || order == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range order.Glyphs {
		g := order.Glyphs[i]
		c.glyphs[glyphKey{order.CacheID, g.CacheIndex}] = &g
	}
}

// Glyph returns a cached glyph, or nil if the slot is empty. The caller must
// not modify it.
func (c *GlyphCache) Glyph(cacheID, index uint32) *pdu.Glyph {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.glyphs[glyphKey{cacheID, index}]
}

// Fragment returns a stored glyph fragment, or nil. The caller must not modify
// it.
func (c *GlyphCache) Fragment(index uint32) []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fragments[index]
}

// putFragment stores a fragment under its cache index. The bytes are copied
// because a fragment outlives the order that carried it.
func (c *GlyphCache) putFragment(index uint32, fragment []byte) {
	if c == nil {
		return
	}
	stored := make([]byte, len(fragment))
	copy(stored, fragment)
	c.mu.Lock()
	c.fragments[index] = stored
	c.mu.Unlock()
}

// Reset empties the cache. A new connection, or a reset graphics, invalidates
// everything in it.
func (c *GlyphCache) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.glyphs = make(map[glyphKey]*pdu.Glyph)
	c.fragments = make(map[uint32][]byte)
	c.mu.Unlock()
}

// Len is the number of glyphs held, for tests and diagnostics.
func (c *GlyphCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.glyphs)
}

// DrawText2 draws a TEXT2 (GlyphIndex) order out of a glyph cache.
//
// The pen starts at the order's X and Y, each glyph is placed where the pen is
// plus the glyph's own origin, and the pen then advances by whatever the order
// says. The order's data is a packed run, per FreeRDP's
// update_process_glyph_fragments: a glyph cache index, a GLYPH_FRAGMENT_USE
// with a fragment cache index, or a GLYPH_FRAGMENT_ADD with an index and a
// size. When the font is not fixed pitch an advance byte follows a glyph index
// or a USE, and the bytes a USE names are replayed as the same kind of run.
//
// It must be called with the screen lock held, which is how Screen.Draw calls
// it from the dispatch.
func (s *Screen) DrawText2(d *pdu.GlyphIndex, glyphs *GlyphCache, clip image.Rectangle) image.Rectangle {
	if d == nil || glyphs == nil {
		return image.Rectangle{}
	}
	// The dispatched clip is already inside the screen, but DrawText2 is
	// exported, so the pixel writes below are bounded here rather than assumed.
	clip = clip.Intersect(image.Rect(0, 0, s.width, s.height))

	fg := inkColour(d.ForeColor)
	bg := inkColour(d.BackColor)

	// A fixed pitch font puts its advance in the order rather than after every
	// glyph: either ulCharInc is the advance, or SO_CHAR_INC_EQUAL_BM_BASE says
	// the glyph's own width is.
	fixed := d.UlCharInc != 0 || d.FlAccel&pdu.SO_CHAR_INC_EQUAL_BM_BASE != 0
	transparent := d.FOpRedundant != 0

	var dirty image.Rectangle

	// Opaque text paints the rectangle it carries before the glyphs. The
	// coordinates are inclusive, which is why the size is right-left+1.
	if !transparent && d.OpRight > d.OpLeft && d.OpBottom > d.OpTop {
		dirty = union(dirty, s.fill(int(d.OpLeft), int(d.OpTop),
			int(d.OpRight-d.OpLeft+1), int(d.OpBottom-d.OpTop+1), d.BackColor, clip))
	}

	penX, penY := int(d.X), int(d.Y)

	advance := func(delta int) {
		if d.FlAccel&pdu.SO_VERTICAL != 0 {
			penY += delta
		}
		if d.FlAccel&pdu.SO_HORIZONTAL != 0 {
			penX += delta
		}
	}

	draw := func(index uint32) {
		g := glyphs.Glyph(uint32(d.CacheID), index)
		if g == nil {
			// Drawing a glyph that was never cached leaves the screen as it is,
			// which is also what a client that missed the cache order shows.
			s.note("text2 glyph not in the cache")
			return
		}
		dirty = union(dirty, s.drawGlyph(g, penX, penY, fg, bg, transparent, clip))
		if fixed {
			// FreeRDP advances the pen only when SO_CHAR_INC_EQUAL_BM_BASE is
			// set, which leaves a fixed pitch order's glyphs stacked on top of
			// each other. The specification says ulCharInc is the advance
			// width, so it is used as one here.
			if d.FlAccel&pdu.SO_CHAR_INC_EQUAL_BM_BASE != 0 {
				penX += g.Width
			} else {
				penX += int(d.UlCharInc)
			}
		}
	}

	// replay draws a stored fragment, which is a run of glyph indices and
	// advances with no control bytes, so every byte in it is an index. FreeRDP
	// walks a fragment the same way rather than looking for a USE or an ADD in
	// it.
	replay := func(fragment []byte) bool {
		for i := 0; i < len(fragment); {
			index := uint32(fragment[i])
			i++
			if !fixed {
				delta, next, ok := readGlyphAdvance(fragment, i)
				if !ok {
					s.note("text2 truncated fragment advance")
					return false
				}
				i = next
				advance(delta)
			}
			draw(index)
		}
		return true
	}

	data := d.Data
	for i := 0; i < len(data); {
		op := data[i]
		i++
		switch op {
		case pdu.GLYPH_FRAGMENT_USE:
			if i >= len(data) {
				s.note("text2 truncated fragment use")
				return dirty
			}
			id := data[i]
			i++
			if !fixed {
				delta, next, ok := readGlyphAdvance(data, i)
				if !ok {
					s.note("text2 truncated fragment advance")
					return dirty
				}
				i = next
				advance(delta)
			}
			fragment := glyphs.Fragment(uint32(id))
			if fragment == nil {
				s.note("text2 fragment not in the cache")
				continue
			}
			if !replay(fragment) {
				return dirty
			}
		case pdu.GLYPH_FRAGMENT_ADD:
			if i+2 > len(data) {
				s.note("text2 truncated fragment add")
				return dirty
			}
			id := data[i]
			size := int(data[i+1])
			i += 2
			// The fragment is the run of glyph bytes that precedes the ADD,
			// and the size counts neither the ADD, its index nor the size byte.
			start := i - 3 - size
			if start < 0 {
				s.note("text2 fragment longer than the run before it")
				continue
			}
			glyphs.putFragment(uint32(id), data[start:i-3])
		default:
			if !fixed {
				delta, next, ok := readGlyphAdvance(data, i)
				if !ok {
					s.note("text2 truncated glyph advance")
					return dirty
				}
				i = next
				advance(delta)
			}
			draw(uint32(op))
		}
	}
	return dirty
}

// readGlyphAdvance reads the distance a glyph or a fragment moves the pen. A
// byte with its top bit clear is the distance; a set top bit means the next two
// bytes are the distance, little endian.
func readGlyphAdvance(data []byte, i int) (delta, next int, ok bool) {
	if i >= len(data) {
		return 0, i, false
	}
	b := data[i]
	i++
	if b&0x80 != 0 {
		if i+1 >= len(data) {
			return 0, i, false
		}
		return int(data[i]) | int(data[i+1])<<8, i + 2, true
	}
	return int(b), i, true
}

// drawGlyph paints one cached glyph. A bit set is the foreground colour and a
// bit clear the background, which is left alone when the text background is
// transparent.
func (s *Screen) drawGlyph(g *pdu.Glyph, penX, penY int, fg, bg [4]uint8,
	transparent bool, clip image.Rectangle) image.Rectangle {

	if g == nil || g.Width <= 0 || g.Height <= 0 {
		return image.Rectangle{}
	}
	x0, y0 := penX+int(g.X), penY+int(g.Y)
	r := image.Rect(x0, y0, x0+g.Width, y0+g.Height).Intersect(clip)
	if r.Empty() {
		return image.Rectangle{}
	}

	stride := (g.Width + 7) / 8
	for row := r.Min.Y; row < r.Max.Y; row++ {
		src := (row - y0) * stride
		if src < 0 || src+stride > len(g.Bits) {
			continue
		}
		dst := (row*s.width + r.Min.X) * 4
		for col := r.Min.X; col < r.Max.X; col++ {
			x := col - x0
			set := g.Bits[src+x/8]&(0x80>>uint(x%8)) != 0
			if !set && transparent {
				dst += 4
				continue
			}
			c := bg
			if set {
				c = fg
			}
			copy(s.pixels[dst:dst+4], c[:])
			dst += 4
		}
	}
	return r
}
