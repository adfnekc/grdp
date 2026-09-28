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

	"github.com/adfnekc/grdp/protocol/pdu"
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
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{entries: make(map[cacheKey]*Entry)}
}

// Put stores a bitmap from a cache order. Entries the server says not to keep
// are still stored, because a MEMBLT usually follows immediately and expects to
// find them; they are simply not relied upon to last.
//
// The order's own pixels are in the bitmap's pixel format and are converted to
// BGRA here, so that blitting is a copy.
func (c *Cache) Put(cb *pdu.CacheBitmap) {
	if cb == nil || cb.Width <= 0 || cb.Height <= 0 {
		return
	}

	var pixels []byte
	switch {
	case len(cb.Pixels) > 0:
		pixels = toBGRA(cb.Pixels, cb.Width, cb.Height, cb.Bpp)
	case cb.CodecID != 0 && len(cb.Data) > 0:
		// A revision 3 entry carries a codec id and encoded data. Those are not
		// decoded here.
		return
	default:
		return
	}
	if len(pixels) != cb.Width*cb.Height*4 {
		return
	}

	c.mu.Lock()
	c.entries[cacheKey{cb.CacheID, cb.CacheIndex}] = &Entry{
		Width:  cb.Width,
		Height: cb.Height,
		Pixels: pixels,
	}
	c.mu.Unlock()
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
