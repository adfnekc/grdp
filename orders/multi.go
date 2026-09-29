package orders

import (
	"fmt"
	"image"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// DrawMulti draws the primary orders that carry a list of rectangles, and
// MEM3BLT. It is the single entry point the batch dispatch calls for them.
//
// The four multi-rectangle orders are MULTI_DSTBLT, MULTI_PATBLT, MULTI_SCRBLT
// and MULTI_OPAQUE_RECT. Each is the matching single-rectangle order repeated
// over its rectangle list, so each is drawn here by filling in that order's
// value and calling the renderer that already exists for it. That is what
// reuses the raster operation handling, the clipping and the fill and blit
// helpers, instead of a second copy of them.
//
// FreeRDP renders only MultiOpaqueRect of the four: its GDI client leaves the
// other three unset, so there is no reference drawing to copy for them. Drawing
// each rectangle on its own is the reading their fields support, and the bounds
// the order carries still clip the whole lot.
//
// MEM3BLT is a MEMBLT against a third colour table that also carries a raster
// operation, so it copies only when the operation is SRCCOPY.
//
// The batch dispatch calls this once per order from Screen.drawOne, where the
// screen lock is already held and the clip rectangle is known, so it must not be
// called concurrently with other drawing.
func (s *Screen) DrawMulti(o *pdu.OrderPdu, clip image.Rectangle) image.Rectangle {
	if o == nil || o.Primary == nil {
		return image.Rectangle{}
	}
	switch d := o.Primary.Data.(type) {
	case *pdu.Mem3blt:
		return s.mem3blt(d, clip)
	case *pdu.MultiDstBlt:
		return s.multiDstBlt(d, clip)
	case *pdu.MultiPatBlt:
		return s.multiPatBlt(d, clip)
	case *pdu.MultiScrBlt:
		return s.multiScrBlt(d, clip)
	case *pdu.MultiOpaqueRect:
		return s.multiOpaqueRect(d, clip)
	default:
		return image.Rectangle{}
	}
}

// mem3blt draws a MEMBLT whose bitmap comes with a third colour table and a
// raster operation. The colour table only matters for a palettised bitmap,
// which is not decoded here, and the bitmap itself is copied out of the cache
// as MEMBLT's is. Unlike MEMBLT, MEM3BLT's bRop is a real raster operation, so
// drawing it as an unconditional copy is wrong for anything but SRCCOPY; the
// rest are counted instead of drawn as the wrong thing.
func (s *Screen) mem3blt(d *pdu.Mem3blt, clip image.Rectangle) image.Rectangle {
	if d.Opcode != ropSrcCopy {
		s.note(fmt.Sprintf("mem3blt rop 0x%02x", d.Opcode))
		return image.Rectangle{}
	}
	return s.memblt(&pdu.Memblt{
		CacheId:     d.CacheId,
		ColourTable: d.ColourTable,
		X:           d.X,
		Y:           d.Y,
		Cx:          d.Cx,
		Cy:          d.Cy,
		Srcx:        d.Srcx,
		Srcy:        d.Srcy,
		CacheIdx:    d.CacheIdx,
	}, clip)
}

// multiRects returns the rectangles to draw. It is not always the whole list:
// an order may keep the previous order's rectangles and only change the count,
// and then only the first count of them are meant.
func multiRects(rects []pdu.DeltaRect, count int) []pdu.DeltaRect {
	if count < 0 {
		count = 0
	}
	if count > len(rects) {
		count = len(rects)
	}
	return rects[:count]
}

// multiDstBlt paints each of the order's rectangles with its destination-only
// raster operation.
func (s *Screen) multiDstBlt(d *pdu.MultiDstBlt, clip image.Rectangle) image.Rectangle {
	var dirty image.Rectangle
	for _, r := range multiRects(d.Rectangles, d.NumRectangles) {
		dirty = union(dirty, s.dstblt(&pdu.Dstblt{
			X: r.Left, Y: r.Top, Cx: r.Width, Cy: r.Height,
			Opcode: d.Opcode,
		}, clip))
	}
	return dirty
}

// multiPatBlt paints each of the order's rectangles from its brush.
func (s *Screen) multiPatBlt(d *pdu.MultiPatBlt, clip image.Rectangle) image.Rectangle {
	var dirty image.Rectangle
	for _, r := range multiRects(d.Rectangles, d.NumRectangles) {
		dirty = union(dirty, s.patblt(&pdu.Patblt{
			X: r.Left, Y: r.Top, Cx: r.Width, Cy: r.Height,
			Opcode:   d.Opcode,
			BgColour: d.BgColour,
			FgColour: d.FgColour,
			Brush:    d.Brush,
		}, clip))
	}
	return dirty
}

// multiScrBlt copies part of the screen to each of the order's rectangles.
//
// The order carries one source origin and a list of destination rectangles, and
// each rectangle reads the source at the same translation the order applies as
// a whole: a rectangle at (r.Left, r.Top) reads
// (nXSrc + r.Left - nLeftRect, nYSrc + r.Top - nTopRect). FreeRDP has no
// renderer to check this against, since its Windows client sets only
// MultiOpaqueRect and leaves this handler unset, so the reading comes from
// MS-RDPEGDI's annotated MultiScrBlt dump: there the destination rectangle is
// exactly the bounds of the delta rectangles and the source origin is that
// rectangle shifted, which is only consistent with the whole shape moving by
// one vector. Copying from the source origin at each rectangle's own size would
// instead read a single source row for a shape that spans many.
func (s *Screen) multiScrBlt(d *pdu.MultiScrBlt, clip image.Rectangle) image.Rectangle {
	var dirty image.Rectangle
	for _, r := range multiRects(d.Rectangles, d.NumRectangles) {
		dirty = union(dirty, s.scrblt(&pdu.Scrblt{
			X: r.Left, Y: r.Top, Cx: r.Width, Cy: r.Height,
			Opcode: d.Opcode,
			Srcx:   d.Srcx + r.Left - d.X,
			Srcy:   d.Srcy + r.Top - d.Y,
		}, clip))
	}
	return dirty
}

// multiOpaqueRect fills each of the order's rectangles with its colour.
func (s *Screen) multiOpaqueRect(d *pdu.MultiOpaqueRect, clip image.Rectangle) image.Rectangle {
	var dirty image.Rectangle
	for _, r := range multiRects(d.Rectangles, d.NumRectangles) {
		dirty = union(dirty, s.opaqueRect(&pdu.OpaqueRect{
			X: r.Left, Y: r.Top, Cx: r.Width, Cy: r.Height,
			Colour: d.Colour,
		}, clip))
	}
	return dirty
}
