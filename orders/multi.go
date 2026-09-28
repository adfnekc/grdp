package orders

import (
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
// MEM3BLT is a MEMBLT against a third colour table, so its drawing is MEMBLT's.
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

// mem3blt draws a MEMBLT whose bitmap comes with a third colour table. The
// colour table only matters for a palettised bitmap, which is not decoded here,
// so the pixels are copied out of the cache exactly as MEMBLT's are.
func (s *Screen) mem3blt(d *pdu.Mem3blt, clip image.Rectangle) image.Rectangle {
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

// multiScrBlt copies each of the order's rectangles from the source origin the
// order carries. The single-rectangle SCRBLT takes an independent source origin
// and a destination rectangle of the same size, so each rectangle here is a
// destination that copies the source at that rectangle's size.
func (s *Screen) multiScrBlt(d *pdu.MultiScrBlt, clip image.Rectangle) image.Rectangle {
	var dirty image.Rectangle
	for _, r := range multiRects(d.Rectangles, d.NumRectangles) {
		dirty = union(dirty, s.scrblt(&pdu.Scrblt{
			X: r.Left, Y: r.Top, Cx: r.Width, Cy: r.Height,
			Opcode: d.Opcode,
			Srcx:   d.Srcx,
			Srcy:   d.Srcy,
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
