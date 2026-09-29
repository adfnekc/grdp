// Package orders renders the drawing orders a server sends when the client says
// it supports them.
//
// Orders are how RDP drew things before bitmap updates and EGFX. The server puts
// bitmaps into a cache with secondary orders and then blits them to the screen
// with MEMBLT, often with a couple of fills alongside. That is the whole model
// in practice: a session measured against Windows 10 consisted of 1770 MEMBLTs,
// 1000 cache fills and 62 alternate secondary orders, and nothing else.
//
// Whether to advertise support is a trade: the server stops sending bitmap
// updates entirely, so a client that cannot render orders gets a blank screen
// rather than a slower one. See docs/windows-verification.md.
package orders

import (
	"sync"

	"github.com/adfnekc/grdp/codec"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// maxEntryDimension and maxEntryBytes bound a cache cell before anything is
// allocated for it. Width and height come off the wire and the codec decoders
// allocate from them, so a limit applied after the decode would already have
// cost the memory it is meant to save.
const (
	maxEntryDimension = 1 << 14
	maxEntryBytes     = 16 << 20
)

// cacheKey identifies a cache slot. The server names a cache with two bits and
// an index within it.
type cacheKey struct {
	cacheID uint32
	index   uint32
}

// Entry is one bitmap in the cache, already decoded to BGRA.
type Entry struct {
	Width  int
	Height int
	// Pixels are BGRA, top down.
	Pixels []byte
}

// maxCacheBytes bounds everything the cache holds together. A per-entry bound
// does not bound the sum, and the server chooses both how many entries to send
// and how large each one is: with a few hundred bytes per order it can decode a
// cell of many megabytes, and it can choose the slot to put it in, so the total
// has to be capped as well.
const maxCacheBytes = 64 << 20

// Cache holds the bitmaps the server stores with secondary orders and reads
// back with MEMBLT.
//
// Entries are dropped rather than reused when the server overwrites a slot,
// which is what it does when its own cache fills up: the protocol has no
// eviction message for the client because the client is not expected to hand
// anything back.
type Cache struct {
	mu      sync.Mutex
	entries map[cacheKey]*Entry
	bytes   int

	// RFXMode is the entropy coder for RemoteFX cache entries. RemoteFX data
	// does not say which coder produced it, so this has to match what the
	// session negotiated. A cache keeps its own copy rather than reading
	// codec.RFXMode, since that global belongs to whichever session set it and
	// decoding garbage is worse than failing. Set it before the cache is used;
	// the default, RLGR1, is what codec.RFXMode defaults to and what every
	// server supports.
	RFXMode codec.RLGRMode
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{entries: make(map[cacheKey]*Entry), RFXMode: codec.RLGR1}
}

// Put stores a bitmap from a cache order. Entries the server says not to keep
// are still stored, because a MEMBLT usually follows immediately and expects to
// find them; they are simply not relied upon to last.
//
// The order's own pixels are in the bitmap's pixel format and are converted to
// BGRA here, so that blitting is a copy.
func (c *Cache) Put(cb *pdu.CacheBitmap) {
	if cb == nil || cb.Width <= 0 || cb.Height <= 0 || entryTooLarge(cb.Width, cb.Height) {
		return
	}

	var pixels []byte
	switch {
	case len(cb.Pixels) > 0:
		pixels = toBGRA(cb.Pixels, cb.Width, cb.Height, cb.Bpp)
	case cb.CodecID != 0 && len(cb.Data) > 0:
		// A revision 3 entry carries a codec id and data the codec encoded,
		// rather than plain pixels. Both codecs a cache can hold decode to BGRA
		// at 32bpp, which is what the rest of the cache holds, so nothing is
		// converted after this.
		decoded, err := c.decode(cb)
		if err != nil {
			// A payload that will not decode would only put rubbish in the cache
			// and then on screen, so leave the slot empty.
			return
		}
		pixels = decoded
	default:
		return
	}
	if len(pixels) != cb.Width*cb.Height*4 {
		return
	}

	key := cacheKey{cb.CacheID, cb.CacheIndex}

	c.mu.Lock()
	defer c.mu.Unlock()

	if old, ok := c.entries[key]; ok {
		c.bytes -= len(old.Pixels)
	}
	// Dropping everything is a blunt way to stay under the cap, and it is the
	// right one here: the entries a server sends next will refill whatever it
	// still needs, and a client that evicted selectively would have to guess
	// which slots matter.
	if c.bytes+len(pixels) > maxCacheBytes {
		c.entries = make(map[cacheKey]*Entry)
		c.bytes = 0
	}
	c.entries[key] = &Entry{Width: cb.Width, Height: cb.Height, Pixels: pixels}
	c.bytes += len(pixels)
}

// decode expands a revision 3 entry's payload with the codec its id names.
func (c *Cache) decode(cb *pdu.CacheBitmap) ([]byte, error) {
	if cb.CodecID == codec.CodecIDRemoteFX {
		// codec.Decompress would reach for the codec.RFXMode global; use the
		// cache's own coder instead.
		return codec.DecodeRFX(cb.Data, cb.Width, cb.Height, c.RFXMode)
	}
	return codec.Decompress(cb.CodecID, cb.Data, cb.Width, cb.Height, cb.Bpp)
}

// entryTooLarge reports whether a cell's geometry is too big to decode. The
// dimension check comes first so that the product cannot overflow on a 32 bit
// build.
func entryTooLarge(width, height int) bool {
	if width > maxEntryDimension || height > maxEntryDimension {
		return true
	}
	return width*height > maxEntryBytes/4
}

// Get returns a cache entry, or nil if the slot is empty. The caller must not
// modify the pixels.
func (c *Cache) Get(cacheID uint32, index uint32) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[cacheKey{cacheID, index}]
}

// Reset empties the cache. A reset graphics or a new connection invalidates
// everything in it.
func (c *Cache) Reset() {
	c.mu.Lock()
	c.entries = make(map[cacheKey]*Entry)
	c.bytes = 0
	c.mu.Unlock()
}

// Len is the number of entries held, for tests and diagnostics.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// toBGRA converts a bitmap in the cache's own format to BGRA. Only the formats
// that turn up in practice are handled: the measured traffic is all 32bpp, and
// 24bpp is a shift. The rest return nil, and the caller drops the entry rather
// than drawing something wrong.
func toBGRA(src []byte, width, height, bpp int) []byte {
	if width <= 0 || height <= 0 || bpp <= 0 {
		return nil
	}
	n := width * height
	out := make([]byte, n*4)
	switch bpp {
	case 32:
		if len(src) < n*4 {
			return nil
		}
		copy(out, src[:n*4])
	case 24:
		if len(src) < n*3 {
			return nil
		}
		for i := 0; i < n; i++ {
			out[i*4] = src[i*3]
			out[i*4+1] = src[i*3+1]
			out[i*4+2] = src[i*3+2]
			out[i*4+3] = 0xff
		}
	default:
		return nil
	}
	return out
}
