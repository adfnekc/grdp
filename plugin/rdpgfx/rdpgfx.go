// Package rdpgfx implements the RDP Graphics Pipeline Extension channel
// (MS-RDPEGFX), the modern drawing path where a server composites surfaces and
// sends them as compressed bitmaps.
//
// It is a dynamic virtual channel, so it only starts once the drdynvc layer
// has created it and both sides have exchanged capabilities.
package rdpgfx

import (
	"encoding/binary"
	"fmt"
	"image"
	"sync"

	"github.com/adfnekc/grdp/codec"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
)

// RDPGFX_DVC_CHANNEL_NAME is the dynamic virtual channel name.
const DVCChannelName = "Microsoft::Windows::RDS::Graphics"

// Command ids (MS-RDPEGFX 2.2.3).
const (
	cmdWireToSurface1           = 0x0001
	cmdWireToSurface2           = 0x0002
	cmdDeleteEncodingCtx        = 0x0003
	cmdSolidFill                = 0x0004
	cmdSurfaceToSurface         = 0x0005
	cmdSurfaceToCache           = 0x0006
	cmdCacheToSurface           = 0x0007
	cmdEvictCacheEntry          = 0x0008
	cmdCreateSurface            = 0x0009
	cmdDeleteSurface            = 0x000A
	cmdStartFrame               = 0x000B
	cmdEndFrame                 = 0x000C
	cmdFrameAcknowledge         = 0x000D
	cmdResetGraphics            = 0x000E
	cmdMapSurfaceToOutput       = 0x000F
	cmdCacheImportOffer         = 0x0010
	cmdCacheImportReply         = 0x0011
	cmdCapsAdvertise            = 0x0012
	cmdCapsConfirm              = 0x0013
	cmdMapSurfaceToWindow       = 0x0015
	cmdMapSurfaceToScaledOutput = 0x0017
	cmdMapSurfaceToScaledWindow = 0x0018
)

// Codec ids carried by WireToSurface (MS-RDPEGFX 2.2.3.9).
const (
	codecUncompressed  = 0x0000
	codecCAVideo       = 0x0003 // RemoteFX, decoded by the codec package
	codecClearCodec    = 0x0008
	codecPlanar        = 0x000A
	codecAVC420        = 0x000B
	codecAlpha         = 0x000C
	codecAVC444        = 0x000E
	codecAVC444v2      = 0x000F
	codecProgressive   = 0x0009
	codecProgressiveV2 = 0x000D
)

// Pixel formats (MS-RDPEGFX 2.2.3.9).
const (
	pixelFormatXRGB8888 = 0x20
	pixelFormatARGB8888 = 0x21
)

// Capability versions. 8.0 and 8.1 are the ones a decoder of RemoteFX and raw
// bitmaps can honestly claim; the 10.x versions advertise AVC support.
const (
	CapsVersion8  = 0x00080004
	CapsVersion81 = 0x00080105
)

const (
	headerSize            = 8
	capsetBase            = 8
	queueDepthUnavailable = 0x00000000
)

// headerBuf is the common RDPGFX PDU header.
type headerBuf struct {
	cmdID     uint16
	flags     uint16
	pduLength uint32
}

// Frame is one completed frame: the id the server assigned and the surfaces
// that were touched. The pixels live on the surfaces.
type Frame struct {
	ID       uint32
	Surfaces []*Surface
}

// Reset describes a Reset Graphics PDU: the size of the desktop the server
// will composite into. Any previously known surfaces are gone.
type Reset struct {
	Width, Height int
}

// Surface is a decoded surface: a set of pixels that the server composites
// into frames. Surfaces are addressed by a 16 bit id.
type Surface struct {
	ID          uint16
	Width       int
	Height      int
	PixelFormat uint8
	pixels      []byte
	mu          sync.Mutex
}

// Pixels returns a copy of the surface contents as BGRA, top down.
func (s *Surface) Pixels() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.pixels))
	copy(out, s.pixels)
	return out
}

// GfxClient is the RDPGFX channel handler.
type GfxClient struct {
	emission.Emitter

	// send writes a complete PDU on the dynamic virtual channel. It is
	// supplied by the wiring that owns the drdynvc layer.
	send func(channelID uint32, data []byte) error

	mu       sync.Mutex
	channel  uint32
	width    int
	height   int
	surfaces map[uint16]*Surface
	frameID  uint32
}

// NewGfxClient creates an RDPGFX handler.
func NewGfxClient() *GfxClient {
	return &GfxClient{
		Emitter:  *emission.NewEmitter(),
		surfaces: make(map[uint16]*Surface),
	}
}

// SetSender installs the callback used to write to the dynamic virtual
// channel. It must be called before the channel is opened.
func (c *GfxClient) SetSender(f func(channelID uint32, data []byte) error) { c.send = f }

// OnOpen implements drdynvc.DynChannel.
func (c *GfxClient) OnOpen(channelID uint32) {
	c.mu.Lock()
	c.channel = channelID
	c.mu.Unlock()
	glog.Debugf("rdpgfx: channel %d opened, advertising capabilities", channelID)
	// The client speaks first: advertise what it can decode.
	_ = c.sendCapsAdvertise()
	// Notify through the same namespace the client bridges.
	c.Emit("gfx-open")
}

// OnClose implements drdynvc.DynChannel.
func (c *GfxClient) OnClose() {
	c.mu.Lock()
	c.surfaces = make(map[uint16]*Surface)
	c.mu.Unlock()
	c.Emit("close")
}

// OnData implements drdynvc.DynChannel. A payload may hold several PDUs.
func (c *GfxClient) OnData(data []byte) {
	for off := 0; off+headerSize <= len(data); {
		h := headerBuf{
			cmdID:     binary.LittleEndian.Uint16(data[off:]),
			flags:     binary.LittleEndian.Uint16(data[off+2:]),
			pduLength: binary.LittleEndian.Uint32(data[off+4:]),
		}
		if h.pduLength < headerSize || off+int(h.pduLength) > len(data) {
			glog.Debugf("rdpgfx: command 0x%04x has bad length %d", h.cmdID, h.pduLength)
			return
		}
		body := data[off+headerSize : off+int(h.pduLength)]
		if err := c.handle(h, body); err != nil {
			glog.Debugf("rdpgfx: command 0x%04x: %v", h.cmdID, err)
		}
		off += int(h.pduLength)
	}
}

// handle dispatches one PDU body.
func (c *GfxClient) handle(h headerBuf, body []byte) error {
	switch h.cmdID {
	case cmdCapsConfirm:
		return c.processCapsConfirm(body)
	case cmdResetGraphics:
		return c.processResetGraphics(body)
	case cmdCreateSurface:
		return c.processCreateSurface(body)
	case cmdDeleteSurface:
		return c.processDeleteSurface(body)
	case cmdWireToSurface1:
		return c.processWireToSurface1(body)
	case cmdStartFrame:
		return c.processStartFrame(body)
	case cmdEndFrame:
		return c.processEndFrame(body)
	case cmdSolidFill:
		return c.processSolidFill(body)
	case cmdSurfaceToSurface:
		return c.processSurfaceToSurface(body)
	case cmdDeleteEncodingCtx, cmdEvictCacheEntry:
		return nil
	case cmdCacheImportOffer:
		// Caching is optional; answering with an empty reply is valid.
		return c.sendCacheImportReply()
	case cmdMapSurfaceToOutput, cmdMapSurfaceToScaledOutput,
		cmdMapSurfaceToWindow, cmdMapSurfaceToScaledWindow:
		// Placement only matters for a windowed consumer.
		return nil
	default:
		// Unknown commands are skipped: the length field lets us stay in
		// sync, and a newer server may send things we do not model.
		glog.Debugf("rdpgfx: ignoring command 0x%04x", h.cmdID)
		return nil
	}
}

// processCapsConfirm records the version the server chose.
func (c *GfxClient) processCapsConfirm(body []byte) error {
	if len(body) < capsetBase+4 {
		return fmt.Errorf("caps confirm: %w", errShort)
	}
	version := binary.LittleEndian.Uint32(body)
	c.Emit("gfx-caps", version)
	glog.Debugf("rdpgfx: server confirmed caps version 0x%08x", version)
	return nil
}

// processResetGraphics records the desktop size the server will use.
func (c *GfxClient) processResetGraphics(body []byte) error {
	if len(body) < 12 {
		return fmt.Errorf("reset graphics: %w", errShort)
	}
	width := int(binary.LittleEndian.Uint32(body))
	height := int(binary.LittleEndian.Uint32(body[4:]))

	c.mu.Lock()
	c.width, c.height = width, height
	// The server is starting over, so drop any surfaces it did not recreate.
	c.surfaces = make(map[uint16]*Surface)
	c.mu.Unlock()

	c.Emit("gfx-reset", Reset{Width: width, Height: height})
	return nil
}

// processCreateSurface registers a surface.
func (c *GfxClient) processCreateSurface(body []byte) error {
	if len(body) < 7 {
		return fmt.Errorf("create surface: %w", errShort)
	}
	s := &Surface{
		ID:          binary.LittleEndian.Uint16(body),
		Width:       int(binary.LittleEndian.Uint16(body[2:])),
		Height:      int(binary.LittleEndian.Uint16(body[4:])),
		PixelFormat: body[6],
	}
	if s.Width <= 0 || s.Height <= 0 {
		return fmt.Errorf("create surface: bad size %dx%d", s.Width, s.Height)
	}
	s.pixels = make([]byte, s.Width*s.Height*4)

	c.mu.Lock()
	c.surfaces[s.ID] = s
	c.mu.Unlock()

	c.Emit("gfx-surface-created", s.ID)
	return nil
}

// processDeleteSurface removes a surface.
func (c *GfxClient) processDeleteSurface(body []byte) error {
	if len(body) < 2 {
		return fmt.Errorf("delete surface: %w", errShort)
	}
	id := binary.LittleEndian.Uint16(body)

	c.mu.Lock()
	delete(c.surfaces, id)
	c.mu.Unlock()

	c.Emit("gfx-surface-deleted", id)
	return nil
}

// processWireToSurface1 decodes a bitmap into a surface.
func (c *GfxClient) processWireToSurface1(body []byte) error {
	// surfaceId(2) codecId(2) pixelFormat(1) reserved(1) destRect(8, four
	// little endian uint16) bitmapDataLength(4) bitmapData. The fixed part is
	// 18 bytes and RDPGFX_WIRE_TO_SURFACE_PDU_1_SIZE includes the header.
	if len(body) < 18 {
		return fmt.Errorf("wire to surface 1: %w", errShort)
	}
	surfaceID := binary.LittleEndian.Uint16(body)
	codecID := binary.LittleEndian.Uint16(body[2:])
	pixelFormat := body[4]
	left := int(binary.LittleEndian.Uint16(body[6:]))
	top := int(binary.LittleEndian.Uint16(body[8:]))
	right := int(binary.LittleEndian.Uint16(body[10:]))
	bottom := int(binary.LittleEndian.Uint16(body[12:]))
	dataLen := int(binary.LittleEndian.Uint32(body[14:]))
	if dataLen < 0 || 18+dataLen > len(body) {
		return fmt.Errorf("wire to surface 1: declared %d bytes, have %d: %w",
			dataLen, len(body)-18, errShort)
	}
	payload := body[18 : 18+dataLen]

	c.mu.Lock()
	s := c.surfaces[surfaceID]
	c.mu.Unlock()
	if s == nil {
		return fmt.Errorf("wire to surface 1: unknown surface %d", surfaceID)
	}

	width, height := right-left, bottom-top
	if width <= 0 || height <= 0 {
		return fmt.Errorf("wire to surface 1: empty destination %dx%d", width, height)
	}

	pixels, err := decodeGfx(codecID, payload, width, height, pixelFormat)
	if err != nil {
		return err
	}

	s.mu.Lock()
	blit(s.pixels, s.Width, s.Height, left, top, width, height, pixels)
	s.mu.Unlock()

	// The surface contents changed; a frame event will follow.
	_ = image.Rect(left, top, right, bottom)
	return nil
}

// decodeGfx maps an RDPGFX codec id onto the bitmap codec package.
func decodeGfx(codecID uint16, data []byte, width, height int, pixelFormat uint8) ([]byte, error) {
	switch codecID {
	case codecUncompressed:
		// Raw pixels at the surface's own depth.
		depth := 4
		if pixelFormat == 0x20 || pixelFormat == 0x21 {
			depth = 4
		}
		if len(data) < width*height*depth {
			return nil, fmt.Errorf("uncompressed surface data: %w", errShort)
		}
		return data[:width*height*depth], nil
	case codecCAVideo:
		// "CAVideo" is the RemoteFX codec under its RDPGFX name.
		return codec.Decompress(codec.CodecIDRemoteFX, data, width, height, 32)
	default:
		return nil, fmt.Errorf("unsupported RDPGFX codec 0x%04x", codecID)
	}
}

// processStartFrame records the frame id.
func (c *GfxClient) processStartFrame(body []byte) error {
	if len(body) < 8 {
		return fmt.Errorf("start frame: %w", errShort)
	}
	c.mu.Lock()
	c.frameID = binary.LittleEndian.Uint32(body[4:])
	frameID := c.frameID
	c.mu.Unlock()

	c.Emit("gfx-frame-start", frameID)
	return nil
}

// processEndFrame acknowledges the frame, which the server requires before it
// will send the next one.
func (c *GfxClient) processEndFrame(body []byte) error {
	if len(body) < 4 {
		return fmt.Errorf("end frame: %w", errShort)
	}
	frameID := binary.LittleEndian.Uint32(body)

	c.mu.Lock()
	surfaces := make([]*Surface, 0, len(c.surfaces))
	for _, s := range c.surfaces {
		surfaces = append(surfaces, s)
	}
	c.mu.Unlock()

	c.Emit("gfx-frame", Frame{ID: frameID, Surfaces: surfaces})

	return c.sendFrameAcknowledge(queueDepthUnavailable, frameID, frameID)
}

// processSolidFill fills rectangles with a single colour.
func (c *GfxClient) processSolidFill(body []byte) error {
	// surfaceId(2) fillPixel(4) fillRectCount(2) rects(8 each)
	if len(body) < 8 {
		return fmt.Errorf("solid fill: %w", errShort)
	}
	surfaceID := binary.LittleEndian.Uint16(body)
	fill := binary.LittleEndian.Uint32(body[2:])
	count := int(binary.LittleEndian.Uint16(body[6:]))
	if 8+count*8 > len(body) {
		return fmt.Errorf("solid fill: %d rects need more data: %w", count, errShort)
	}

	c.mu.Lock()
	s := c.surfaces[surfaceID]
	c.mu.Unlock()
	if s == nil {
		return fmt.Errorf("solid fill: unknown surface %d", surfaceID)
	}

	// RDPGFX_COLOR32 is XRGB, i.e. 0x00RRGGBB.
	b := byte(fill)
	g := byte(fill >> 8)
	r := byte(fill >> 16)

	s.mu.Lock()
	for i := 0; i < count; i++ {
		rc := body[8+i*8:]
		left := int(binary.LittleEndian.Uint16(rc))
		top := int(binary.LittleEndian.Uint16(rc[2:]))
		right := int(binary.LittleEndian.Uint16(rc[4:]))
		bottom := int(binary.LittleEndian.Uint16(rc[6:]))
		for y := top; y < bottom && y < s.Height; y++ {
			if y < 0 {
				continue
			}
			for x := left; x < right && x < s.Width; x++ {
				if x < 0 {
					continue
				}
				i := (y*s.Width + x) * 4
				s.pixels[i+0] = b
				s.pixels[i+1] = g
				s.pixels[i+2] = r
				s.pixels[i+3] = 0xFF
			}
		}
	}
	s.mu.Unlock()

	return nil
}

// processSurfaceToSurface copies a rectangle between surfaces.
func (c *GfxClient) processSurfaceToSurface(body []byte) error {
	// sourceSurfaceId(2) destSurfaceId(2) destPointsCount(2) destPts(4 each)
	// then rectSrc(8)
	if len(body) < 14 {
		return fmt.Errorf("surface to surface: %w", errShort)
	}
	srcID := binary.LittleEndian.Uint16(body)
	dstID := binary.LittleEndian.Uint16(body[2:])
	count := int(binary.LittleEndian.Uint16(body[4:]))
	need := 6 + count*4 + 8
	if need > len(body) {
		return fmt.Errorf("surface to surface: %d points need more data: %w", count, errShort)
	}
	rect := body[6+count*4:]
	left := int(binary.LittleEndian.Uint16(rect))
	top := int(binary.LittleEndian.Uint16(rect[2:]))
	right := int(binary.LittleEndian.Uint16(rect[4:]))
	bottom := int(binary.LittleEndian.Uint16(rect[6:]))

	c.mu.Lock()
	src, dst := c.surfaces[srcID], c.surfaces[dstID]
	c.mu.Unlock()
	if src == nil || dst == nil {
		return fmt.Errorf("surface to surface: unknown surface %d or %d", srcID, dstID)
	}

	width, height := right-left, bottom-top
	if width <= 0 || height <= 0 {
		return nil
	}
	if left < 0 || top < 0 || right > src.Width || bottom > src.Height {
		return fmt.Errorf("surface to surface: source rectangle out of bounds")
	}

	src.mu.Lock()
	buf := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		si := ((top+y)*src.Width + left) * 4
		copy(buf[y*width*4:(y+1)*width*4], src.pixels[si:si+width*4])
	}
	src.mu.Unlock()

	for i := 0; i < count; i++ {
		x := int(binary.LittleEndian.Uint16(body[6+i*4:]))
		y := int(binary.LittleEndian.Uint16(body[6+i*4+2:]))
		dst.mu.Lock()
		blit(dst.pixels, dst.Width, dst.Height, x, y, width, height, buf)
		dst.mu.Unlock()
	}

	return nil
}

// blit copies a width x height BGRA image into dst at (x, y), clipped.
func blit(dst []byte, dstW, dstH, x, y, width, height int, src []byte) {
	for row := 0; row < height; row++ {
		dy := y + row
		if dy < 0 || dy >= dstH {
			continue
		}
		for col := 0; col < width; col++ {
			dx := x + col
			if dx < 0 || dx >= dstW {
				continue
			}
			si := (row*width + col) * 4
			if si+4 > len(src) {
				return
			}
			di := (dy*dstW + dx) * 4
			copy(dst[di:di+4], src[si:si+4])
		}
	}
}

// sendCapsAdvertise tells the server which capability versions we support.
// Only the non AVC versions are offered, because the AVC codecs need an H.264
// decoder that this library does not have.
func (c *GfxClient) sendCapsAdvertise() error {
	versions := []uint32{CapsVersion8, CapsVersion81}
	length := headerSize + 2
	for range versions {
		length += capsetBase + 4
	}

	out := make([]byte, 0, length)
	out = appendHeader(out, cmdCapsAdvertise, 0, uint32(length))
	out = append(out, byte(len(versions)), byte(len(versions)>>8))
	for _, v := range versions {
		out = appendU32(out, v) // version
		out = appendU32(out, 4) // length of the flags that follow
		out = appendU32(out, 0) // flags
	}
	return c.write(out)
}

// sendFrameAcknowledge tells the server a frame was consumed.
func (c *GfxClient) sendFrameAcknowledge(queueDepth, frameID, totalDecoded uint32) error {
	out := make([]byte, 0, headerSize+12)
	out = appendHeader(out, cmdFrameAcknowledge, 0, headerSize+12)
	out = appendU32(out, queueDepth)
	out = appendU32(out, frameID)
	out = appendU32(out, totalDecoded)
	return c.write(out)
}

// sendCacheImportReply declines an import offer, which means "I did not have
// any of these cached".
func (c *GfxClient) sendCacheImportReply() error {
	out := make([]byte, 0, headerSize+2)
	out = appendHeader(out, cmdCacheImportReply, 0, headerSize+2)
	out = append(out, 0, 0) // importedEntriesCount
	return c.write(out)
}

func (c *GfxClient) write(pdu []byte) error {
	if c.send == nil {
		return fmt.Errorf("rdpgfx: no sender")
	}
	c.mu.Lock()
	channel := c.channel
	c.mu.Unlock()
	return c.send(channel, pdu)
}

func appendHeader(b []byte, cmdID, flags uint16, length uint32) []byte {
	b = append(b, byte(cmdID), byte(cmdID>>8), byte(flags), byte(flags>>8))
	return appendU32(b, length)
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// errShort reports a PDU body that ended early.
var errShort = fmt.Errorf("truncated PDU")
