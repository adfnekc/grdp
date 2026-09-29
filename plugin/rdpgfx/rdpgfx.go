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
	"io"
	"os"
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
//
// 0x00080105 rather than 0x00080100: the low byte is part of the version, not
// padding. See MS-RDPEGFX 2.2.3.2.
const (
	CapsVersion8  = 0x00080004
	CapsVersion81 = 0x00080105
)

// Capability flags (MS-RDPEGFX 2.2.3.1).
//
// Only THINCLIENT is set. It asks for the RemoteFX codec rather than the
// RemoteFX Progressive codec, which this client cannot decode, and caps the
// bitmap cache at 16 MB, which costs nothing here because cache import offers
// are declined anyway.
//
// AVC is excluded by omission rather than switched off:
// RDPGFX_CAPS_FLAG_AVC_DISABLED only exists from version 10 onwards, and the
// way to keep H.264 out of a version 8.1 negotiation is to not set
// RDPGFX_CAPS_FLAG_AVC420_ENABLED (0x00000010).
const capsFlagThinClient = 0x00000001

const (
	headerSize            = 8
	capsetBase            = 8
	queueDepthUnavailable = 0x00000000
)

// maxScaledDimension bounds the target width and height a scaled placement may
// carry. They are uint32 on the wire, unlike a surface's own uint16 size, so
// without a bound a server could name a target that resampling turns into a
// multi-gigabyte allocation. 8192 already exceeds an 8K display (7680x4320) and
// keeps the resample buffer under 256 MiB.
const maxScaledDimension = 8192

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

// SurfacePlacement reports where the server mapped a surface. The four mapping
// orders carry different geometry and only some of it can be applied:
//
//   - MapSurfaceToOutput and MapSurfaceToScaledOutput place the surface on the
//     desktop at X,Y. The scaled form additionally asks for it to be resampled
//     to TargetWidth x TargetHeight, which CompositePixels applies.
//   - MapSurfaceToWindow and MapSurfaceToScaledWindow place the surface inside a
//     window. This client has no window model, so the window id and the mapped
//     and target sizes are recorded and left for a consumer that has one;
//     CompositePixels deliberately does not resample them.
type SurfacePlacement struct {
	ID uint16

	// X, Y is the desktop origin, from the two output mappings. A window
	// mapping carries no desktop origin, so it keeps the last one.
	X, Y int

	// Windowed is set for MapSurfaceToWindow and MapSurfaceToScaledWindow.
	Windowed bool
	// WindowID identifies the window a Windowed placement belongs to.
	WindowID uint64
	// MappedWidth, MappedHeight is the surface's size within that window.
	MappedWidth, MappedHeight int

	// TargetWidth, TargetHeight is the size the server asked the surface to be
	// resampled to, from the two scaled mappings. Zero when the mapping does
	// not scale.
	TargetWidth, TargetHeight int
}

// Surface is a decoded surface: a set of pixels that the server composites
// into frames. Surfaces are addressed by a 16 bit id.
type Surface struct {
	ID          uint16
	Width       int
	Height      int
	PixelFormat uint8

	// placement is where the server last mapped this surface. It stays zero
	// (desktop origin, native size) until a mapping command says otherwise,
	// which is where the main desktop surface belongs anyway.
	placement SurfacePlacement

	pixels []byte
	mu     sync.Mutex
}

// Origin returns where the surface is mapped on the desktop. A client that
// composites surfaces itself has to offset them by this, or everything ends up
// in the top left corner. A window mapping carries no desktop origin, so the
// last output origin is kept and this never moves a surface to the corner.
func (s *Surface) Origin() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.placement.X, s.placement.Y
}

// Placement returns the mapping the server most recently applied to this
// surface: the output origin from an output mapping, or the window id and the
// mapped and target sizes from a window mapping. A surface that was never
// mapped reports the zero placement.
func (s *Surface) Placement() SurfacePlacement {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.placement
	p.ID = s.ID
	return p
}

// Pixels returns a copy of the surface contents as BGRA, top down, at the
// surface's native size.
func (s *Surface) Pixels() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.pixels))
	copy(out, s.pixels)
	return out
}

// CompositePixels returns the pixels to composite for this surface and the size
// to composite them at. When the server mapped the surface to the output with a
// target size (MapSurfaceToScaledOutput), the pixels are resampled to that size
// so the surface is drawn at the size the server asked for. In every other case
// the native pixels and size are returned, so a compositor can always draw the
// result at the size this returns.
//
// The two window mappings carry a target size as well, but they are not
// resampled: the surface belongs at a position inside a window this client has
// no model for, and scaling it without a destination would not be faithful.
// Placement reports what was recorded for them.
func (s *Surface) CompositePixels() ([]byte, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.placement
	if p.Windowed || p.TargetWidth <= 0 || p.TargetHeight <= 0 ||
		(p.TargetWidth == s.Width && p.TargetHeight == s.Height) {
		out := make([]byte, len(s.pixels))
		copy(out, s.pixels)
		return out, s.Width, s.Height
	}
	return resample(s.pixels, s.Width, s.Height, p.TargetWidth, p.TargetHeight),
		p.TargetWidth, p.TargetHeight
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

	// zgfx unwraps the channel's bulk compression. EGFX does not put bare
	// RDPGFX PDUs on the wire: every message is a ZGFX stream, and its history
	// is shared between messages, so this has to live for the whole channel and
	// be fed every message in order.
	zgfx *codec.ZGFX
}

// NewGfxClient creates an RDPGFX handler.
func NewGfxClient() *GfxClient {
	return &GfxClient{
		Emitter:  *emission.NewEmitter(),
		surfaces: make(map[uint16]*Surface),
		zgfx:     codec.NewZGFX(),
	}
}

// SetSender installs the callback used to write to the dynamic virtual
// channel. It must be called before the channel is opened.
func (c *GfxClient) SetSender(f func(channelID uint32, data []byte) error) { c.send = f }

// dumpZGFX records every payload received on the graphics channel, in arrival
// order, when GRDP_DUMP_ZGFX names a file. It exists because the decompressor is
// easiest to trust when it can be run against real traffic: the capture feeds
// codec/realdata_test.go, which decodes it and compares against the reference
// decoder. Nothing happens unless the variable is set.
//
// The framing is the one that test reads: each payload as a little endian
// uint32 length followed by that many bytes.
var dumpZGFX = func() io.Writer {
	name := os.Getenv("GRDP_DUMP_ZGFX")
	if name == "" {
		return nil
	}
	f, err := os.Create(name)
	if err != nil {
		glog.Errorf("rdpgfx: cannot open %s: %v", name, err)
		return nil
	}
	return f
}()

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

// OnData implements drdynvc.DynChannel. The payload is a ZGFX stream that may
// hold several PDUs.
func (c *GfxClient) OnData(data []byte) {
	if dumpZGFX != nil {
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(data)))
		dumpZGFX.Write(l[:])
		dumpZGFX.Write(data)
	}

	pdu, err := c.zgfx.Decompress(data)
	if err != nil {
		glog.Errorf("rdpgfx: cannot decompress %d bytes: %v", len(data), err)
		return
	}
	data = pdu

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
	case cmdMapSurfaceToOutput:
		return c.processMapSurfaceToOutput(body)
	case cmdMapSurfaceToScaledOutput:
		return c.processMapSurfaceToScaledOutput(body)
	case cmdMapSurfaceToWindow:
		return c.processMapSurfaceToWindow(body)
	case cmdMapSurfaceToScaledWindow:
		return c.processMapSurfaceToScaledWindow(body)
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

// processMapSurfaceToOutput records where a surface sits on the desktop.
func (c *GfxClient) processMapSurfaceToOutput(body []byte) error {
	// surfaceId(2) reserved(2) outputOriginX(4, signed) outputOriginY(4, signed).
	// This is RDPGFX_MAP_SURFACE_TO_OUTPUT_PDU, twelve bytes.
	if len(body) < 12 {
		return fmt.Errorf("map surface to output: %w", errShort)
	}
	id := binary.LittleEndian.Uint16(body)
	x := int(int32(binary.LittleEndian.Uint32(body[4:])))
	y := int(int32(binary.LittleEndian.Uint32(body[8:])))
	return c.recordPlacement("map surface to output", id, SurfacePlacement{ID: id, X: x, Y: y})
}

// processMapSurfaceToScaledOutput records where a surface sits on the desktop
// and the size it has to be resampled to. The scale is applied by
// CompositePixels.
func (c *GfxClient) processMapSurfaceToScaledOutput(body []byte) error {
	// surfaceId(2) reserved(2) outputOriginX(4, signed) outputOriginY(4, signed)
	// targetWidth(4) targetHeight(4). This is
	// RDPGFX_MAP_SURFACE_TO_SCALED_OUTPUT_PDU, twenty bytes.
	if len(body) < 20 {
		return fmt.Errorf("map surface to scaled output: %w", errShort)
	}
	id := binary.LittleEndian.Uint16(body)
	x := int(int32(binary.LittleEndian.Uint32(body[4:])))
	y := int(int32(binary.LittleEndian.Uint32(body[8:])))
	tw := binary.LittleEndian.Uint32(body[12:])
	th := binary.LittleEndian.Uint32(body[16:])
	if err := checkScaledTarget(tw, th); err != nil {
		return fmt.Errorf("map surface to scaled output: %w", err)
	}
	return c.recordPlacement("map surface to scaled output", id, SurfacePlacement{
		ID: id, X: x, Y: y,
		TargetWidth: int(tw), TargetHeight: int(th),
	})
}

// processMapSurfaceToWindow records the window a surface belongs to and the
// size it is mapped at there. The client has no window model to composite into,
// so nothing is applied; Placement reports what was recorded.
func (c *GfxClient) processMapSurfaceToWindow(body []byte) error {
	// surfaceId(2) windowId(8) mappedWidth(4) mappedHeight(4). This is
	// RDPGFX_MAP_SURFACE_TO_WINDOW_PDU, eighteen bytes.
	if len(body) < 18 {
		return fmt.Errorf("map surface to window: %w", errShort)
	}
	id := binary.LittleEndian.Uint16(body)
	return c.recordPlacement("map surface to window", id, SurfacePlacement{
		ID:           id,
		Windowed:     true,
		WindowID:     binary.LittleEndian.Uint64(body[2:]),
		MappedWidth:  int(binary.LittleEndian.Uint32(body[10:])),
		MappedHeight: int(binary.LittleEndian.Uint32(body[14:])),
	})
}

// processMapSurfaceToScaledWindow records the window, the mapped size and the
// target size of a scaled window placement. None of it is applied, for the same
// reason as a plain window mapping.
func (c *GfxClient) processMapSurfaceToScaledWindow(body []byte) error {
	// surfaceId(2) windowId(8) mappedWidth(4) mappedHeight(4) targetWidth(4)
	// targetHeight(4). This is RDPGFX_MAP_SURFACE_TO_SCALED_WINDOW_PDU,
	// twenty-six bytes.
	if len(body) < 26 {
		return fmt.Errorf("map surface to scaled window: %w", errShort)
	}
	id := binary.LittleEndian.Uint16(body)
	tw := binary.LittleEndian.Uint32(body[18:])
	th := binary.LittleEndian.Uint32(body[22:])
	if err := checkScaledTarget(tw, th); err != nil {
		return fmt.Errorf("map surface to scaled window: %w", err)
	}
	return c.recordPlacement("map surface to scaled window", id, SurfacePlacement{
		ID:           id,
		Windowed:     true,
		WindowID:     binary.LittleEndian.Uint64(body[2:]),
		MappedWidth:  int(binary.LittleEndian.Uint32(body[10:])),
		MappedHeight: int(binary.LittleEndian.Uint32(body[14:])),
		TargetWidth:  int(tw),
		TargetHeight: int(th),
	})
}

// recordPlacement stores a mapping on the named surface and reports it. A
// window mapping has no desktop origin, so it keeps the last one rather than
// resetting the surface to the top left corner.
func (c *GfxClient) recordPlacement(name string, id uint16, p SurfacePlacement) error {
	c.mu.Lock()
	s := c.surfaces[id]
	c.mu.Unlock()
	if s == nil {
		return fmt.Errorf("%s: unknown surface %d", name, id)
	}

	s.mu.Lock()
	if p.Windowed {
		p.X, p.Y = s.placement.X, s.placement.Y
	}
	s.placement = p
	s.mu.Unlock()

	c.Emit("gfx-surface-mapped", p)
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
	// surfaceId(2) codecId(2) pixelFormat(1) destRect(8, four little endian
	// uint16) bitmapDataLength(4) bitmapData. There is no reserved byte between
	// the pixel format and the rectangle: the fields add up to
	// RDPGFX_WIRE_TO_SURFACE_PDU_1_SIZE, which is 17. Getting this wrong shifts
	// the destination and turns the length into 0xC00000xx. See
	// testdata/README.md, which is how it was found.
	if len(body) < 17 {
		return fmt.Errorf("wire to surface 1: %w", errShort)
	}
	surfaceID := binary.LittleEndian.Uint16(body)
	codecID := binary.LittleEndian.Uint16(body[2:])
	pixelFormat := body[4]
	left := int(binary.LittleEndian.Uint16(body[5:]))
	top := int(binary.LittleEndian.Uint16(body[7:]))
	right := int(binary.LittleEndian.Uint16(body[9:]))
	bottom := int(binary.LittleEndian.Uint16(body[11:]))
	dataLen := int(binary.LittleEndian.Uint32(body[13:]))
	if dataLen < 0 || 17+dataLen > len(body) {
		return fmt.Errorf("wire to surface 1: declared %d bytes, have %d: %w",
			dataLen, len(body)-17, errShort)
	}
	payload := body[17 : 17+dataLen]

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
	case codecProgressive, codecProgressiveV2:
		// The progressive variants need their own arithmetic decoder. The
		// THINCLIENT flag asked the server not to use them, so reaching here
		// means the negotiation went differently than expected.
		return nil, fmt.Errorf("RDPGFX codec 0x%04x is RemoteFX Progressive, which was not advertised", codecID)
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

// checkScaledTarget rejects a target size that cannot be honoured. The parser
// runs this before the value reaches CompositePixels, which allocates for it.
func checkScaledTarget(w, h uint32) error {
	if w == 0 || h == 0 {
		return fmt.Errorf("zero target size %dx%d", w, h)
	}
	if w > maxScaledDimension || h > maxScaledDimension {
		return fmt.Errorf("target size %dx%d exceeds %d", w, h, maxScaledDimension)
	}
	return nil
}

// resample scales a srcW x srcH BGRA image to dstW x dstH by nearest
// neighbour, which is exact for whole number scale factors and never reads
// outside the source. dstW and dstH are bounded by checkScaledTarget before
// this is reached, so the destination allocation is bounded before it is made.
func resample(src []byte, srcW, srcH, dstW, dstH int) []byte {
	dst := make([]byte, dstW*dstH*4)
	for y := 0; y < dstH; y++ {
		sy := y * srcH / dstH
		for x := 0; x < dstW; x++ {
			sx := x * srcW / dstW
			si := (sy*srcW + sx) * 4
			di := (y*dstW + x) * 4
			if si+4 > len(src) {
				return dst
			}
			copy(dst[di:di+4], src[si:si+4])
		}
	}
	return dst
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
		out = appendU32(out, v)                  // version
		out = appendU32(out, 4)                  // length of the flags that follow
		out = appendU32(out, capsFlagThinClient) // flags
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
