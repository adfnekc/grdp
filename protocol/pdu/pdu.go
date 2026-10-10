package pdu

import (
	"bytes"
	"io"
	"sync"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/t125/gcc"
)

type PDULayer struct {
	emission.Emitter
	transport          core.Transport
	sharedId           uint32
	userId             uint16
	channelId          uint16
	serverCapabilities map[CapsType]Capability
	clientCapabilities map[CapsType]Capability
	fastPathSender     core.FastPathSender
	demandActivePDU    *DemandActivePDU
}

func NewPDULayer(t core.Transport) *PDULayer {
	p := &PDULayer{
		Emitter:   *emission.NewEmitter(),
		transport: t,
		sharedId:  0x103EA,
		serverCapabilities: map[CapsType]Capability{
			CAPSTYPE_GENERAL: &GeneralCapability{
				ProtocolVersion: 0x0200,
			},
			CAPSTYPE_BITMAP: &BitmapCapability{
				Receive1BitPerPixel:      0x0001,
				Receive4BitsPerPixel:     0x0001,
				Receive8BitsPerPixel:     0x0001,
				BitmapCompressionFlag:    0x0001,
				MultipleRectangleSupport: 0x0001,
			},
			CAPSTYPE_ORDER: &OrderCapability{
				DesktopSaveXGranularity: 1,
				DesktopSaveYGranularity: 20,
				MaximumOrderLevel:       1,
				OrderFlags:              NEGOTIATEORDERSUPPORT,
				DesktopSaveSize:         480 * 480,
			},
			CAPSTYPE_POINTER:        &PointerCapability{ColorPointerCacheSize: 20},
			CAPSTYPE_INPUT:          &InputCapability{},
			CAPSTYPE_VIRTUALCHANNEL: &VirtualChannelCapability{},
			CAPSTYPE_FONT:           &FontCapability{SupportFlags: 0x0001},
			CAPSTYPE_COLORCACHE:     &ColorCacheCapability{CacheSize: 0x0006},
			CAPSTYPE_SHARE:          &ShareCapability{},
		},
		clientCapabilities: map[CapsType]Capability{
			CAPSTYPE_GENERAL: &GeneralCapability{
				ProtocolVersion: 0x0200,
			},
			CAPSTYPE_BITMAP: &BitmapCapability{
				Receive1BitPerPixel:      0x0001,
				Receive4BitsPerPixel:     0x0001,
				Receive8BitsPerPixel:     0x0001,
				BitmapCompressionFlag:    0x0001,
				MultipleRectangleSupport: 0x0001,
			},
			CAPSTYPE_ORDER: &OrderCapability{
				DesktopSaveXGranularity: 1,
				DesktopSaveYGranularity: 20,
				MaximumOrderLevel:       1,
				OrderFlags:              NEGOTIATEORDERSUPPORT,
				DesktopSaveSize:         480 * 480,
				TextANSICodePage:        0x4e4,
			},
			CAPSTYPE_CONTROL:         &ControlCapability{0, 0, 2, 2},
			CAPSTYPE_ACTIVATION:      &WindowActivationCapability{},
			CAPSTYPE_POINTER:         &PointerCapability{1, 20, 20},
			CAPSTYPE_SHARE:           &ShareCapability{},
			CAPSTYPE_COLORCACHE:      &ColorCacheCapability{6, 0},
			CAPSTYPE_SOUND:           &SoundCapability{0x0001, 0},
			CAPSTYPE_INPUT:           &InputCapability{},
			CAPSTYPE_FONT:            &FontCapability{0x0001, 0},
			CAPSTYPE_BRUSH:           &BrushCapability{BRUSH_COLOR_8x8},
			CAPSTYPE_GLYPHCACHE:      &GlyphCapability{},
			CAPSETTYPE_BITMAP_CODECS: NewNSCodecCapability(),
			CAPSTYPE_BITMAPCACHE_REV2: &BitmapCache2Capability{
				BitmapCachePersist: 2,
				CachesNum:          5,
				BmpC0Cells:         0x258,
				BmpC1Cells:         0x258,
				BmpC2Cells:         0x800,
				BmpC3Cells:         0x1000,
				BmpC4Cells:         0x800,
			},
			CAPSTYPE_VIRTUALCHANNEL:        &VirtualChannelCapability{0, 1600},
			CAPSETTYPE_MULTIFRAGMENTUPDATE: &MultiFragmentUpdate{65535},
			CAPSTYPE_RAIL: &RemoteProgramsCapability{
				RailSupportLevel: RAIL_LEVEL_SUPPORTED |
					RAIL_LEVEL_SHELL_INTEGRATION_SUPPORTED |
					RAIL_LEVEL_LANGUAGE_IME_SYNC_SUPPORTED |
					RAIL_LEVEL_SERVER_TO_CLIENT_IME_SYNC_SUPPORTED |
					RAIL_LEVEL_HIDE_MINIMIZED_APPS_SUPPORTED |
					RAIL_LEVEL_WINDOW_CLOAKING_SUPPORTED |
					RAIL_LEVEL_HANDSHAKE_EX_SUPPORTED |
					RAIL_LEVEL_DOCKED_LANGBAR_SUPPORTED,
			},
			CAPSETTYPE_LARGE_POINTER: &LargePointerCapability{1},
			CAPSETTYPE_SURFACE_COMMANDS: &SurfaceCommandsCapability{
				CmdFlags: SURFCMDS_SET_SURFACE_BITS | SURFCMDS_STREAM_SURFACE_BITS | SURFCMDS_FRAME_MARKER,
			},
			CAPSSETTYPE_FRAME_ACKNOWLEDGE: &FrameAcknowledgeCapability{2},
		},
	}

	t.On("close", func() {
		p.Emit("close")
	}).On("error", func(err error) {
		p.Emit("error", err)
	})
	return p
}

func (p *PDULayer) sendPDU(message PDUMessage) {
	pdu := NewPDU(p.userId, message)
	b := pdu.serialize()
	glog.Trace("pdu send:", glog.Hex(b))
	p.transport.Write(b)
}

func (p *PDULayer) sendDataPDU(message DataPDUData) {
	dataPdu := NewDataPDU(message, p.sharedId)
	p.sendPDU(dataPdu)
}

func (p *PDULayer) SetFastPathSender(f core.FastPathSender) {
	p.fastPathSender = f
}

type Client struct {
	*PDULayer
	clientCoreData *gcc.ClientCoreData
	buff           *bytes.Buffer

	// orders is the parsing state for drawing orders: the order type, bounds
	// and per type fields that delta coordinates refer back to. It belongs to
	// one connection, which is why it lives here and not in the parser.
	orders *OrderState

	// capsMu guards serverCapabilities, which used to be a plain map written
	// from a listener that the emitter could run twice at once. A concurrent
	// write to a map is a run time fatal error, and recover does not catch
	// those: it took down the process and every session in it, which is how it
	// was found.
	capsMu sync.Mutex
}

func NewClient(t core.Transport) *Client {
	c := &Client{
		PDULayer: NewPDULayer(t),
		buff:     &bytes.Buffer{},
		orders:   NewOrderState(),
	}
	c.transport.Once("connect", c.connect)
	return c
}

// SetOrderSupport declares which drawing orders the client can draw.
//
// It is off by default, and that is deliberate. MEMBLT is the only way a cached
// bitmap reaches the screen, so a server that sees it advertised moves to orders
// entirely, cache fills included, instead of mixing the two paths. A client that
// advertises it without being able to draw it gets a blank screen rather than a
// slower one, so this is a switch rather than a default.
//
// Only the orders that are actually drawn are declared. Advertising one this
// client cannot draw would be worse than not advertising it at all, because the
// server would then send it and the screen would lose that part. That leaves out
// LINETO and SAVEBITMAP, which parse and are not drawn, FAST_INDEX, which needs
// the glyph path, and the nine grid and FAST_GLYPH orders, which are not parsed.
func (c *Client) SetOrderSupport(enabled bool) {
	capa, ok := c.clientCapabilities[CAPSTYPE_ORDER].(*OrderCapability)
	if !ok || capa == nil {
		return
	}

	// MEMBLT only, and that is a measurement rather than an oversight. A Windows
	// 10 host was watched with every order below advertised as well: it sent 1473
	// MEMBLTs and not one shape, glyph or multi order. So advertising them buys
	// nothing here, and on an older server that does draw with them it would ask
	// for orders this client parses and draws but has never seen, where declining
	// to advertise them leaves that server on bitmap updates, which are verified.
	// They are implemented and unit tested anyway, as a safety net for a server
	// that sends them regardless.
	drawn := []Order{
		TS_NEG_MEMBLT_INDEX,
	}
	var bit uint8
	if enabled {
		bit = 1
	}
	for _, idx := range drawn {
		capa.OrderSupport[idx] = bit
	}

}

func (c *Client) connect(data *gcc.ClientCoreData, userId uint16, channelId uint16) {
	glog.Debug("pdu connect:", userId, ",", channelId)
	c.clientCoreData = data
	c.userId = userId
	c.channelId = channelId
	c.transport.Once("data", c.recvDemandActivePDU)
}

// ServerCapabilities returns the capability set the server announced in its
// demand active PDU, keyed by type.
//
// It is the server's statement of what it will do, which is what a client's
// replies and expectations are built around, so it is worth being able to look
// at. The map is a copy; the values are the parsed structures and must not be
// modified.
func (c *Client) ServerCapabilities() map[CapsType]Capability {
	c.capsMu.Lock()
	defer c.capsMu.Unlock()
	out := make(map[CapsType]Capability, len(c.serverCapabilities))
	for k, v := range c.serverCapabilities {
		out[k] = v
	}
	return out
}

func (c *Client) recvDemandActivePDU(s []byte) {
	glog.Trace("PDU recvDemandActivePDU", glog.Hex(s))
	r := bytes.NewReader(s)
	pdu, err := readPDU(r)
	if err != nil {
		glog.Error(err)
		return
	}
	if pdu.ShareCtrlHeader.PDUType != PDUTYPE_DEMANDACTIVEPDU {
		glog.Info("PDU ignore message during connection sequence, type is", pdu.ShareCtrlHeader.PDUType)
		c.transport.Once("data", c.recvDemandActivePDU)
		return
	}
	c.sharedId = pdu.Message.(*DemandActivePDU).SharedId
	c.demandActivePDU = pdu.Message.(*DemandActivePDU)
	c.capsMu.Lock()
	for _, caps := range c.demandActivePDU.CapabilitySets {
		glog.Debugf("serverCapabilities<%s>: %+v", caps.Type(), caps)
		c.serverCapabilities[caps.Type()] = caps
	}
	c.capsMu.Unlock()

	c.sendConfirmActivePDU()
	c.sendClientFinalizeSynchronizePDU()
	c.transport.Once("data", c.recvServerSynchronizePDU)
}

func (c *Client) sendConfirmActivePDU() {
	glog.Debug("PDU start sendConfirmActivePDU")

	pdu := NewConfirmActivePDU()
	generalCapa := c.clientCapabilities[CAPSTYPE_GENERAL].(*GeneralCapability)
	generalCapa.OSMajorType = OSMAJORTYPE_WINDOWS
	generalCapa.OSMinorType = OSMINORTYPE_WINDOWS_NT
	generalCapa.ExtraFlags = LONG_CREDENTIALS_SUPPORTED | NO_BITMAP_COMPRESSION_HDR |
		FASTPATH_OUTPUT_SUPPORTED | AUTORECONNECT_SUPPORTED
	generalCapa.RefreshRectSupport = 0
	generalCapa.SuppressOutputSupport = 0

	bitmapCapa := c.clientCapabilities[CAPSTYPE_BITMAP].(*BitmapCapability)
	bitmapCapa.PreferredBitsPerPixel = c.clientCoreData.HighColorDepth
	bitmapCapa.DesktopWidth = c.clientCoreData.DesktopWidth
	bitmapCapa.DesktopHeight = c.clientCoreData.DesktopHeight
	bitmapCapa.DesktopResizeFlag = 0x0001

	orderCapa := c.clientCapabilities[CAPSTYPE_ORDER].(*OrderCapability)
	orderCapa.OrderFlags = NEGOTIATEORDERSUPPORT | ZEROBOUNDSDELTASSUPPORT | COLORINDEXSUPPORT | ORDERFLAGS_EXTRA_FLAGS
	orderCapa.OrderSupportExFlags |= ORDERFLAGS_EX_ALTSEC_FRAME_MARKER_SUPPORT
	// ORDERFLAGS_EX_CACHE_BITMAP_REV3_SUPPORT is deliberately not set. Rev3
	// entries carry either a bitmap codec or an RDP 6.1 compressed blob, and the
	// latter is not decoded here, so advertising it would claim support that is
	// not there. Measured against Windows it makes no difference: the server
	// sends revision 2 whether or not it is offered.
	orderCapa.OrderSupport[TS_NEG_DSTBLT_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_PATBLT_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_SCRBLT_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_LINETO_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_MEMBLT_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_MEM3BLT_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_POLYLINE_INDEX] = 1
	/*orderCapa.OrderSupport[TS_NEG_MULTIOPAQUERECT_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_GLYPH_INDEX_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_DRAWNINEGRID_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_SAVEBITMAP_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_POLYGON_SC_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_POLYGON_CB_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_ELLIPSE_SC_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_ELLIPSE_CB_INDEX] = 1*/
	orderCapa.OrderSupport[TS_NEG_FAST_GLYPH_INDEX] = 1

	inputCapa := c.clientCapabilities[CAPSTYPE_INPUT].(*InputCapability)
	inputCapa.Flags = INPUT_FLAG_SCANCODES | INPUT_FLAG_MOUSEX | INPUT_FLAG_UNICODE |
		INPUT_FLAG_FASTPATH_INPUT | INPUT_FLAG_FASTPATH_INPUT2
	inputCapa.KeyboardLayout = c.clientCoreData.KbdLayout
	inputCapa.KeyboardType = c.clientCoreData.KeyboardType
	inputCapa.KeyboardSubType = c.clientCoreData.KeyboardSubType
	inputCapa.KeyboardFunctionKey = c.clientCoreData.KeyboardFnKeys
	inputCapa.ImeFileName = c.clientCoreData.ImeFileName

	glyphCapa := c.clientCapabilities[CAPSTYPE_GLYPHCACHE].(*GlyphCapability)
	/*glyphCapa.GlyphCache[0] = cacheEntry{254, 4}
	glyphCapa.GlyphCache[1] = cacheEntry{254, 4}
	glyphCapa.GlyphCache[2] = cacheEntry{254, 8}
	glyphCapa.GlyphCache[3] = cacheEntry{254, 8}
	glyphCapa.GlyphCache[4] = cacheEntry{254, 16}
	glyphCapa.GlyphCache[5] = cacheEntry{254, 32}
	glyphCapa.GlyphCache[6] = cacheEntry{254, 64}
	glyphCapa.GlyphCache[7] = cacheEntry{254, 128}
	glyphCapa.GlyphCache[8] = cacheEntry{254, 256}
	glyphCapa.GlyphCache[9] = cacheEntry{64, 2048}
	glyphCapa.FragCache = 0x01000100*/
	glyphCapa.SupportLevel = GLYPH_SUPPORT_NONE

	pdu.SharedId = c.sharedId
	for _, v := range c.clientCapabilities {
		glog.Debugf("clientCapabilities<%s>: %+v", v.Type(), v)
		pdu.CapabilitySets = append(pdu.CapabilitySets, v)
	}
	pdu.NumberCapabilities = uint16(len(pdu.CapabilitySets))
	pdu.LengthSourceDescriptor = c.demandActivePDU.LengthSourceDescriptor
	pdu.SourceDescriptor = c.demandActivePDU.SourceDescriptor
	pdu.LengthCombinedCapabilities = c.demandActivePDU.LengthCombinedCapabilities

	c.sendPDU(pdu)
}

func (c *Client) sendClientFinalizeSynchronizePDU() {
	glog.Debug("PDU start sendClientFinalizeSynchronizePDU")
	c.sendDataPDU(NewSynchronizeDataPDU(c.channelId))
	c.sendDataPDU(&ControlDataPDU{Action: CTRLACTION_COOPERATE})
	c.sendDataPDU(&ControlDataPDU{Action: CTRLACTION_REQUEST_CONTROL})
	//c.sendDataPDU(&PersistKeyPDU{BBitMask: 0x03})
	c.sendDataPDU(&FontListDataPDU{ListFlags: 0x0003, EntrySize: 0x0032})
}

func (c *Client) recvServerSynchronizePDU(s []byte) {
	glog.Debug("PDU recvServerSynchronizePDU")
	r := bytes.NewReader(s)
	pdu, err := readPDU(r)
	if err != nil {
		glog.Error(err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_SYNCHRONIZE {
		if ok {
			glog.Error("recvServerSynchronizePDU ignore datapdu type2", dataPdu.Header.PDUType2)
		} else {
			glog.Error("recvServerSynchronizePDU ignore message type", pdu.ShareCtrlHeader.PDUType)
		}
		glog.Infof("%+v", dataPdu)
		c.transport.Once("data", c.recvServerSynchronizePDU)
		return
	}
	c.transport.Once("data", c.recvServerControlCooperatePDU)
}

func (c *Client) recvServerControlCooperatePDU(s []byte) {
	glog.Debug("PDU recvServerControlCooperatePDU")
	r := bytes.NewReader(s)
	pdu, err := readPDU(r)
	if err != nil {
		glog.Error(err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_CONTROL {
		if ok {
			glog.Error("recvServerControlCooperatePDU ignore datapdu type2", dataPdu.Header.PDUType2)
		} else {
			glog.Error("recvServerControlCooperatePDU ignore message type", pdu.ShareCtrlHeader.PDUType)
		}
		c.transport.Once("data", c.recvServerControlCooperatePDU)
		return
	}
	if dataPdu.Data.(*ControlDataPDU).Action != CTRLACTION_COOPERATE {
		glog.Error("recvServerControlCooperatePDU ignore action", dataPdu.Data.(*ControlDataPDU).Action)
		c.transport.Once("data", c.recvServerControlCooperatePDU)
		return
	}
	c.transport.Once("data", c.recvServerControlGrantedPDU)
}

func (c *Client) recvServerControlGrantedPDU(s []byte) {
	glog.Debug("PDU recvServerControlGrantedPDU")
	r := bytes.NewReader(s)
	pdu, err := readPDU(r)
	if err != nil {
		glog.Error(err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_CONTROL {
		if ok {
			glog.Error("recvServerControlGrantedPDU ignore datapdu type2", dataPdu.Header.PDUType2)
		} else {
			glog.Error("recvServerControlGrantedPDU ignore message type", pdu.ShareCtrlHeader.PDUType)
		}
		c.transport.Once("data", c.recvServerControlGrantedPDU)
		return
	}
	if dataPdu.Data.(*ControlDataPDU).Action != CTRLACTION_GRANTED_CONTROL {
		glog.Error("recvServerControlGrantedPDU ignore action", dataPdu.Data.(*ControlDataPDU).Action)
		c.transport.Once("data", c.recvServerControlGrantedPDU)
		return
	}
	c.transport.Once("data", c.recvServerFontMapPDU)
}

func (c *Client) recvServerFontMapPDU(s []byte) {
	glog.Debug("PDU recvServerFontMapPDU")
	r := bytes.NewReader(s)
	pdu, err := readPDU(r)
	if err != nil {
		glog.Error(err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_FONTMAP {
		if ok {
			glog.Error("recvServerFontMapPDU ignore datapdu type2", dataPdu.Header.PDUType2)
		} else {
			glog.Error("recvServerFontMapPDU ignore message type", pdu.ShareCtrlHeader.PDUType)
		}
		return
	}
	c.transport.On("data", c.recvPDU)
	c.Emit("ready")
}

func (c *Client) recvPDU(s []byte) {
	glog.Trace("PDU recvPDU", glog.Hex(s))
	r := bytes.NewReader(s)
	if r.Len() > 0 {
		p, err := readPDU(r)
		if err != nil {
			glog.Error(err)
			return
		}
		if p.ShareCtrlHeader.PDUType == PDUTYPE_DEACTIVATEALLPDU {
			// A deactivate-all is the server starting a new session, so the
			// orders after it no longer repeat fields from the session before
			// it. Drop the per connection carry over with it.
			c.orders.Reset()
			c.transport.Once("data", c.recvDemandActivePDU)
		} else if p.ShareCtrlHeader.PDUType == PDUTYPE_DATAPDU {
			d := p.Message.(*DataPDU)
			switch d.Header.PDUType2 {
			case PDUTYPE2_POINTER:
				c.Emit("pointer", d.Data.(*PointerDataPDU))
			case PDUTYPE2_UPDATE:
				up := d.Data.(*UpdateDataPDU)
				p := up.Udata
				if up.UpdateType == FASTPATH_UPDATETYPE_BITMAP {
					c.Emit("bitmap", p.(*BitmapUpdateDataPDU).Rectangles)
				} else if up.UpdateType == FASTPATH_UPDATETYPE_ORDERS {
					c.Emit("orders", p.(*FastPathOrdersPDU).OrderPdus)
				}
			}
		}
	}
}

func (c *Client) RecvFastPath(secFlag byte, s []byte) {
	glog.Trace("PDU RecvFastPath", glog.Hex(s))
	r := bytes.NewReader(s)
	for r.Len() > 0 {
		updateHeader, err := core.ReadUInt8(r)
		if err != nil {
			return
		}
		updateCode := updateHeader & 0x0f
		fragmentation := updateHeader & 0x30
		compression := updateHeader & 0xC0

		var compressionFlags uint8 = 0
		if compression == FASTPATH_OUTPUT_COMPRESSION_USED {
			compressionFlags, err = core.ReadUInt8(r)
		}

		size, err := core.ReadUint16LE(r)

		if err != nil {
			return
		}
		glog.Trace("Code:", FastPathUpdateType(updateCode),
			"compressionFlags:", compressionFlags,
			"fragmentation:", fragmentation,
			"size:", size, "len:", r.Len())
		if compressionFlags&RDP_MPPC_COMPRESSED != 0 {
			glog.Info("RDP_MPPC_COMPRESSED")
		}
		// src bounds this update's payload. It defaults to the shared reader so
		// that several updates in one PDU stay in sync; a body copy is used when
		// the declared size must be enforced, and the reassembly buffer when the
		// update was fragmented.
		src := io.Reader(r)
		if fragmentation != FASTPATH_FRAGMENT_SINGLE {
			if fragmentation == FASTPATH_FRAGMENT_FIRST {
				c.buff.Reset()
			}
			b, _ := core.ReadBytes(r.Len(), r)
			c.buff.Write(b)
			if fragmentation != FASTPATH_FRAGMENT_LAST {
				return
			}
			src = bytes.NewReader(c.buff.Bytes())
		} else if size > 0 {
			// A fast-path update is self-describing: bound the reader to the
			// declared size so that a payload we parse short (or long) cannot
			// desynchronise the following updates in the same PDU.
			body, err := core.ReadBytes(int(size), r)
			if err != nil {
				glog.Debug("fastpath: short update body:", err)
				return
			}
			src = bytes.NewReader(body)
		}

		p, err := readFastPathUpdatePDU(src, updateCode, c.orders)
		if err != nil || p == nil || p.Data == nil {
			glog.Debug("readFastPathUpdatePDU:", err)
			return
		}

		if updateCode == FASTPATH_UPDATETYPE_BITMAP {
			c.Emit("bitmap", p.Data.(*FastPathBitmapUpdateDataPDU).Rectangles)
		} else if updateCode == FASTPATH_UPDATETYPE_ORDERS {
			c.Emit("orders", p.Data.(*FastPathOrdersPDU).OrderPdus)
		} else if updateCode == FASTPATH_UPDATETYPE_PTR_POSITION {
			c.Emit("pointer-position", p.Data.(*FastPathPointerPositionPDU))
		} else if updateCode == FASTPATH_UPDATETYPE_SURFCMDS {
			for _, cmd := range p.Data.(*SurfaceCommandsPDU).Commands {
				if cmd.Bits != nil {
					c.Emit("surface-bits", cmd.Bits)
				} else if cmd.Marker != nil {
					c.Emit("frame-marker", cmd.Marker)
				}
			}
		} else if updateCode == FASTPATH_UPDATETYPE_PTR_NULL || updateCode == FASTPATH_UPDATETYPE_PTR_DEFAULT {
			// These carry no payload at all: the update code is the whole
			// message. They are the two system pointers the specification
			// names, and nothing used to be emitted for either, so a client
			// was never told that the server had asked for no pointer or for
			// the default arrow.
			if updateCode == FASTPATH_UPDATETYPE_PTR_NULL {
				c.Emit("pointer-system", uint32(SYSPTR_NULL))
			} else {
				c.Emit("pointer-system", uint32(SYSPTR_DEFAULT))
			}
		} else if updateCode == FASTPATH_UPDATETYPE_COLOR || updateCode == FASTPATH_UPDATETYPE_CACHED ||
			updateCode == FASTPATH_UPDATETYPE_POINTER || updateCode == FASTPATH_UPDATETYPE_LARGE_POINTER {
			c.Emit("pointer-shape", p.Data.(*FastPathPointerPDU))
		}
	}
}

type InputEventsInterface interface {
	Serialize() []byte
}

// Fast-path input constants (MS-RDPBCGR 2.2.8.1.2).
const (
	FASTPATH_INPUT_ACTION_FASTPATH = 0x0
	FASTPATH_INPUT_ENCRYPTED       = 0x2

	FASTPATH_INPUT_EVENT_SCANCODE = 0x0
	FASTPATH_INPUT_EVENT_MOUSE    = 0x1
	FASTPATH_INPUT_EVENT_MOUSEX   = 0x2
	FASTPATH_INPUT_EVENT_SYNC     = 0x3
	FASTPATH_INPUT_EVENT_UNICODE  = 0x4

	FASTPATH_INPUT_KBDFLAGS_RELEASE   = 0x01
	FASTPATH_INPUT_KBDFLAGS_EXTENDED  = 0x02
	FASTPATH_INPUT_KBDFLAGS_EXTENDED1 = 0x04
)

func fastPathEventCode(msgType uint16) (byte, bool) {
	switch msgType {
	case INPUT_EVENT_SCANCODE:
		return FASTPATH_INPUT_EVENT_SCANCODE, true
	case INPUT_EVENT_MOUSE:
		return FASTPATH_INPUT_EVENT_MOUSE, true
	case INPUT_EVENT_MOUSEX:
		return FASTPATH_INPUT_EVENT_MOUSEX, true
	case INPUT_EVENT_UNICODE:
		return FASTPATH_INPUT_EVENT_UNICODE, true
	case INPUT_EVENT_SYNC:
		return FASTPATH_INPUT_EVENT_SYNC, true
	}
	return 0, false
}

// fastPathEventCodeFor returns the fast path event code for a single event.
//
// The code comes from the event's own type rather than from the msgType of the
// whole PDU, because one PDU may carry a mixture: the input preamble every real
// client sends when the session becomes ready is a key release, a synchronise
// and another key release, and the three have different codes. Deriving one code
// for the whole slice and writing it on every event made that PDU impossible to
// express.
//
// msgType still decides between the two pointer encodings, MOUSE and MOUSEX,
// since a PointerEvent carries no hint of which one the caller wants, and it is
// ignored for the other event types because their code is not a matter of choice.
func fastPathEventCodeFor(e InputEventsInterface, msgType uint16) (byte, bool) {
	switch e.(type) {
	case *ScancodeKeyEvent:
		return FASTPATH_INPUT_EVENT_SCANCODE, true
	case *UnicodeKeyEvent:
		return FASTPATH_INPUT_EVENT_UNICODE, true
	case *SynchronizeEvent:
		return FASTPATH_INPUT_EVENT_SYNC, true
	case *PointerEvent:
		if code, ok := fastPathEventCode(msgType); ok {
			return code, true
		}
		// A pointer event with no pointer msgType is a mouse event; there is no
		// reading of the call as a request for MOUSEX.
		return FASTPATH_INPUT_EVENT_MOUSE, true
	}
	return 0, false
}

// sendFastPathInput serializes one or more input events into a TS_FP_INPUT_PDU
// (MS-RDPBCGR 2.2.8.1.2) and hands it to the fast-path sender.
//
// fpInputHeader:  action (2 bits) | numEvents << 2 | encrypted << 6
// eventHeader:    eventFlags (5 bits) | eventCode << 5
func (c *Client) sendFastPathInput(msgType uint16, events []InputEventsInterface) {
	if len(events) == 0 || len(events) > 15 {
		return
	}
	buff := &bytes.Buffer{}
	for _, e := range events {
		eventCode, ok := fastPathEventCodeFor(e, msgType)
		if !ok {
			// One unusable event used to take the whole PDU with it, silently.
			// Bailing out here rather than mid-PDU is what keeps the events that
			// came before it from being sent with a wrong count.
			return
		}
		switch ev := e.(type) {
		case *ScancodeKeyEvent:
			var flags byte
			code := ev.KeyCode
			if ev.KeyboardFlags&KBDFLAGS_RELEASE != 0 {
				flags |= FASTPATH_INPUT_KBDFLAGS_RELEASE
			}
			if ev.KeyboardFlags&KBDFLAGS_EXTENDED != 0 || code&0xFF00 == 0xE000 {
				flags |= FASTPATH_INPUT_KBDFLAGS_EXTENDED
				code &= 0xFF
			}
			buff.WriteByte(flags | (eventCode << 5))
			buff.WriteByte(byte(code & 0xFF))
		case *UnicodeKeyEvent:
			// The fast path has no separate keyboardFlags field: the release
			// flag is the release bit of the event header, the same bit the
			// scancode case sets above. Without it every event is a key press
			// and the server never sees the key come up, so nothing is typed:
			// the header was being written as eventCode<<5 alone, which drops
			// the flag on the floor.
			var flags byte
			if ev.KeyboardFlags&KBDFLAGS_RELEASE != 0 {
				flags |= FASTPATH_INPUT_KBDFLAGS_RELEASE
			}
			buff.WriteByte(flags | (eventCode << 5))
			core.WriteUInt16LE(ev.Unicode, buff)
		case *PointerEvent:
			buff.WriteByte(eventCode << 5)
			core.WriteUInt16LE(ev.PointerFlags, buff)
			core.WriteUInt16LE(ev.XPos, buff)
			core.WriteUInt16LE(ev.YPos, buff)
		case *SynchronizeEvent:
			// The fast path carries the toggle state in the low five bits of the
			// event header and has no payload after it, so this event is one
			// byte. The slow path's SynchronizeEvent is six bytes and its
			// Serialize is what that path uses; the two shapes are not
			// interchangeable and this is the fast path's.
			buff.WriteByte(byte(ev.ToggleFlags&0x1F) | (eventCode << 5))
		default:
			return
		}
	}
	if c.fastPathSender != nil {
		c.fastPathSender.SendFastPathInput(byte(len(events)), buff.Bytes())
	}
}

func (c *Client) SendInputEvents(msgType uint16, events []InputEventsInterface) {
	// Prefer fast-path input (what modern servers/clients use); fall back to the
	// slow-path T.128 input PDU when no fast-path sender is configured.
	if c.fastPathSender != nil {
		c.sendFastPathInput(msgType, events)
		return
	}

	p := &ClientInputEventPDU{}
	p.NumEvents = uint16(len(events))
	p.SlowPathInputEvents = make([]SlowPathInputEvent, 0, p.NumEvents)
	for _, in := range events {
		seria := in.Serialize()
		s := SlowPathInputEvent{0, msgType, len(seria), seria}
		p.SlowPathInputEvents = append(p.SlowPathInputEvents, s)
	}

	c.sendDataPDU(p)
}
