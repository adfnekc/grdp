package pdu

import (
	"fmt"
	"sync"
)

// maxPointerMaskBytes bounds one pointer's mask, so that a malformed update
// cannot ask for an allocation of its own choosing. A cursor is small: the
// largest Windows sends is a few hundred pixels square.
const maxPointerMaskBytes = 1 << 20

// PointerXorRowBytes is the size of one scan line of a pointer's colour mask.
//
// It is not simply XorMaskRowBytes, because a two colour pointer has one bit per
// pixel rather than one byte: there, XorBpp is one meaning one bit, and the
// colour mask's rows are the same size as the AND mask's. Treating that one as a
// byte gives a row twice as wide as the wire, which reads the mask skewed.
// FreeRDP's own decoder uses the same row size for both masks when the depth is
// one, and only flips the rows when it is not.
func PointerXorRowBytes(width, xorBpp int) int {
	if xorBpp == 1 {
		return AndMaskRowBytes(width)
	}
	return XorMaskRowBytes(width, xorBpp)
}

// AndMaskRowBytes is the size of one scan line of an AND mask: one bit per
// pixel, padded to a two byte boundary. MS-RDPBCGR 2.2.9.1.1.4.4 states the
// padding for the XOR mask and gives a worked example for it; the AND mask is
// padded the same way, which is what makes the two masks line up.
func AndMaskRowBytes(width int) int {
	if width <= 0 {
		return 0
	}
	return (width + 15) / 16 * 2
}

// XorMaskRowBytes is the size of one scan line of an XOR mask: bpp bytes per
// pixel, padded to a two byte boundary.
//
// The padding is not optional and is not a rounding of the total. The
// specification's own example is a 3x3 cursor at 24bpp, where each scan line
// consumes ten bytes rather than nine: three pixels of three bytes is nine,
// rounded up to the next even number. Sizing the whole mask as width*height*bpp
// reads every row after the first from one byte too early, so a cursor whose
// width is odd comes out skewed and the bytes after it belong to the next field.
func XorMaskRowBytes(width, bpp int) int {
	if width <= 0 || bpp <= 0 {
		return 0
	}
	return (width*bpp + 1) / 2 * 2
}

// System pointer types, from the table in MS-RDPBCGR 2.2.9.1.1.4.3.
//
// That table names only these two. Windows and its clients use a longer run of
// values starting at SYSPTR_DEFAULT for the arrow, the I beam, the hourglass and
// the rest, but the specification does not list them, so they are not named here:
// a caller that wants to map them can switch on PointerDataPDU.SystemType, which
// is passed through as it arrived.
const (
	SYSPTR_NULL    = 0x00000000
	SYSPTR_DEFAULT = 0x00007F00
)

// PointerShape is one cursor shape as the server sent it.
//
// The masks are kept in the layout they arrived in, scan line by scan line with
// the padding above and, for the XOR mask, bottom up: the last row on the wire
// is the top row of the cursor. CursorImage does the conversion, and callers
// almost always want that rather than this.
type PointerShape struct {
	Width  int
	Height int

	// Hotspot is the point in the shape that sits on the pointer's position.
	HotspotX int
	HotspotY int

	// XorBpp is the bytes per pixel of Xor: three for a 24bpp colour pointer,
	// four for a 32bpp one, and one bit per pixel for a monochrome pointer,
	// which arrives as 1bpp inside a bitmap.
	XorBpp int

	// Xor is the colour, or for a monochrome pointer the second of the two
	// masks that together decide each pixel.
	Xor []byte

	// And is one bit per pixel: zero means the colour is drawn as it is, one
	// means the colour is a mask against whatever is underneath.
	And []byte
}

// PointerCache holds the pointer shapes the server has asked the client to keep.
//
// The client advertises how many slots it has, in the pointer capability set,
// and the server may then send a shape once and refer to it by index forever
// after. Without the cache those later updates carry no pixels at all, so the
// pointer disappears: a shape that is merely undecodable is visible, and one
// that was never kept is not.
type PointerCache struct {
	mu    sync.Mutex
	slots []*PointerShape
}

// NewPointerCache returns a cache with the given number of slots, which must
// match the capability the client advertised. A size of zero or less gives one
// slot, since index zero is legal and a zero length slice would panic on it.
func NewPointerCache(size int) *PointerCache {
	if size <= 0 {
		size = 1
	}
	return &PointerCache{slots: make([]*PointerShape, size)}
}

// Size is the number of slots.
func (c *PointerCache) Size() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.slots)
}

// Put stores a copy of a shape in a slot. An out of range index is refused
// rather than wrapping: overwriting slot zero with a shape meant for slot twenty
// would make some other cursor wrong, which is worse than dropping this one.
//
// The copy matters because the bytes come from a buffer the read loop reuses:
// storing the slice would leave the cache pointing at whatever the next update
// wrote there, so a cursor would change to something the server never sent.
func (c *PointerCache) Put(index uint16, s *PointerShape) error {
	if c == nil {
		return fmt.Errorf("pdu: no pointer cache")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if int(index) >= len(c.slots) {
		return fmt.Errorf("pdu: pointer slot %d is past the %d slots advertised", index, len(c.slots))
	}
	c.slots[index] = s.Fill()
	return nil
}

// Get returns the shape in a slot. A slot the server never filled returns false,
// so that a cached pointer with nothing behind it can be reported as a gap
// rather than turned into an empty cursor.
func (c *PointerCache) Get(index uint16) (*PointerShape, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if int(index) >= len(c.slots) {
		return nil, false
	}
	s := c.slots[index]
	return s, s != nil
}

// Fill returns a fresh copy of the shape, so that a caller cannot alter what the
// cache holds and leave a later lookup with a shape the server never sent.
func (s *PointerShape) Fill() *PointerShape {
	if s == nil {
		return nil
	}
	out := *s
	out.Xor = append([]byte(nil), s.Xor...)
	out.And = append([]byte(nil), s.And...)
	return &out
}

// ColorPointerCacheSize is the number of cursor slots this client advertises in
// its pointer capability set.
//
// It is exported so that the client's cache can be built with the same number.
// The server indexes the slots against what was advertised, so a cache of a
// different size silently loses shapes: the ones past its end are dropped, and
// the ones the server keeps sending are looked up in a slot that was never
// filled.
func (c *Client) ColorPointerCacheSize() uint16 {
	if c == nil || c.clientCapabilities == nil {
		return 0
	}
	cap, ok := c.clientCapabilities[CAPSTYPE_POINTER].(*PointerCapability)
	if !ok || cap == nil {
		return 0
	}
	return cap.ColorPointerCacheSize
}
