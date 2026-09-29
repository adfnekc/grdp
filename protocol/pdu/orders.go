package pdu

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/adfnekc/grdp/glog"

	"github.com/adfnekc/grdp/core"
)

type ControlFlag uint8

const (
	TS_STANDARD             = 0x01
	TS_SECONDARY            = 0x02
	TS_BOUNDS               = 0x04
	TS_TYPE_CHANGE          = 0x08
	TS_DELTA_COORDINATES    = 0x10
	TS_ZERO_BOUNDS_DELTAS   = 0x20
	TS_ZERO_FIELD_BYTE_BIT0 = 0x40
	TS_ZERO_FIELD_BYTE_BIT1 = 0x80
)

type PrimaryOrderType uint8

const (
	ORDER_TYPE_DSTBLT = 0x00 //0
	ORDER_TYPE_PATBLT = 0x01 //1
	ORDER_TYPE_SCRBLT = 0x02 //2
	//ORDER_TYPE_DRAWNINEGRID       = 0x07 //7
	//ORDER_TYPE_MULTI_DRAWNINEGRID = 0x08 //8
	ORDER_TYPE_LINETO     = 0x09 //9
	ORDER_TYPE_OPAQUERECT = 0x0A //10
	ORDER_TYPE_SAVEBITMAP = 0x0B //11
	ORDER_TYPE_MEMBLT     = 0x0D //13
	ORDER_TYPE_MEM3BLT    = 0x0E //14
	//ORDER_TYPE_MULTIDSTBLT        = 0x0F //15
	//ORDER_TYPE_MULTIPATBLT        = 0x10 //16
	//ORDER_TYPE_MULTISCRBLT        = 0x11 //17
	//ORDER_TYPE_MULTIOPAQUERECT    = 0x12 //18
	//ORDER_TYPE_FAST_INDEX         = 0x13 //19
	ORDER_TYPE_POLYGON_SC = 0x14 //20
	ORDER_TYPE_POLYGON_CB = 0x15 //21
	ORDER_TYPE_POLYLINE   = 0x16 //22
	//ORDER_TYPE_FAST_GLYPH         = 0x18 //24
	ORDER_TYPE_ELLIPSE_SC = 0x19 //25
	ORDER_TYPE_ELLIPSE_CB = 0x1A //26
	ORDER_TYPE_TEXT2      = 0x1B //27
)

type SecondaryOrderType uint8

const (
	ORDER_TYPE_BITMAP_UNCOMPRESSED     = 0x00
	ORDER_TYPE_CACHE_COLOR_TABLE       = 0x01
	ORDER_TYPE_CACHE_BITMAP_COMPRESSED = 0x02
	ORDER_TYPE_CACHE_GLYPH             = 0x03
	ORDER_TYPE_BITMAP_UNCOMPRESSED_V2  = 0x04
	ORDER_TYPE_BITMAP_COMPRESSED_V2    = 0x05
	ORDER_TYPE_CACHE_BRUSH             = 0x07
	ORDER_TYPE_BITMAP_COMPRESSED_V3    = 0x08
)

func (s SecondaryOrderType) String() string {
	name := "Unknown"
	switch s {
	case ORDER_TYPE_BITMAP_UNCOMPRESSED:
		name = "Cache Bitmap"
	case ORDER_TYPE_CACHE_COLOR_TABLE:
		name = "Cache Color Table"
	case ORDER_TYPE_CACHE_BITMAP_COMPRESSED:
		name = "Cache Bitmap (Compressed)"
	case ORDER_TYPE_CACHE_GLYPH:
		name = "Cache Glyph"
	case ORDER_TYPE_BITMAP_UNCOMPRESSED_V2:
		name = "Cache Bitmap V2"
	case ORDER_TYPE_BITMAP_COMPRESSED_V2:
		name = "Cache Bitmap V2 (Compressed)"
	case ORDER_TYPE_CACHE_BRUSH:
		name = "Cache Brush"
	case ORDER_TYPE_BITMAP_COMPRESSED_V3:
		name = "Cache Bitmap V3"
	}
	return fmt.Sprintf("[0x%02d] %s", s, name)
}

/* Alternate Secondary Drawing Orders */
const (
	ORDER_TYPE_SWITCH_SURFACE          = 0x00
	ORDER_TYPE_CREATE_OFFSCREEN_BITMAP = 0x01
	ORDER_TYPE_STREAM_BITMAP_FIRST     = 0x02
	ORDER_TYPE_STREAM_BITMAP_NEXT      = 0x03
	ORDER_TYPE_CREATE_NINE_GRID_BITMAP = 0x04
	ORDER_TYPE_GDIPLUS_FIRST           = 0x05
	ORDER_TYPE_GDIPLUS_NEXT            = 0x06
	ORDER_TYPE_GDIPLUS_END             = 0x07
	ORDER_TYPE_GDIPLUS_CACHE_FIRST     = 0x08
	ORDER_TYPE_GDIPLUS_CACHE_NEXT      = 0x09
	ORDER_TYPE_GDIPLUS_CACHE_END       = 0x0A
	ORDER_TYPE_WINDOW                  = 0x0B
	ORDER_TYPE_COMPDESK_FIRST          = 0x0C
	ORDER_TYPE_FRAME_MARKER            = 0x0D
)

const (
	GLYPH_FRAGMENT_NOP = 0x00
	GLYPH_FRAGMENT_USE = 0xFE
	GLYPH_FRAGMENT_ADD = 0xFF

	CBR2_HEIGHT_SAME_AS_WIDTH      = 0x01
	CBR2_PERSISTENT_KEY_PRESENT    = 0x02
	CBR2_NO_BITMAP_COMPRESSION_HDR = 0x08
	CBR2_DO_NOT_CACHE              = 0x10
)

const (
	ORDER_PRIMARY = iota
	ORDER_SECONDARY
	ORDER_ALTSEC
)

type OrderPdu struct {
	ControlFlags uint8
	Type         int
	Altsec       *Altsec
	Primary      *Primary
	Secondary    *Secondary

	// state is the connection's parsing state, supplied by the batch.
	state *OrderState
}

func (o *OrderPdu) HasBounds() bool {
	return o.ControlFlags&TS_BOUNDS != 0
}

type Altsec struct {
}

type Secondary struct {
	// CacheBitmap is set by the cache orders, which are how the server fills
	// its bitmap cache. MEMBLT then blits those entries to the screen, so
	// without storing them there is nothing for MEMBLT to read.
	CacheBitmap *CacheBitmap

	// CacheBrush is set by the brush cache order, which PATBLT draws with.
	CacheBrush *CacheBrush

	// CacheGlyphs is set by the glyph cache order, which the TEXT2 order draws
	// from.
	CacheGlyphs *GlyphCacheOrder
}

// CacheBrush is a brush a secondary order puts into the brush cache, or the one
// PATBLT carries inline.
type CacheBrush struct {
	Index uint8
	// Bpp is bits per pixel for a colour brush; 1 means the 8x8 pattern in Data
	// is monochrome.
	Bpp    uint8
	Width  int
	Height int
	Style  uint8
	Hatch  uint8
	// Data is the pattern, top down, eight bytes of eight pixels for a
	// monochrome brush.
	Data []byte
}

// IsSolid reports whether the brush paints one colour, which is what the fills
// a server sends almost always want.
func (b *CacheBrush) IsSolid() bool {
	return b == nil || b.Style == 0 || len(b.Data) == 0
}

// CacheBitmap is a bitmap a secondary order puts into the bitmap cache. The
// pixels are decoded on arrival, so blitting one is a straight copy.
type CacheBitmap struct {
	// CacheID is one of the four caches a client advertises, and CacheIndex is
	// the slot within it.
	CacheID    uint32
	CacheIndex uint32

	Width  int
	Height int
	// Bpp is bits per pixel of Pixels, and is 8, 16, 24 or 32.
	Bpp int
	// Pixels are top down, in Bpp format, already decompressed.
	Pixels []byte

	// NotCached is set when the order says the entry must not be kept, which is
	// what a bitmap that is blitted once and discarded looks like.
	NotCached bool

	// CodecID is non-zero for a revision 3 entry whose data is encoded with a
	// bitmap codec rather than compressed. Pixels is empty in that case and
	// Data holds the encoded bytes.
	CodecID uint8
	Data    []byte
}

// cachePixels decodes a cache order's bitmap. The compression is the same
// RDP 6.0 scheme used for bitmap updates, so it is the same decoder; the
// argument is bytes per pixel there, which is why it is divided by eight.
func cachePixels(data []byte, width, height, bitsPerPixel int, compressed bool) []byte {
	if width <= 0 || height <= 0 || bitsPerPixel <= 0 || bitsPerPixel%8 != 0 {
		return nil
	}
	// The decoder allocates width by height up front, so a declared size that
	// cannot be a cache cell has to be refused before it gets there: a one byte
	// payload with a 32767 square size is four gigabytes.
	bpp := bitsPerPixel / 8
	if width > 1<<14 || height > 1<<14 || width*height*bpp > maxCachePixels {
		glog.Debugf("cache bitmap: refusing %dx%d at %d bpp", width, height, bitsPerPixel)
		return nil
	}
	if !compressed {
		if len(data) != width*height*bpp {
			return nil
		}
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}
	if len(data) == 0 {
		return nil
	}
	pixels, err := core.Decompress(data, width, height, bpp)
	if err != nil {
		// A truncated entry would only put rubbish in the cache and then on
		// screen, so leave the slot empty and let the server refill it.
		glog.Debugf("cache bitmap: %v", err)
		return nil
	}
	return pixels
}

type Primary struct {
	Bounds Bounds
	Data   PrimaryOrder
}

type FastPathOrdersPDU struct {
	// State is the connection's order parsing state. It has to be the same one
	// for every update on a connection, since delta coordinates refer back to
	// earlier orders.
	State *OrderState

	NumberOrders uint16
	OrderPdus    []OrderPdu
}

func (*FastPathOrdersPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_ORDERS
}

func (f *FastPathOrdersPDU) Unpack(r io.Reader) error {
	f.NumberOrders, _ = core.ReadUint16LE(r)
	//glog.Info("NumberOrders:", f.NumberOrders)
	for i := 0; i < int(f.NumberOrders); i++ {
		o := OrderPdu{state: f.State}
		o.ControlFlags, _ = core.ReadUInt8(r)

		// An order that cannot be read has to stop the batch. Its fields have
		// unknown length, so carrying on means reading whatever follows as if it
		// were the next order's flags, and everything after it in the batch is
		// nonsense. Failing leaves the rest of the screen as it was.
		var err error
		switch {
		case o.ControlFlags&TS_STANDARD == 0:
			err = o.processAltsecOrder(r)
			o.Type = ORDER_ALTSEC
		case o.ControlFlags&TS_SECONDARY != 0:
			err = o.processSecondaryOrder(r)
			o.Type = ORDER_SECONDARY
		default:
			err = o.processPrimaryOrder(r)
			o.Type = ORDER_PRIMARY
		}
		if err != nil {
			return fmt.Errorf("orders: order %d of %d: %w", i+1, f.NumberOrders, err)
		}

		if f.OrderPdus == nil {
			f.OrderPdus = make([]OrderPdu, 0, f.NumberOrders)
		}
		f.OrderPdus = append(f.OrderPdus, o)
	}
	return nil
}
func (o *OrderPdu) processAltsecOrder(r io.Reader) error {
	orderType := o.ControlFlags >> 2
	//glog.Info("Altsec:", orderType)
	switch orderType {
	case ORDER_TYPE_SWITCH_SURFACE:
	case ORDER_TYPE_CREATE_OFFSCREEN_BITMAP:
	case ORDER_TYPE_STREAM_BITMAP_FIRST:
	case ORDER_TYPE_STREAM_BITMAP_NEXT:
	case ORDER_TYPE_CREATE_NINE_GRID_BITMAP:
	case ORDER_TYPE_GDIPLUS_FIRST:
	case ORDER_TYPE_GDIPLUS_NEXT:
	case ORDER_TYPE_GDIPLUS_END:
	case ORDER_TYPE_GDIPLUS_CACHE_FIRST:
	case ORDER_TYPE_GDIPLUS_CACHE_NEXT:
	case ORDER_TYPE_GDIPLUS_CACHE_END:
	case ORDER_TYPE_WINDOW:
	case ORDER_TYPE_COMPDESK_FIRST:
	case ORDER_TYPE_FRAME_MARKER:
		core.ReadUInt32LE(r)
	}

	return nil
}
func (o *OrderPdu) processSecondaryOrder(r io.Reader) error {
	var sec Secondary
	length, _ := core.ReadUint16LE(r)
	flags, _ := core.ReadUint16LE(r)
	orderType, _ := core.ReadUInt8(r)

	glog.Info("Secondary:", SecondaryOrderType(orderType))

	b, _ := core.ReadBytes(int(length)+13-6, r)
	r0 := bytes.NewReader(b)

	switch orderType {
	case ORDER_TYPE_BITMAP_UNCOMPRESSED:
		fallthrough
	case ORDER_TYPE_CACHE_BITMAP_COMPRESSED:
		compressed := (orderType == ORDER_TYPE_CACHE_BITMAP_COMPRESSED)
		if err := sec.updateCacheBitmapOrder(r0, compressed, flags); err != nil {
			return fmt.Errorf("cache bitmap order: %w", err)
		}
	case ORDER_TYPE_BITMAP_UNCOMPRESSED_V2:
		fallthrough
	case ORDER_TYPE_BITMAP_COMPRESSED_V2:
		compressed := (orderType == ORDER_TYPE_BITMAP_COMPRESSED_V2)
		sec.updateCacheBitmapV2Order(r0, compressed, flags)
	case ORDER_TYPE_BITMAP_COMPRESSED_V3:
		sec.updateCacheBitmapV3Order(r0, flags)
	case ORDER_TYPE_CACHE_COLOR_TABLE:
		sec.updateCacheColorTableOrder(r0, flags)
	case ORDER_TYPE_CACHE_GLYPH:
		glyphs, err := ParseGlyphCacheOrder(b, flags)
		if err != nil {
			return fmt.Errorf("cache glyph order: %w", err)
		}
		sec.CacheGlyphs = glyphs
	case ORDER_TYPE_CACHE_BRUSH:
		sec.updateCacheBrushOrder(r0, flags)
	default:
		glog.Debugf("Unsupport order type 0x%x", orderType)
	}

	// The parsed order has to be attached: a renderer reaches the cache bitmaps
	// through it, and leaving it out makes every cache fill look like an order
	// nobody understood.
	o.Secondary = &sec
	return nil
}
func (b *Bounds) updateBounds(r io.Reader) {
	present, _ := core.ReadUInt8(r)

	if present&1 != 0 {
		readOrderCoord(r, &b.Left, false)
	} else if present&16 != 0 {
		readOrderCoord(r, &b.Left, true)
	}

	if present&2 != 0 {
		readOrderCoord(r, &b.Top, false)
	} else if present&32 != 0 {
		readOrderCoord(r, &b.Top, true)
	}

	if present&4 != 0 {
		readOrderCoord(r, &b.Right, false)
	} else if present&64 != 0 {
		readOrderCoord(r, &b.Right, true)
	}
	if present&8 != 0 {
		readOrderCoord(r, &b.Bottom, false)
	} else if present&128 != 0 {
		readOrderCoord(r, &b.Bottom, true)
	}
}

type PrimaryOrder interface {
	Type() int
	Unpack(io.Reader, uint32, bool) error
}

// OrderState is the order parsing state that outlives a single update: the last
// order type, the last bounds, and the last order of each type.
//
// All three have to persist across updates, because an order with
// TS_DELTA_COORDINATES repeats the fields the previous order of its type left
// out. They used to be package level variables, which meant every connection in
// a process shared them and one connection's orders filled in another's gaps.
// This belongs to a connection, so it is handed to the batch being parsed.
type OrderState struct {
	orderType uint8
	bounds    Bounds
	delta     map[int]PrimaryOrder
}

// NewOrderState returns fresh parsing state for one connection.
func NewOrderState() *OrderState {
	return &OrderState{delta: make(map[int]PrimaryOrder)}
}

// Reset forgets the previous orders, which is what a server starting over
// implies.
func (st *OrderState) Reset() {
	st.orderType = 0
	st.bounds = Bounds{}
	st.delta = make(map[int]PrimaryOrder)
}

// copyPrimaryOrder overwrites dst with the fields of src when they are the same
// concrete type, leaving dst alone otherwise.
func copyPrimaryOrder(dst, src PrimaryOrder) {
	d := reflect.ValueOf(dst)
	s := reflect.ValueOf(src)
	if d.Kind() != reflect.Ptr || d.IsNil() || s.Kind() != reflect.Ptr || s.IsNil() {
		return
	}
	if d.Elem().Type() != s.Elem().Type() {
		return
	}
	d.Elem().Set(s.Elem())
}

// stateFor returns the parser state for this order, allocating a throwaway one
// when an order is parsed on its own rather than as part of a connection.
func (o *OrderPdu) stateFor() *OrderState {
	if o.state == nil {
		o.state = NewOrderState()
	}
	return o.state
}

func (o *OrderPdu) processPrimaryOrder(r io.Reader) error {
	st := o.stateFor()
	o.Primary = &Primary{}
	if o.ControlFlags&TS_TYPE_CHANGE != 0 {
		st.orderType, _ = core.ReadUInt8(r)
	}
	orderType := st.orderType
	size := 1
	switch orderType {
	case ORDER_TYPE_MEM3BLT, ORDER_TYPE_TEXT2:
		size = 3

	case ORDER_TYPE_PATBLT, ORDER_TYPE_MEMBLT, ORDER_TYPE_LINETO, ORDER_TYPE_POLYGON_CB, ORDER_TYPE_ELLIPSE_CB:
		size = 2
	}
	// The multi rectangle orders carry their own field count, which is more
	// than three in places, so they say how many bytes of flags they have.
	if n, ok := MultiFieldBytes(orderType); ok {
		size = n
	}
	if n, ok := MissingFieldBytes(orderType); ok {
		size = n
	}

	// The field flags shrink by a byte for each of these control flags, and the
	// bytes that remain are read from the bottom up.
	//
	// It is tempting to read this the other way, with the absent byte being the
	// low one and the bytes that follow shifted up to make room for a zero. That
	// reading is wrong here, and the wire settles it: a MEMBLT with only one
	// flag byte carries 0x02, which is the left coordinate. Shifting it up gives
	// 0x200, which is bit nine of a nine bit field, so it would name no field at
	// all.
	if o.ControlFlags&TS_ZERO_FIELD_BYTE_BIT0 != 0 {
		size--
	}
	if o.ControlFlags&TS_ZERO_FIELD_BYTE_BIT1 != 0 {
		if size < 2 {
			size = 0
		} else {
			size -= 2
		}
	}
	var present uint32
	for i := 0; i < size; i++ {
		bits, _ := core.ReadUInt8(r)
		present |= uint32(bits) << (i * 8)
	}

	if o.ControlFlags&TS_BOUNDS != 0 {
		if o.ControlFlags&TS_ZERO_BOUNDS_DELTAS == 0 {
			st.bounds.updateBounds(r)
		}
		//glog.Infof("updateBounds")
		o.Primary.Bounds = st.bounds
	}

	delta := o.ControlFlags&TS_DELTA_COORDINATES != 0

	//glog.Infof("present=%d,delta=%v", present, delta)

	var p PrimaryOrder
	switch orderType {
	case ORDER_TYPE_DSTBLT:
		p = &Dstblt{}

	case ORDER_TYPE_PATBLT:
		p = &Patblt{}

	case ORDER_TYPE_SCRBLT:
		p = &Scrblt{}

	//case ORDER_TYPE_DRAWNINEGRID:

	//case ORDER_TYPE_MULTI_DRAWNINEGRID:

	case ORDER_TYPE_LINETO:
		p = &LineTo{}

	case ORDER_TYPE_OPAQUERECT:
		p = &OpaqueRect{}

	case ORDER_TYPE_SAVEBITMAP:
		p = &SaveBitmap{}

	case ORDER_TYPE_MEMBLT:
		p = &Memblt{}

	case ORDER_TYPE_MEM3BLT:
		p = &Mem3blt{}

	//case ORDER_TYPE_MULTIDSTBLT:

	//case ORDER_TYPE_MULTIPATBLT:

	//case ORDER_TYPE_MULTISCRBLT:

	//case ORDER_TYPE_MULTIOPAQUERECT:

	//case ORDER_TYPE_FAST_INDEX:

	case ORDER_TYPE_POLYGON_SC:
		p = &PolygonSc{}

	case ORDER_TYPE_POLYGON_CB:
		p = &PolygonCb{}

	case ORDER_TYPE_POLYLINE:
		p = &Polyline{}

	//case ORDER_TYPE_FAST_GLYPH:

	case ORDER_TYPE_ELLIPSE_SC:
		p = &EllipeSc{}

	case ORDER_TYPE_ELLIPSE_CB:
		p = &EllipeCb{}

	case ORDER_TYPE_TEXT2:
		p = &GlyphIndex{}
	default:
		p = NewMultiOrder(orderType)
		if p == nil {
			p = NewMissingOrder(orderType)
		}
		if p == nil {
			glog.Error("Not Support order type:", orderType)
			return errors.New("Not Support order type")
		}
	}
	if p != nil {
		// With delta coordinates the fields an order leaves out repeat the
		// values from the previous order of the same type. Parsing into a zero
		// valued struct loses them, and the symptom is not a parse error: a
		// MEMBLT that omits its cache id reads as cache 0 and finds nothing.
		// The fields an order leaves out repeat the previous order of the same
		// type. This is not tied to TS_DELTA_COORDINATES, which only says that
		// the coordinates that are present are relative to the last one:
		// presence is what the field flags are for. Gating the carry over on the
		// delta flag meant that an order which only moved kept nothing, and drew
		// nothing, because its cache and its size came back as zero.
		if prev, ok := st.delta[p.Type()]; ok {
			copyPrimaryOrder(p, prev)
		}
		if err := p.Unpack(r, present, delta); err != nil {
			return err
		}
		st.delta[p.Type()] = p
	}

	o.Primary.Data = p
	return nil
}
func readOrderCoord(r io.Reader, coord *int32, delta bool) {
	if delta {
		change, _ := core.ReadUInt8(r)
		*coord += int32(int8(change))
	} else {
		change, _ := core.ReadUint16LE(r)
		*coord = int32(int16(change))
	}
}

type Dstblt struct {
	X      int32
	Y      int32
	Cx     int32
	Cy     int32
	Opcode uint8
}

func (d *Dstblt) Type() int {
	return ORDER_TYPE_DSTBLT
}
func (d *Dstblt) Unpack(r io.Reader, present uint32, delta bool) error {
	glog.Infof("Dstblt Order")
	if present&0x01 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x02 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x04 != 0 {
		readOrderCoord(r, &d.Cx, delta)
	}
	if present&0x08 != 0 {
		readOrderCoord(r, &d.Cy, delta)
	}
	if present&0x10 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	return nil
}

type Patblt struct {
	X        int32
	Y        int32
	Cx       int32
	Cy       int32
	Opcode   uint8
	BgColour [4]uint8
	FgColour [4]uint8
	Brush    Brush
}

func (d *Patblt) Type() int {
	return ORDER_TYPE_PATBLT
}
func (d *Patblt) Unpack(r io.Reader, present uint32, delta bool) error {
	glog.Infof("Patblt Order")
	if present&0x01 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x02 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x04 != 0 {
		readOrderCoord(r, &d.Cx, delta)
	}
	if present&0x08 != 0 {
		readOrderCoord(r, &d.Cy, delta)
	}
	if present&0x10 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0020 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.BgColour[0], d.BgColour[1], d.BgColour[2], d.BgColour[3] = b, g, r, a
	}
	if present&0x0040 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.FgColour[0], d.FgColour[1], d.FgColour[2], d.FgColour[3] = b, g, r, a
	}
	d.Brush.updateBrush(r, present>>7)

	return nil
}

type Brush struct {
	X     uint8
	Y     uint8
	Style uint8
	Hatch uint8
	Data  []byte
}

func (b *Brush) updateBrush(r io.Reader, present uint32) {
	if present&1 != 0 {
		b.X, _ = core.ReadUInt8(r)
	}

	if present&2 != 0 {
		b.Y, _ = core.ReadUInt8(r)
	}

	if present&4 != 0 {
		b.Style, _ = core.ReadUInt8(r)
	}

	if present&8 != 0 {
		b.Hatch, _ = core.ReadUInt8(r)
	}

	if present&16 != 0 {
		data, _ := core.ReadBytes(7, r)
		b.Data = make([]byte, 0, 8)
		b.Data = append(b.Data, b.Hatch)
		b.Data = append(b.Data, data...)
	}
}

type Scrblt struct {
	X      int32
	Y      int32
	Cx     int32
	Cy     int32
	Opcode uint8
	Srcx   int32
	Srcy   int32
}

func (d *Scrblt) Type() int {
	return ORDER_TYPE_SCRBLT
}

// Unpack reads the order into its receiver. It used to read into a package level
// variable and then copy that over the receiver, which meant two connections in
// one process shared the last SCRBLT either of them saw, and overrode the per
// connection carry over of the fields an order leaves out.
func (d *Scrblt) Unpack(r io.Reader, present uint32, delta bool) error {
	glog.Infof("Scrblt Order")
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Cx, delta)
	}
	if present&0x0008 != 0 {
		readOrderCoord(r, &d.Cy, delta)
	}
	if present&0x0010 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0020 != 0 {
		readOrderCoord(r, &d.Srcx, delta)
	}
	if present&0x0040 != 0 {
		readOrderCoord(r, &d.Srcy, delta)
	}
	return nil
}

type LineTo struct {
	Mixmode  uint16
	Startx   int32
	Starty   int32
	Endx     int32
	Endy     int32
	Bgcolour [4]uint8
	Opcode   uint8
	Pen      Pen
}

func (d *LineTo) Type() int {
	return ORDER_TYPE_LINETO
}
func (d *LineTo) Unpack(r io.Reader, present uint32, delta bool) error {
	glog.Infof("LineTo Order")
	if present&0x0001 != 0 {
		d.Mixmode, _ = core.ReadUint16LE(r)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Startx, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Starty, delta)
	}
	if present&0x008 != 0 {
		readOrderCoord(r, &d.Endx, delta)
	}
	if present&0x0010 != 0 {
		readOrderCoord(r, &d.Endy, delta)
	}
	if present&0x0020 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.Bgcolour[0], d.Bgcolour[1], d.Bgcolour[2], d.Bgcolour[3] = b, g, r, a
	}
	if present&0x0040 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}

	d.Pen.updatePen(r, present>>7)

	return nil
}

type Pen struct {
	Style  uint8
	Width  uint8
	Colour [4]uint8
}

func (d *Pen) updatePen(r io.Reader, present uint32) {
	if present&1 != 0 {
		d.Style, _ = core.ReadUInt8(r)
	}

	if present&2 != 0 {
		d.Width, _ = core.ReadUInt8(r)
	}

	if present&4 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.Colour[0], d.Colour[1], d.Colour[2], d.Colour[3] = b, g, r, a
	}
}

type OpaqueRect struct {
	X      int32
	Y      int32
	Cx     int32
	Cy     int32
	Colour [4]uint8
}

func (d *OpaqueRect) Type() int {
	return ORDER_TYPE_OPAQUERECT
}
func (d *OpaqueRect) Unpack(r io.Reader, present uint32, delta bool) error {
	glog.Infof("OpaqueRect Order")
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Cx, delta)
	}
	if present&0x0008 != 0 {
		readOrderCoord(r, &d.Cy, delta)
	}
	if present&0x0010 != 0 {
		i, _ := core.ReadUInt8(r)
		d.Colour[0] = i
	}
	if present&0x0020 != 0 {
		i, _ := core.ReadUInt8(r)
		d.Colour[1] = i
	}
	if present&0x0040 != 0 {
		i, _ := core.ReadUInt8(r)
		d.Colour[2] = i
	}
	return nil
}

type SaveBitmap struct {
	Offset uint32
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
	action uint8
}

func (d *SaveBitmap) Type() int {
	return ORDER_TYPE_SAVEBITMAP
}
func (d *SaveBitmap) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		d.Offset, _ = core.ReadUInt32LE(r)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Left, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Top, delta)
	}
	if present&0x0008 != 0 {
		readOrderCoord(r, &d.Right, delta)
	}
	if present&0x0010 != 0 {
		readOrderCoord(r, &d.Bottom, delta)
	}
	if present&0x0020 != 0 {
		d.action, _ = core.ReadUInt8(r)
	}
	return nil
}

type Memblt struct {
	CacheId     uint8
	ColourTable uint8
	X           int32
	Y           int32
	Cx          int32
	Cy          int32
	// ColourIndex is the byte the order carries in its colorIndex field. It is
	// not a raster operation: MEMBLT always copies, and the field is only used
	// when the cached bitmap is palettised.
	ColourIndex uint8
	Srcx        int32
	Srcy        int32
	CacheIdx    uint16
}

func (d *Memblt) Type() int {
	return ORDER_TYPE_MEMBLT
}
func (d *Memblt) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		d.CacheId, _ = core.ReadUInt8(r)
		d.ColourTable, _ = core.ReadUInt8(r)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x0008 != 0 {
		readOrderCoord(r, &d.Cx, delta)
	}
	if present&0x0010 != 0 {
		readOrderCoord(r, &d.Cy, delta)
	}
	if present&0x0020 != 0 {
		d.ColourIndex, _ = core.ReadUInt8(r)
	}
	if present&0x0040 != 0 {
		readOrderCoord(r, &d.Srcx, delta)
	}
	if present&0x0080 != 0 {
		readOrderCoord(r, &d.Srcy, delta)
	}
	if present&0x0100 != 0 {
		d.CacheIdx, _ = core.ReadUint16LE(r)
	}
	return nil
}

type Mem3blt struct {
	ColourTable uint8
	CacheId     uint8
	X           int32
	Y           int32
	Cx          int32
	Cy          int32
	Opcode      uint8
	Srcx        int32
	Srcy        int32
	Bgcolour    [4]uint8
	Fgcolour    [4]uint8
	Brush       Brush
	CacheIdx    uint16
}

func (d *Mem3blt) Type() int {
	return ORDER_TYPE_MEM3BLT
}
func (d *Mem3blt) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x000001 != 0 {
		d.CacheId, _ = core.ReadUInt8(r)
		d.ColourTable, _ = core.ReadUInt8(r)
	}
	if present&0x000002 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x000004 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x000008 != 0 {
		readOrderCoord(r, &d.Cx, delta)
	}
	if present&0x000010 != 0 {
		readOrderCoord(r, &d.Cy, delta)
	}
	if present&0x000020 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x000040 != 0 {
		readOrderCoord(r, &d.Srcx, delta)
	}
	if present&0x000080 != 0 {
		readOrderCoord(r, &d.Srcy, delta)
	}
	if present&0x000100 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.Bgcolour[0], d.Bgcolour[1], d.Bgcolour[2], d.Bgcolour[3] = b, g, r, a
	}
	if present&0x000200 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.Fgcolour[0], d.Fgcolour[1], d.Fgcolour[2], d.Fgcolour[3] = b, g, r, a
	}
	d.Brush.updateBrush(r, present>>10)
	if present&0x008000 != 0 {
		d.CacheIdx, _ = core.ReadUint16LE(r)
	}
	if present&0x010000 != 0 {
		core.ReadUint16LE(r)
	}

	return nil
}

type PolygonSc struct {
	X        int32
	Y        int32
	Opcode   uint8
	Fillmode uint8
	Fgcolour [4]uint8
	Npoints  uint8
	Points   []Point
}

type Point struct {
	X int32
	Y int32
}

func (d *PolygonSc) Type() int {
	return ORDER_TYPE_POLYGON_SC
}
func (d *PolygonSc) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x0004 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0008 != 0 {
		d.Fillmode, _ = core.ReadUInt8(r)
	}
	if present&0x0010 != 0 {
		b, g, r, a := updateReadColorRef(r)
		d.Fgcolour[0], d.Fgcolour[1], d.Fgcolour[2], d.Fgcolour[3] = b, g, r, a
	}
	if present&0x0020 != 0 {
		d.Npoints, _ = core.ReadUInt8(r)
		d.Points = make([]Point, 0, d.Npoints+1)
	}
	if present&0x0040 != 0 {
		// The points that follow are deltas from the order's own position, and
		// the ones that are zero are flagged in a bitmap rather than written out.
		// One byte the order calls cbData comes first.
		if _, err := core.ReadUInt8(r); err != nil {
			return fmt.Errorf("polygon sc: %w", err)
		}
		d.Points = readDeltaPoints(r, int(d.Npoints), Point{d.X, d.Y})
	}

	return nil
}

// readDeltaPoints reads the packed point list a polygon or a polyline carries.
//
// Each point is a pair of deltas from the one before, starting at start, and a
// bitmap of two bits per point says which of them are zero and therefore omitted.
// The bitmap comes first and is re-read every four points, which is what the
// shift below accounts for.
func readDeltaPoints(r io.Reader, count int, start Point) []Point {
	points := make([]Point, 0, count+1)
	points = append(points, start)
	if count <= 0 {
		return points
	}

	zeroBits, err := core.ReadBytes((count+3)/4, r)
	if err != nil || len(zeroBits) != (count+3)/4 {
		return points
	}
	zr := bytes.NewReader(zeroBits)
	cur := start
	var flags uint8
	for i := 0; i < count; i++ {
		if i%4 == 0 {
			flags, _ = core.ReadUInt8(zr)
		}
		if flags&0x80 == 0 {
			cur.X += parseDelta(r)
		}
		if flags&0x40 == 0 {
			cur.Y += parseDelta(r)
		}
		flags <<= 2
		points = append(points, cur)
	}
	return points
}

func parseDelta(r io.Reader) (v int32) {
	b, _ := core.ReadUInt8(r)
	if b&0x40 != 0 {
		v = int32(b) | (^0x3F)
	} else {
		v = int32(b & 0x3F)
	}
	if b&0x80 != 0 {
		b, _ := core.ReadUInt8(r)
		v = (v << 8) | int32(b)
	}
	return
}

// PolygonCb is a polygon filled with a colour brush.
type PolygonCb struct {
	X, Y     int32
	Opcode   uint8
	Fillmode uint8
	BgColour [4]uint8
	FgColour [4]uint8
	Brush    Brush
	Npoints  uint8
	Points   []Point
}

func (d *PolygonCb) Type() int {
	return ORDER_TYPE_POLYGON_CB
}

// Unpack reads the order as MS-RDPEGDI 2.2.2.2.1.1.2.17 lays it out, which is
// taken from FreeRDP's parser rather than from the wording of the specification.
// The point count sits in a later field than the rest, hence the twelfth.
func (d *PolygonCb) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x0004 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0008 != 0 {
		d.Fillmode, _ = core.ReadUInt8(r)
	}
	if present&0x0010 != 0 {
		b, g, rr, a := updateReadColorRef(r)
		d.BgColour[0], d.BgColour[1], d.BgColour[2], d.BgColour[3] = b, g, rr, a
	}
	if present&0x0020 != 0 {
		b, g, rr, a := updateReadColorRef(r)
		d.FgColour[0], d.FgColour[1], d.FgColour[2], d.FgColour[3] = b, g, rr, a
	}
	// The brush travels in the middle of the field flags, as it does for PATBLT.
	// Not reading it left the point count and the list shifted by up to five
	// bytes for any order that carried one.
	d.Brush.updateBrush(r, present>>6)
	if present&0x0800 != 0 {
		d.Npoints, _ = core.ReadUInt8(r)
	}
	if present&0x1000 != 0 {
		if _, err := core.ReadUInt8(r); err != nil {
			return fmt.Errorf("polygon cb: %w", err)
		}
		d.Points = readDeltaPoints(r, int(d.Npoints), Point{d.X, d.Y})
	}
	return nil
}

// Polyline is a sequence of line segments.
type Polyline struct {
	X, Y   int32
	Opcode uint8
	// Word is a reserved field the order still carries.
	Word    uint16
	Colour  [4]uint8
	Npoints uint8
	Points  []Point
}

func (d *Polyline) Type() int {
	return ORDER_TYPE_POLYLINE
}

// Unpack reads the order as MS-RDPEGDI 2.2.2.2.1.1.2.16 lays it out.
func (d *Polyline) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.X, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Y, delta)
	}
	if present&0x0004 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0008 != 0 {
		d.Word, _ = core.ReadUint16LE(r)
	}
	if present&0x0010 != 0 {
		b, g, rr, a := updateReadColorRef(r)
		d.Colour[0], d.Colour[1], d.Colour[2], d.Colour[3] = b, g, rr, a
	}
	if present&0x0020 != 0 {
		d.Npoints, _ = core.ReadUInt8(r)
	}
	if present&0x0040 != 0 {
		if _, err := core.ReadUInt8(r); err != nil {
			return fmt.Errorf("polyline: %w", err)
		}
		d.Points = readDeltaPoints(r, int(d.Npoints), Point{d.X, d.Y})
	}
	return nil
}

// EllipeSc is an ellipse drawn in an outline.
type EllipeSc struct {
	Left, Top, Right, Bottom int32
	Opcode                   uint8
	Fillmode                 uint8
	Colour                   [4]uint8
}

func (d *EllipeSc) Type() int {
	return ORDER_TYPE_ELLIPSE_SC
}

// Unpack reads the order as MS-RDPEGDI 2.2.2.2.1.1.2.20 lays it out.
func (d *EllipeSc) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.Left, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Top, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Right, delta)
	}
	if present&0x0008 != 0 {
		readOrderCoord(r, &d.Bottom, delta)
	}
	if present&0x0010 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0020 != 0 {
		d.Fillmode, _ = core.ReadUInt8(r)
	}
	if present&0x0040 != 0 {
		b, g, rr, a := updateReadColorRef(r)
		d.Colour[0], d.Colour[1], d.Colour[2], d.Colour[3] = b, g, rr, a
	}
	return nil
}

// EllipeCb is an ellipse filled with a colour brush.
type EllipeCb struct {
	Left, Top, Right, Bottom int32
	Opcode                   uint8
	Fillmode                 uint8
	BgColour                 [4]uint8
	FgColour                 [4]uint8
	Brush                    Brush
}

func (d *EllipeCb) Type() int {
	return ORDER_TYPE_ELLIPSE_CB
}

// Unpack reads the order as MS-RDPEGDI 2.2.2.2.1.1.2.21 lays it out.
func (d *EllipeCb) Unpack(r io.Reader, present uint32, delta bool) error {
	if present&0x0001 != 0 {
		readOrderCoord(r, &d.Left, delta)
	}
	if present&0x0002 != 0 {
		readOrderCoord(r, &d.Top, delta)
	}
	if present&0x0004 != 0 {
		readOrderCoord(r, &d.Right, delta)
	}
	if present&0x0008 != 0 {
		readOrderCoord(r, &d.Bottom, delta)
	}
	if present&0x0010 != 0 {
		d.Opcode, _ = core.ReadUInt8(r)
	}
	if present&0x0020 != 0 {
		d.Fillmode, _ = core.ReadUInt8(r)
	}
	if present&0x0040 != 0 {
		b, g, rr, a := updateReadColorRef(r)
		d.BgColour[0], d.BgColour[1], d.BgColour[2], d.BgColour[3] = b, g, rr, a
	}
	if present&0x0080 != 0 {
		b, g, rr, a := updateReadColorRef(r)
		d.FgColour[0], d.FgColour[1], d.FgColour[2], d.FgColour[3] = b, g, rr, a
	}
	// The brush travels in the other half of the field flags, as it does for
	// PATBLT. Not reading it would leave the rest of the batch shifted.
	d.Brush.updateBrush(r, present>>8)
	return nil
}

/*Secondary*/
func (s *Secondary) updateCacheBitmapOrder(r io.Reader, compressed bool, flags uint16) error {
	var cb CacheBitmapOrder
	cb.cacheId, _ = core.ReadUInt8(r)
	core.ReadUInt8(r)
	cb.bitmapWidth, _ = core.ReadUInt8(r)
	cb.bitmapHeight, _ = core.ReadUInt8(r)
	cb.bitmapBpp, _ = core.ReadUInt8(r)
	bitmapLength, _ := core.ReadUint16LE(r)
	cb.cacheIndex, _ = core.ReadUint16LE(r)
	var bitmapComprHdr []byte
	if compressed {
		if (flags & NO_BITMAP_COMPRESSION_HDR) == 0 {
			// The eight header bytes are part of the length the order
			// declares, so a smaller length describes no bitmap at all.
			// Subtracting from a uint16 used to wrap it around and ask for
			// nearly 64 KiB, so refuse the order instead of reading on.
			if bitmapLength < 8 {
				return fmt.Errorf("declared length %d is smaller than the 8 byte compression header", bitmapLength)
			}
			bitmapComprHdr, _ = core.ReadBytes(8, r)
			bitmapLength -= 8
		}
	}
	cb.bitmapComprHdr = bitmapComprHdr
	data, err := readChecked(r, int(bitmapLength), maxCacheBitmapBytes)
	if err != nil {
		return err
	}
	cb.bitmapDataStream = data
	cb.bitmapLength = bitmapLength

	s.CacheBitmap = &CacheBitmap{
		CacheID:    uint32(cb.cacheId),
		CacheIndex: uint32(cb.cacheIndex),
		Width:      int(cb.bitmapWidth),
		Height:     int(cb.bitmapHeight),
		Bpp:        int(cb.bitmapBpp),
		Pixels: cachePixels(cb.bitmapDataStream, int(cb.bitmapWidth), int(cb.bitmapHeight),
			int(cb.bitmapBpp), compressed),
	}
	return nil
}

type CacheBitmapOrder struct {
	cacheId          uint8
	bitmapBpp        uint8
	bitmapWidth      uint8
	bitmapHeight     uint8
	bitmapLength     uint16
	cacheIndex       uint16
	bitmapComprHdr   []byte
	bitmapDataStream []byte
}

// readVarUint16 reads MS-RDPEGDI's compact two byte unsigned value: one byte
// when it fits in seven bits, two when the first byte's top bit says otherwise.
// These fields are not fixed width, which is easy to get wrong and shifts
// everything that follows, including the bitmap itself.
func readVarUint16(r io.Reader) uint32 {
	b, _ := core.ReadUInt8(r)
	if b&0x80 == 0 {
		return uint32(b & 0x7f)
	}
	lo, _ := core.ReadUInt8(r)
	return uint32(b&0x7f)<<8 | uint32(lo)
}

// readVarUint32 is the four byte form: the top two bits of the first byte say
// how many more follow.
func readVarUint32(r io.Reader) uint32 {
	b, _ := core.ReadUInt8(r)
	count := (b & 0xc0) >> 6
	v := uint32(b & 0x3f)
	for i := byte(0); i < count; i++ {
		next, _ := core.ReadUInt8(r)
		v = v<<8 | uint32(next)
	}
	return v
}

func getCbV2Bpp(bpp uint32) (b uint32) {
	switch bpp {
	case 3:
		b = 8
	case 4:
		b = 16
	case 5:
		b = 24
	case 6:
		b = 32
	default:
		b = 0
	}
	return
}

type CacheBitmapV2Order struct {
	cacheId            uint32
	flags              uint32
	key1               uint32
	key2               uint32
	bitmapBpp          uint32
	bitmapWidth        uint32
	bitmapHeight       uint32
	bitmapLength       uint32
	cacheIndex         uint32
	compressed         bool
	cbCompFirstRowSize uint16
	cbCompMainBodySize uint16
	cbScanWidth        uint16
	cbUncompressedSize uint16
	bitmapDataStream   []byte
}

func (s *Secondary) updateCacheBitmapV2Order(r io.Reader, compressed bool, flags uint16) {
	var cb CacheBitmapV2Order
	cb.cacheId = uint32(flags) & 0x0003
	cb.flags = (uint32(flags) & 0xFF80) >> 7
	bitsPerPixelId := (uint32(flags) & 0x0078) >> 3
	cb.bitmapBpp = getCbV2Bpp(bitsPerPixelId)

	if cb.flags&CBR2_PERSISTENT_KEY_PRESENT != 0 {
		cb.key1, _ = core.ReadUInt32LE(r)
		cb.key2, _ = core.ReadUInt32LE(r)
	}

	if cb.flags&CBR2_HEIGHT_SAME_AS_WIDTH != 0 {
		cb.bitmapWidth = readVarUint16(r)
		cb.bitmapHeight = cb.bitmapWidth
	} else {
		cb.bitmapWidth = readVarUint16(r)
		cb.bitmapHeight = readVarUint16(r)
	}

	bitmapLength := readVarUint32(r)
	cacheIndex := readVarUint16(r)

	if cb.flags&CBR2_DO_NOT_CACHE != 0 {
		cb.cacheIndex = 0x7FFF
	} else {
		cb.cacheIndex = cacheIndex
	}

	if compressed {
		if cb.flags&CBR2_NO_BITMAP_COMPRESSION_HDR == 0 {
			cb.cbCompFirstRowSize, _ = core.ReadUint16LE(r)
			cb.cbCompMainBodySize, _ = core.ReadUint16LE(r)
			cb.cbScanWidth, _ = core.ReadUint16LE(r)
			cb.cbUncompressedSize, _ = core.ReadUint16LE(r)
			bitmapLength = uint32(cb.cbCompMainBodySize)
		}
	}

	data, err := readChecked(r, int(bitmapLength), maxCacheBitmapBytes)
	if err != nil {
		glog.Debugf("cache bitmap: %v", err)
		return
	}
	cb.bitmapDataStream = data
	cb.bitmapLength = bitmapLength
	cb.compressed = compressed

	s.CacheBitmap = &CacheBitmap{
		CacheID:    cb.cacheId,
		CacheIndex: cb.cacheIndex,
		Width:      int(cb.bitmapWidth),
		Height:     int(cb.bitmapHeight),
		Bpp:        int(cb.bitmapBpp),
		NotCached:  cb.flags&CBR2_DO_NOT_CACHE != 0,
		Pixels: cachePixels(cb.bitmapDataStream, int(cb.bitmapWidth), int(cb.bitmapHeight),
			int(cb.bitmapBpp), compressed),
	}
}

type CacheBitmapV3Order struct {
	cacheId    uint32
	bpp        uint32
	flags      uint32
	cacheIndex uint16
	key1       uint32
	key2       uint32
	bitmapData CacheBitmapDataEx
}

// CacheBitmapDataEx is the embedded BITMAP_DATA_EX structure of a Cache Bitmap
// Revision 3 order (MS-RDPEGDI 2.2.2.2.1.2.8). It is close to, but not the
// same as, TS_BITMAP_DATA_EX from a surface bits command: the second byte is
// reserved here rather than a flags byte.
type CacheBitmapDataEx struct {
	Bpp     uint8
	CodecID uint8
	Width   uint16
	Height  uint16
	Length  uint32
	Data    []byte
}

func (s *Secondary) updateCacheBitmapV3Order(r io.Reader, flags uint16) {
	var cb CacheBitmapV3Order

	cb.cacheId = uint32(flags) & 0x00000003
	cb.flags = (uint32(flags) & 0x0000FF80) >> 7
	bitsPerPixelId := (uint32(flags) & 0x00000078) >> 3
	cb.bpp = getCbV2Bpp(bitsPerPixelId)

	cacheIndex, _ := core.ReadUint16LE(r)
	cb.cacheIndex = cacheIndex
	cb.key1, _ = core.ReadUInt32LE(r)
	cb.key2, _ = core.ReadUInt32LE(r)

	bitmapData := &cb.bitmapData
	bitmapData.Bpp, _ = core.ReadUInt8(r)
	core.ReadUInt8(r) // reserved
	core.ReadUInt8(r) // reserved
	bitmapData.CodecID, _ = core.ReadUInt8(r)
	bitmapData.Width, _ = core.ReadUint16LE(r)
	bitmapData.Height, _ = core.ReadUint16LE(r)
	new_len, _ := core.ReadUInt32LE(r)

	data, err := readChecked(r, int(new_len), maxCacheBitmapBytes)
	if err != nil {
		glog.Debugf("cache bitmap v3: %v", err)
		return
	}
	bitmapData.Data = data
	bitmapData.Length = new_len

	// Revision 3 entries carry a codec id: the data is still encoded, and the
	// codec that produced it has to decode it. The pixels are left empty so the
	// caller can tell the difference.
	s.CacheBitmap = &CacheBitmap{
		CacheID:    cb.cacheId,
		CacheIndex: uint32(cb.cacheIndex),
		Width:      int(bitmapData.Width),
		Height:     int(bitmapData.Height),
		Bpp:        int(bitmapData.Bpp),
		CodecID:    bitmapData.CodecID,
		Data:       bitmapData.Data,
	}
}

type CacheColorTableOrder struct {
	cacheIndex   uint8
	numberColors uint16
	colorTable   [256 * 4]uint8
}

func (s *Secondary) updateCacheColorTableOrder(r io.Reader, flags uint16) {
	var cb CacheColorTableOrder
	cb.cacheIndex, _ = core.ReadUInt8(r)
	cb.numberColors, _ = core.ReadUint16LE(r)

	if cb.numberColors != 256 {
		/* This field MUST be set to 256 */
		return
	}

	// One colour per iteration: stepping four at a time and writing four bytes
	// from each offset ran past the end of the table on the last one.
	for i := 0; i < int(cb.numberColors) && i < 256; i++ {
		// A colour table entry is a four byte quad, not one of the three byte
		// colours an order carries, so it does not go through
		// updateReadColorRef. Sharing that reader misaligned every entry after
		// the first once it was corrected to three bytes.
		b, g, rr, a := updateReadColorRef(r)
		cb.colorTable[i*4], cb.colorTable[i*4+1], cb.colorTable[i*4+2], cb.colorTable[i*4+3] = b, g, rr, a
	}
}

// updateReadColorRef reads a colour an order carries, which is three bytes: blue,
// green and red, in that order.
//
// It used to read four, a fourth byte being discarded, which shifted every field
// after a colour by one byte. Nothing caught it because no server tested sends an
// order with a colour in it, and the drawing tests build their orders directly
// rather than parsing them. FreeRDP's update_read_color is the authority.
// Bounds on the lengths a secondary order declares. They come off the wire, and
// core.ReadBytes allocates the declared size before reading a byte of it, so an
// unchecked length costs that much memory whether or not the data exists.
const (
	maxCacheBitmapBytes = 16 << 20
	maxBrushBytes       = 4 << 10
	// maxCachePixels is the most a cached bitmap may decode to, which bounds the
	// buffer the decoder allocates from geometry that also came off the wire.
	maxCachePixels = 16 << 20
)

// readChecked reads exactly n bytes, refusing a length above limit before it
// allocates anything, and refusing a short read.
func readChecked(r io.Reader, n, limit int) ([]byte, error) {
	if n < 0 || n > limit {
		return nil, fmt.Errorf("length %d is out of range, at most %d", n, limit)
	}
	b, err := core.ReadBytes(n, r)
	if err != nil {
		return nil, err
	}
	if len(b) != n {
		return nil, fmt.Errorf("wanted %d bytes, got %d", n, len(b))
	}
	return b, nil
}

func updateReadColorRef(r io.Reader) (uint8, uint8, uint8, uint8) {
	blue, _ := core.ReadUInt8(r)
	green, _ := core.ReadUInt8(r)
	red, _ := core.ReadUInt8(r)
	return blue, green, red, 255
}

type CacheGlyphOrder struct {
	cacheId uint8
	nglyphs uint8
	glyphs  []CacheGlyph
}
type CacheGlyph struct {
	character uint16
	offset    uint16
	baseline  uint16
	width     uint16
	height    uint16
	datasize  int
	data      []uint8
}

type CacheBrushOrder struct {
	index  uint8
	bpp    uint8
	cx     uint8
	cy     uint8
	style  uint8
	length uint8
	data   []uint8
}

func (s *Secondary) updateCacheBrushOrder(r io.Reader, flags uint16) {
	var cb CacheBrushOrder
	cb.index, _ = core.ReadUInt8(r)
	cb.bpp, _ = core.ReadUInt8(r)
	cb.cx, _ = core.ReadUInt8(r)
	cb.cy, _ = core.ReadUInt8(r)
	cb.style, _ = core.ReadUInt8(r)
	cb.length, _ = core.ReadUInt8(r)
	if cb.cx == 8 && cb.cy == 8 {
		if cb.bpp == 1 {
			if cb.length != 8 {
				glog.Debugf("cache brush: monochrome brush of %d bytes", cb.length)
				return
			}
			cb.data = make([]uint8, 8)
			for i := 7; i >= 0; i-- {
				cb.data[i], _ = core.ReadUInt8(r)
			}
		} else {
			bpp := int(cb.bpp) - 2
			if int(cb.length) == 16+4*bpp {
				// compressed brush
				data, err := readChecked(r, int(cb.length), maxBrushBytes)
				if err != nil {
					glog.Debugf("cache brush: %v", err)
					return
				}
				cb.data = update_decompress_brush(data, bpp)
			} else {
				// uncompressed brush
				scanline := 8 * 8 * bpp
				data, err := readChecked(r, scanline, maxBrushBytes)
				if err != nil {
					glog.Debugf("cache brush: %v", err)
					return
				}
				cb.data = data
			}
		}
		s.CacheBrush = &CacheBrush{Index: cb.index, Bpp: cb.bpp, Width: int(cb.cx),
			Height: int(cb.cy), Style: cb.style, Hatch: cb.index, Data: cb.data}
	}
}
func update_decompress_brush(in []uint8, bpp int) []uint8 {
	// The palette follows a sixteen byte run map, and the body indexes it by
	// two bit values, so both have to be there before anything is read.
	if bpp < 1 || len(in) < 16+4*bpp {
		return nil
	}

	var pal_index, in_index, shift int

	pal := in[16:]
	out := make([]uint8, 8*8*bpp)
	/* read it bottom up */
	for y := 7; y >= 0; y-- {
		/* 2 bytes per row */
		x := 0
		for do2 := 0; do2 < 2; do2++ {
			/* 4 pixels per byte */
			shift = 6
			for shift >= 0 {
				pal_index = int((in[in_index] >> shift) & 3)
				/* size of palette entries depends on bpp */
				for i := 0; i < bpp; i++ {
					out[(y*8+x)*bpp+i] = pal[pal_index*bpp+i]
				}
				x++
				shift -= 2
			}
			in_index++
		}
	}

	return out
}

/*Primary*/
// Bounds is the clip rectangle an order may carry: the order must only affect
// the area inside it. Rendered orders have to honour it, or a blit that the
// server meant to clip paints over the rest of the screen.
type Bounds struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// Rect returns the bounds as left, top, right, bottom.
func (b Bounds) Rect() (int, int, int, int) {
	return int(b.Left), int(b.Top), int(b.Right), int(b.Bottom)
}

type OrderInfo struct {
	controlFlags     uint32
	orderType        uint32
	fieldFlags       uint32
	boundsFlags      uint32
	bounds           Bounds
	deltaCoordinates bool
}
