package orders

import (
	"fmt"
	"image"
	"sync"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// The three orders that parsed correctly but were counted as unsupported rather
// than drawn: LineTo, SaveBitmap and FastIndex. Each is a method on Screen, so
// the batch dispatch in Screen.drawOne reaches it with the screen lock already
// held and the clip rectangle already worked out.
//
// Every rectangle they touch goes through the fill, blit and clip helpers in
// screen.go, and FastIndex goes through DrawText2 in glyph.go; there is no
// second set of any of them here.

// lineTo draws a LINETO_ORDER: a straight line from its start to its end in the
// pen's colour.
//
// It reuses the one pixel line helper the Polyline order uses, so the segment is
// clipped to the order's bounds and the screen edge exactly the same way, and
// delta coordinates that have accumulated across orders cannot ask for more
// steps than there are pixels. FreeRDP's gdi_line_to also applies the pen width,
// pen style and the raster operation; only the colour is honoured here, which is
// what the thin pen almost every line carries draws.
func (s *Screen) lineTo(d *pdu.LineTo, clip image.Rectangle) image.Rectangle {
	if d == nil {
		return image.Rectangle{}
	}
	return s.line(pdu.Point{X: d.Startx, Y: d.Starty},
		pdu.Point{X: d.Endx, Y: d.Endy}, d.Pen.Colour, clip)
}

// The save bitmap operation values (MS-RDPEGDI's SAVE_BITMAP_ORDER): 0x01 saves
// the rectangle the order names, 0x02 restores it.
const (
	saveBitmapSave    = 0x01
	saveBitmapRestore = 0x02
)

// Bounds on the regions SAVE_BITMAP orders may keep. A region is clipped to the
// screen before anything is allocated for it, and the number of regions a screen
// may hold is capped, so a server cannot grow the store without limit.
const (
	maxSavedRegions = 32
	maxSavedBytes   = 16 << 20
)

// savedRegion is one region a SAVE_BITMAP order has stored: the pixels it
// captured, top down BGRA.
type savedRegion struct {
	width, height int
	pixels        []byte
}

// savedBitmaps holds the regions SAVE_BITMAP orders have stored, one table per
// screen, keyed by the savedBitmapPosition the order carries.
//
// The store outlives a single order because a save and its matching restore are
// separate updates, and it is keyed by the screen so that two Screens in one
// process do not read each other's saved bits. It lives here rather than as a
// field of Screen so the batch dispatch only has to add a case; the price is
// that a discarded Screen leaves its table behind, which a session with one
// screen never notices.
var savedBitmaps = struct {
	mu      sync.Mutex
	screens map[*Screen]map[uint32]savedRegion
}{screens: make(map[*Screen]map[uint32]savedRegion)}

// storeSaved keeps a region under an offset, replacing whatever was there. When
// a screen already holds the maximum number of regions the table is dropped,
// which is the same blunt eviction the bitmap cache uses.
func storeSaved(s *Screen, offset uint32, reg savedRegion) {
	if s == nil || len(reg.pixels) == 0 {
		return
	}
	savedBitmaps.mu.Lock()
	defer savedBitmaps.mu.Unlock()
	table := savedBitmaps.screens[s]
	if table == nil {
		table = make(map[uint32]savedRegion)
		savedBitmaps.screens[s] = table
	}
	if _, exists := table[offset]; !exists && len(table) >= maxSavedRegions {
		table = make(map[uint32]savedRegion)
		savedBitmaps.screens[s] = table
	}
	table[offset] = reg
}

// loadSaved returns the region stored under an offset, if any. The pixels are
// shared rather than copied, so the caller must not modify them.
func loadSaved(s *Screen, offset uint32) (savedRegion, bool) {
	savedBitmaps.mu.Lock()
	defer savedBitmaps.mu.Unlock()
	table := savedBitmaps.screens[s]
	if table == nil {
		return savedRegion{}, false
	}
	reg, ok := table[offset]
	return reg, ok
}

// saveBitmap handles a SAVE_BITMAP_ORDER: it either captures the rectangle the
// order names into the saved bitmap store, which changes nothing on screen, or
// blits a region back that an earlier order stored.
//
// The coordinates are inclusive, so the width of the rectangle is right-left+1,
// which is how rdesktop's process_desksave reads the same order. FreeRDP has no
// renderer for this order at all, so the operation values come from MS-RDPEGDI.
func (s *Screen) saveBitmap(d *pdu.SaveBitmap, clip image.Rectangle) image.Rectangle {
	if d == nil {
		return image.Rectangle{}
	}
	switch d.Operation() {
	case saveBitmapSave:
		s.saveBitmapRegion(d, clip)
		return image.Rectangle{}
	case saveBitmapRestore:
		return s.restoreBitmapRegion(d, clip)
	default:
		s.note(fmt.Sprintf("savebitmap operation 0x%02x", d.Operation()))
		return image.Rectangle{}
	}
}

// saveBitmapRegion captures the order's rectangle from the screen. The
// coordinates are clipped to the order's bounds and the screen edge before any
// arithmetic on them, so an out of range coordinate cannot ask for a buffer the
// order never carried.
func (s *Screen) saveBitmapRegion(d *pdu.SaveBitmap, clip image.Rectangle) {
	left, top, right, bottom, ok := clampInclusiveRect(
		d.Left, d.Top, d.Right, d.Bottom, clip)
	if !ok {
		return
	}
	w, h := right-left, bottom-top
	if w*h*4 > maxSavedBytes {
		return
	}
	pixels := make([]byte, 0, w*h*4)
	for row := top; row < bottom; row++ {
		off := (row*s.width + left) * 4
		pixels = append(pixels, s.pixels[off:off+w*4]...)
	}
	storeSaved(s, d.Offset, savedRegion{width: w, height: h, pixels: pixels})
}

// restoreBitmapRegion blits a stored region back at the position the restore
// order names. A restore of an offset that was never saved leaves the screen as
// it is and is counted, which is what a client that missed the save would show.
func (s *Screen) restoreBitmapRegion(d *pdu.SaveBitmap, clip image.Rectangle) image.Rectangle {
	reg, ok := loadSaved(s, d.Offset)
	if !ok {
		s.note("savebitmap restore of an empty slot")
		return image.Rectangle{}
	}
	// The destination is checked before it reaches blitBGRA, whose rectangle
	// arithmetic would otherwise overflow on a 32 bit int for a coordinate the
	// server picked. Anywhere at or past the screen's right or bottom edge draws
	// nothing, which is what the clip would do anyway.
	if int64(d.Left) >= int64(clip.Max.X) || int64(d.Top) >= int64(clip.Max.Y) {
		return image.Rectangle{}
	}
	return s.blitBGRA(reg.pixels, reg.width, 0, 0, reg.width, reg.height,
		int(d.Left), int(d.Top), clip)
}

// clampInclusiveRect turns an inclusive rectangle (left, top, right, bottom) into
// half open bounds inside clip, returning false when nothing of it is inside.
//
// Every coordinate is clamped before it is added to or subtracted from another,
// so neither the width nor the buffer size can be driven past the edge by a
// coordinate the server chose.
func clampInclusiveRect(left, top, right, bottom int32, clip image.Rectangle) (int, int, int, int, bool) {
	// The arithmetic is done in int64 so that adding one to an int32 the server
	// set to its maximum cannot wrap on a 32 bit int.
	l := int64(left)
	if l < int64(clip.Min.X) {
		l = int64(clip.Min.X)
	}
	t := int64(top)
	if t < int64(clip.Min.Y) {
		t = int64(clip.Min.Y)
	}
	r := int64(right) + 1
	if r > int64(clip.Max.X) {
		r = int64(clip.Max.X)
	}
	b := int64(bottom) + 1
	if b > int64(clip.Max.Y) {
		b = int64(clip.Max.Y)
	}
	if r <= l || b <= t {
		return 0, 0, 0, 0, false
	}
	return int(l), int(t), int(r), int(b), true
}

// drawFastIndex draws a FAST_INDEX_ORDER out of the glyph cache, the same way the
// TEXT2 order does: the data is the packed glyph run DrawText2 already knows how
// to walk, so it is turned into the GlyphIndex that carries it.
//
// FreeRDP's update_gdi_fast_index resolves a few sentinel coordinates before it
// draws, and those are applied here: an opBottom of -32768 means "erase all the
// way", and the low nibble of opTop says which of the background rectangle's
// edges to take instead; a zero opLeft or opRight takes the background edge; and
// an x or y of -32768 is the background corner. The order is drawn opaque, which
// is what FreeRDP passes as fOpRedundant.
func (s *Screen) drawFastIndex(d *pdu.FastIndex, clip image.Rectangle) image.Rectangle {
	if d == nil || s.Glyphs == nil {
		return image.Rectangle{}
	}

	opLeft, opTop, opRight, opBottom := d.OpLeft, d.OpTop, d.OpRight, d.OpBottom
	if opBottom == -32768 {
		flags := uint8(opTop) & 0x0f
		if flags&0x01 != 0 {
			opBottom = d.BkBottom
		}
		if flags&0x02 != 0 {
			opRight = d.BkRight
		}
		if flags&0x04 != 0 {
			opTop = d.BkTop
		}
		if flags&0x08 != 0 {
			opLeft = d.BkLeft
		}
	}
	if opLeft == 0 {
		opLeft = d.BkLeft
	}
	if opRight == 0 {
		opRight = d.BkRight
	}
	x, y := d.X, d.Y
	if x == -32768 {
		x = d.BkLeft
	}
	if y == -32768 {
		y = d.BkTop
	}

	return s.DrawText2(&pdu.GlyphIndex{
		CacheID:      d.CacheId,
		FlAccel:      d.FlAccel,
		UlCharInc:    d.CharInc,
		FOpRedundant: 0,
		BackColor:    d.BackColour,
		ForeColor:    d.ForeColour,
		BkLeft:       d.BkLeft,
		BkTop:        d.BkTop,
		BkRight:      d.BkRight,
		BkBottom:     d.BkBottom,
		OpLeft:       opLeft,
		OpTop:        opTop,
		OpRight:      opRight,
		OpBottom:     opBottom,
		X:            x,
		Y:            y,
		Data:         d.Data,
	}, s.Glyphs, clip)
}
