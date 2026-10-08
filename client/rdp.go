package client

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"net"
	"strings"
	"time"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/orders"
	"github.com/adfnekc/grdp/plugin"
	"github.com/adfnekc/grdp/plugin/cliprdr"
	"github.com/adfnekc/grdp/plugin/disp"
	"github.com/adfnekc/grdp/plugin/drdynvc"
	"github.com/adfnekc/grdp/plugin/rdpgfx"
	"github.com/adfnekc/grdp/protocol/nla"
	"github.com/adfnekc/grdp/protocol/pdu"
	"github.com/adfnekc/grdp/protocol/sec"
	"github.com/adfnekc/grdp/protocol/t125"
	"github.com/adfnekc/grdp/protocol/tpkt"
	"github.com/adfnekc/grdp/protocol/x224"
)

// RdpClient is the RDP implementation of Control. NewClient builds one (stored
// unexported) for TC_RDP; callers interact with it through Client, which forwards
// input, events and the clipboard to it.
type RdpClient struct {
	tpkt     *tpkt.TPKT
	x224     *x224.X224
	mcs      *t125.MCSClient
	sec      *sec.Client
	pdu      *pdu.Client
	channels *plugin.Channels
	setting  *Setting
	pending  []pendingEvent

	// Set when EGFX is enabled; nil otherwise.
	dvc *drdynvc.DvcClient
	gfx *rdpgfx.GfxClient

	// Set when the clipboard channel is enabled; nil otherwise.
	clip *cliprdr.CliprdrClient

	// Set when drawing orders are enabled; nil otherwise.
	screen *orders.Screen

	// disp is the display control channel, set when Setting.EnableDisplayControl
	// is on. It is how the desktop size is changed without reconnecting.
	disp *disp.DisplayControlClient

	// fb wraps screen for callers. It exists for every session, not only one
	// with orders enabled, because the bitmap path composites into the same
	// buffer.
	fb *Framebuffer

	// pointers holds the cursor shapes the server has sent and expects us to
	// keep, one slot per entry in the capability set's ColorPointerCacheSize.
	pointers *pdu.PointerCache

	// lastShape and lastSystem remember what the pointer is currently showing,
	// so that an update that changes nothing does not produce a callback. A
	// gateway re-encoding a cursor image on every one of these would spend its
	// time on a cursor that has not moved.
	lastShape  *pdu.PointerShape
	lastSystem uint32
	haveSystem bool
}

type pendingEvent struct {
	event string
	f     interface{}
}

func newRdpClient(s *Setting) *RdpClient {
	return &RdpClient{setting: s}
}

// requestedProtocol maps the configured security protocol name to the X.224
// negotiation bitmask. "rdp" asks for Standard RDP Security: no negotiation
// block is sent, so the server cannot upgrade the connection to TLS or NLA and
// the RC4 / licensing path in protocol/sec is used instead. The default is TLS,
// which is what modern servers offer.
func requestedProtocol(s *Setting) uint32 {
	if s == nil {
		return x224.PROTOCOL_SSL
	}
	switch strings.ToLower(s.Protocol) {
	case "rdp", "standard":
		return x224.PROTOCOL_RDP
	case "nla", "hybrid", "credssp":
		return x224.PROTOCOL_HYBRID
	case "", "auto", "tls", "ssl":
		return x224.PROTOCOL_SSL
	default:
		glog.Warnf("unknown security protocol %q, using TLS", s.Protocol)
		return x224.PROTOCOL_SSL
	}
}

func bitmapDecompress(bitmap *pdu.BitmapData) []byte {
	// A malformed stream yields the part that decoded. Showing an incomplete
	// update beats dropping the whole thing, but it is worth a line in the log.
	pixels, err := core.Decompress(bitmap.BitmapDataStream, int(bitmap.Width), int(bitmap.Height), Bpp(bitmap.BitsPerPixel))
	if err != nil {
		glog.Warnf("bitmap update %dx%d: %v", bitmap.Width, bitmap.Height, err)
	}
	return pixels
}
func split(user string) (domain string, uname string) {
	if strings.Index(user, "\\") != -1 {
		t := strings.Split(user, "\\")
		domain = t[0]
		uname = t[len(t)-1]
	} else if strings.Index(user, "/") != -1 {
		t := strings.Split(user, "/")
		domain = t[0]
		uname = t[len(t)-1]
	} else {
		uname = user
	}
	return
}

// Login dials host, builds the protocol stack (TPKT/X.224/MCS/security/PDU) and
// starts the connection. user may be "user", "DOMAIN\user" or "domain/user":
// the domain is split off and sent separately.
//
// Login returns once the connection has been established, which is before the
// session is usable. The session becomes ready asynchronously and is reported on
// the "ready" event; errors and closure arrive on "error" and "close". Use
// Client.Login or Client.LoginContext to connect and wait for readiness in one
// call.
func (c *RdpClient) Login(host, user, pwd string, width, height int) error {
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		// Wrapped rather than formatted so that the underlying net.OpError
		// survives: errors.Is(err, syscall.ECONNREFUSED) is how a caller tells
		// a closed port from an unreachable host, and %v would lose it.
		return fmt.Errorf("client: cannot reach the server: %w", err)
	}

	domain, user := split(user)
	c.tpkt = tpkt.New(core.NewSocketLayer(conn), nla.NewNTLMv2(domain, user, pwd))
	c.x224 = x224.New(c.tpkt)
	c.mcs = t125.NewMCSClient(c.x224)
	c.sec = sec.NewClient(c.mcs)
	c.pdu = pdu.NewClient(c.sec)
	c.channels = plugin.NewChannels(c.sec)

	// EGFX is the modern drawing path. It only makes sense once the dynamic
	// virtual channel layer and the graphics channel are both registered, so
	// it is opt in: the server may start sending surface commands instead of
	// bitmap updates as soon as the channel is created.
	//
	// The dynamic virtual channel layer also carries Display Control, so it is
	// opened when either of the two needs it. Opening it changes the connect
	// sequence, which is why it is not always on.
	if c.setting != nil && (c.setting.EnableEGFX || c.setting.EnableDisplayControl) {
		dvc := drdynvc.NewDvcClient()
		c.dvc = dvc
		c.channels.Register(dvc)
		c.mcs.SetClientDynvcProtocol()

		if c.setting.EnableEGFX {
			gfx := rdpgfx.NewGfxClient()
			gfx.SetSender(dvc.SendData)
			dvc.Register(rdpgfx.DVCChannelName, gfx)
			c.gfx = gfx
		}
		if c.setting.EnableDisplayControl {
			// The static channel as well as the dynamic one: a server that
			// is not offered it does not create the dynamic one, which is
			// how this went unnoticed against Windows at first.
			c.mcs.SetClientDisplayControl()
			d := disp.NewDisplayControlClient()
			// Registered twice on purpose: the messages are identical on the
			// static and the dynamic channel, and Windows uses the static one.
			// The dynamic registration is kept for servers that use it.
			c.channels.Register(d)
			d.SetSender(dvc.SendData)
			dvc.Register(disp.DVCChannelName, d)
			// Display Control is a channel the CLIENT asks for. Windows does
			// not create it: it waits to be asked, so a client that only
			// listens for channels the server opens never gets one. EGFX is
			// the other way round, which is why this was easy to miss.
			dvc.On("ready", func() {
				if _, err := dvc.Open(disp.DVCChannelName); err != nil {
					glog.Errorf("client: could not ask for the display control channel: %v", err)
				}
			})
			d.OnCaps(func(caps disp.Caps) {
				c.pdu.Emit("disp-caps", caps)
			})
			c.disp = d
		}
	}

	// Drawing orders. Enabling them means advertising MEMBLT, which makes the
	// server stop sending bitmap updates and put everything through the bitmap
	// cache, so the renderer has to exist before the capability is announced.
	if c.setting != nil && c.setting.EnableOrders {
		c.pdu.SetOrderSupport(true)
	}

	// The framebuffer exists for every session, whichever way the server draws.
	// Bitmap updates, surface bits and orders all land in it, so a caller has
	// one place to read pixels from instead of one per drawing path, and the
	// pixels outlive the callback that delivered them.
	c.screen = orders.NewScreen(width, height)
	c.fb = &Framebuffer{screen: c.screen}
	// The slot count has to be the one the capability set advertised, because
	// that is the contract the server indexes against. Change one and the
	// other is wrong.
	c.pointers = pdu.NewPointerCache(int(c.pdu.ColorPointerCacheSize()))
	// The ready event carries no argument, unlike the ones that follow.
	c.pdu.Once("ready", func() {
		c.pdu.On("pointer", c.onPointerPDU)
		c.pdu.On("pointer-shape", c.onFastPathPointer)
		c.pdu.On("pointer-system", func(d interface{}) {
			c.onSystemPointer(d.(uint32))
		})
		// The fast-path position update and the slow-path one mean the same
		// thing, so a caller registers once and hears about either.
		c.pdu.On("pointer-position", func(d interface{}) {
			p := d.(*pdu.FastPathPointerPositionPDU)
			c.pdu.Emit("cursor-pos", image.Point{X: int(p.XPos), Y: int(p.YPos)})
		})
		c.pdu.On("bitmap", func(d interface{}) {
			c.composite(bitmapsFromEvent(d, TC_RDP))
		})
		c.pdu.On("surface-bits", func(d interface{}) {
			c.composite(surfaceBitmaps(d.(*pdu.SurfaceBitsCommand)))
		})
		if c.setting != nil && c.setting.EnableOrders {
			c.pdu.On("orders", func(d interface{}) {
				pdus, ok := d.([]pdu.OrderPdu)
				if !ok {
					return
				}
				dirty, err := c.screen.Draw(pdus)
				if err != nil {
					glog.Errorf("orders: %v", err)
					return
				}
				if dirty.Empty() {
					return
				}
				// The old event keeps its meaning for callers that still use
				// it: one rectangle for the batch.
				c.pdu.Emit("orders-frame", dirty)
				c.pdu.Emit("frame", []image.Rectangle{dirty})
			})
		}
		if c.gfx != nil {
			c.gfx.On("gfx-frame", func(d interface{}) {
				c.compositeSurfaces(d.(rdpgfx.Frame).Surfaces)
			})
			// A reset names the desktop the server will composite into, and
			// means every surface known so far is gone, so the buffer is
			// resized and reported as entirely redrawn.
			c.gfx.On("gfx-reset", func(d interface{}) {
				r := d.(rdpgfx.Reset)
				if r.Width <= 0 || r.Height <= 0 {
					return
				}
				c.resizeFramebuffer(r.Width, r.Height)
			})
		}
	})

	if c.setting != nil && c.setting.EnableClipboard {
		clip := cliprdr.NewCliprdrClient()
		clip.OnText(func(text string) {
			if c.pdu != nil {
				c.pdu.Emit("clipboard-text", text)
			}
		})
		c.channels.Register(clip)
		c.clip = clip
		c.mcs.SetClientCliprdr()
	}
	// Replay handlers that were registered before the layers existed.
	for _, p := range c.pending {
		c.dispatch(p.event, p.f)
	}
	c.pending = nil

	c.mcs.SetClientDesktop(uint16(width), uint16(height))

	c.sec.SetUser(user)
	c.sec.SetPwd(pwd)
	c.sec.SetDomain(domain)

	c.tpkt.SetFastPathListener(c.sec)
	c.sec.SetFastPathListener(c.pdu)
	c.sec.SetChannelSender(c.mcs)
	c.channels.SetChannelSender(c.sec)
	if c.setting == nil || !c.setting.NoFastPathInput {
		c.pdu.SetFastPathSender(c.tpkt)
	}

	c.x224.SetRequestedProtocol(requestedProtocol(c.setting))

	err = c.x224.Connect()
	if err != nil {
		return fmt.Errorf("client: the RDP connection failed: %w", err)
	}
	return nil
}

// SetClipboardText publishes text on the shared clipboard.
func (c *RdpClient) SetClipboardText(text string) error {
	if c.clip == nil {
		return fmt.Errorf("client: the clipboard channel is not enabled")
	}
	return c.clip.SetClipboardText(text)
}

// RequestClipboardText asks the server for its clipboard text.
func (c *RdpClient) RequestClipboardText() error {
	if c.clip == nil {
		return fmt.Errorf("client: the clipboard channel is not enabled")
	}
	return c.clip.RequestClipboardText()
}

// On registers a handler for an event. It may be called before Login, in which
// case handlers are buffered and replayed once the layer that produces them
// exists; this ordering is what lets a caller register handlers and then call
// Login without losing the "ready" event.
//
// Event names: "ready", "success", "error" (error), "close", "bitmap"
// ([]pdu.BitmapData), "surface-bits" (*pdu.SurfaceBitsCommand), "orders"
// ([]pdu.OrderPdu), "orders-frame" (image.Rectangle), "pointer"
// (*pdu.PointerDataPDU), "pointer-position" (*pdu.FastPathPointerPositionPDU),
// "clipboard-text" (string), and "gfx-frame"/"gfx-reset" from the graphics
// channel. f must have the type the event carries or the receive will panic.
func (c *RdpClient) On(event string, f interface{}) {
	if c == nil {
		return
	}
	if event == "error" {
		// Classified here rather than at each caller, so that a handler
		// registered with OnError sees the same distinction that Login
		// returns: a disconnect after the session started arrives only as an
		// event, and it is the one an operator most needs told apart from a
		// lost connection.
		if fn, ok := f.(func(error)); ok {
			f = func(e error) { fn(classifyError(e)) }
		}
	}
	if c.pdu == nil {
		// Registered before Login: buffer and replay once the layers exist.
		c.pending = append(c.pending, pendingEvent{event, f})
		return
	}
	c.dispatch(event, f)
}

// dispatch routes an event to the layer that produces it. EGFX events come
// from the graphics channel, everything else from the PDU layer.
func (c *RdpClient) dispatch(event string, f interface{}) {
	if strings.HasPrefix(event, "gfx-") {
		if c.gfx != nil {
			c.gfx.On(event, f)
		}
		return
	}
	c.pdu.On(event, f)
}

// scancodeFlags splits an 0xE0-prefixed extended scancode into the base
// scancode and the KBDFLAGS_EXTENDED flag required by MS-RDPBCGR.
func scancodeFlags(sc int) (uint16, uint16) {
	if sc&0xFF00 == 0xE000 {
		return uint16(sc & 0xFF), pdu.KBDFLAGS_EXTENDED
	}
	return uint16(sc), 0
}

// KeyUp releases sc. See Client.KeyUp for the scancode and extended key
// conventions. name is ignored.
func (c *RdpClient) KeyUp(sc int, name string) {
	if c == nil || c.pdu == nil {
		return
	}
	code, flags := scancodeFlags(sc)
	p := &pdu.ScancodeKeyEvent{}
	p.KeyCode = code
	p.KeyboardFlags = flags | pdu.KBDFLAGS_RELEASE
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
}

// KeyDown presses sc. See Client.KeyUp for the scancode and extended key
// conventions. name is ignored.
func (c *RdpClient) KeyDown(sc int, name string) {
	if c == nil || c.pdu == nil {
		return
	}
	code, flags := scancodeFlags(sc)
	p := &pdu.ScancodeKeyEvent{}
	p.KeyCode = code
	p.KeyboardFlags = flags
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
}

// MouseMove moves the pointer to (x, y) in screen pixels from the top left.
func (c *RdpClient) MouseMove(x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}
	p.PointerFlags |= pdu.PTRFLAGS_MOVE
	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

// MouseWheel turns the wheel by scroll notches at (x, y); positive is up.
func (c *RdpClient) MouseWheel(scroll, x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}
	p.PointerFlags |= pdu.PTRFLAGS_WHEEL | pdu.PTRFLAGS_MOVE
	rotation := scroll
	if rotation < 0 {
		p.PointerFlags |= pdu.PTRFLAGS_WHEEL_NEGATIVE
		rotation = -rotation
	}
	p.PointerFlags |= uint16(rotation) & pdu.WheelRotationMask
	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

// MouseUp releases button 0 (left), 1 (middle) or 2 (right) at (x, y).
func (c *RdpClient) MouseUp(button int, x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}

	switch button {
	case 0:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON1
	case 2:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON2
	case 1:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON3
	default:
		p.PointerFlags |= pdu.PTRFLAGS_MOVE
	}
	p.PointerFlags |= pdu.PTRFLAGS_MOVE

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

// MouseDown presses button 0 (left), 1 (middle) or 2 (right) at (x, y).
func (c *RdpClient) MouseDown(button int, x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}

	p.PointerFlags |= pdu.PTRFLAGS_DOWN | pdu.PTRFLAGS_MOVE

	switch button {
	case 0:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON1
	case 2:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON2
	case 1:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON3
	default:
		p.PointerFlags |= pdu.PTRFLAGS_MOVE
	}

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

// framebuffer returns the wrapper around this session's pixel buffer, or nil
// before Login has built it. The wrapper is made once, so the pointer stays
// valid across calls even though the buffer under it can be resized.
func (c *RdpClient) framebuffer() *Framebuffer {
	if c == nil {
		return nil
	}
	return c.fb
}

// requestResize asks the server to change the desktop size. See
// Client.RequestResize.
func (c *RdpClient) requestResize(w, h int) error {
	if c == nil || c.disp == nil {
		return errors.New("client: the display control channel is not enabled; set Setting.EnableDisplayControl")
	}
	return c.disp.RequestResize(w, h)
}

// onPointerPDU handles a slow-path pointer update: the pointer moved, its shape
// changed, the server named a system cursor, or the server referred back to a
// shape it already sent.
//
// The last of those is the reason a cache exists. The client advertises how many
// shapes it will keep, and after that the server stops resending them, so a
// client with nowhere to put them loses the pointer rather than drawing it
// wrongly: nothing arrives to draw.
func (c *RdpClient) onPointerPDU(d interface{}) {
	p, ok := d.(*pdu.PointerDataPDU)
	if !ok || p == nil {
		return
	}
	switch p.MessageType {
	case pdu.TS_PTRMSGTYPE_POSITION:
		c.pdu.Emit("cursor-pos", image.Point{X: int(p.XPos), Y: int(p.YPos)})
	case pdu.TS_PTRMSGTYPE_SYSTEM:
		c.onSystemPointer(p.SystemType)
	case pdu.TS_PTRMSGTYPE_COLOR:
		c.onPointerShape(&pdu.PointerShape{
			Width:    int(p.Width),
			Height:   int(p.Height),
			HotspotX: int(p.HotSpotX),
			HotspotY: int(p.HotSpotY),
			XorBpp:   p.XorBpp,
			Xor:      p.Data,
			And:      p.Mask,
		}, p.CacheIndex)
	case pdu.TS_PTRMSGTYPE_CACHED:
		c.onCachedPointer(p.CacheIndex)
	case pdu.TS_PTRMSGTYPE_POINTER:
		c.onPointerShape(&pdu.PointerShape{
			Width:    int(p.Width),
			Height:   int(p.Height),
			HotspotX: int(p.HotSpotX),
			HotspotY: int(p.HotSpotY),
			XorBpp:   p.XorBpp,
			Xor:      p.Data,
			And:      p.Mask,
		}, p.CacheIndex)
	}
}

// onFastPathPointer handles the fast-path pointer shape updates. They are the
// same four cases as the slow path, and modern Windows sends its cursors here
// rather than there: a 32bpp cursor arrives as FASTPATH_UPDATETYPE_LARGE_POINTER,
// which is the only one of them that states its bit depth.
func (c *RdpClient) onFastPathPointer(d interface{}) {
	f, ok := d.(*pdu.FastPathPointerPDU)
	if !ok || f == nil {
		return
	}
	switch f.UpdateType {
	case pdu.FASTPATH_UPDATETYPE_CACHED:
		c.onCachedPointer(f.CacheIndex)
	case pdu.FASTPATH_UPDATETYPE_POINTER:
		c.onPointerShape(&pdu.PointerShape{
			Width:    int(f.Width),
			Height:   int(f.Height),
			HotspotX: int(f.HotSpotX),
			HotspotY: int(f.HotSpotY),
			XorBpp:   int(f.Bpp) / 8,
			Xor:      f.Data,
			And:      f.Mask,
		}, f.CacheIndex)
	default:
		c.onPointerShape(&pdu.PointerShape{
			Width:    int(f.Width),
			Height:   int(f.Height),
			HotspotX: int(f.HotSpotX),
			HotspotY: int(f.HotSpotY),
			XorBpp:   int(f.Bpp) / 8,
			Xor:      f.Data,
			And:      f.Mask,
		}, f.CacheIndex)
	}
}

// onPointerShape records a shape and reports it, unless it is the shape already
// on screen.
func (c *RdpClient) onPointerShape(s *pdu.PointerShape, cacheIndex uint16) {
	if s == nil || s.Width <= 0 || s.Height <= 0 {
		return
	}
	if err := c.pointers.Put(cacheIndex, s); err != nil {
		glog.Warnf("client: %v", err)
	}
	if sameShape(c.lastShape, s) {
		return
	}
	cur := cursorFromShape(s)
	if cur == nil {
		glog.Warnf("client: pointer shape %dx%d at %d bytes per pixel could not be decoded",
			s.Width, s.Height, s.XorBpp)
		return
	}
	c.lastShape, c.haveSystem = s.Fill(), false
	c.pdu.Emit("cursor", cur)
}

// onCachedPointer reports a shape the server already sent. A reference to a slot
// that was never filled is reported rather than passed over: an empty cursor
// here is the pointer vanishing, and it would vanish silently.
func (c *RdpClient) onCachedPointer(index uint16) {
	s, ok := c.pointers.Get(index)
	if !ok {
		glog.Warnf("client: cached pointer %d was never sent by the server", index)
		return
	}
	if sameShape(c.lastShape, s) {
		return
	}
	cur := cursorFromShape(s)
	if cur == nil {
		glog.Warnf("client: cached pointer %d could not be decoded", index)
		return
	}
	c.lastShape, c.haveSystem = s.Fill(), false
	c.pdu.Emit("cursor", cur)
}

// onSystemPointer reports one of the server's built in cursors. It is reported
// rather than skipped so that a caller can substitute its own artwork; a caller
// that is never told keeps drawing the last shape it had, which is how a pointer
// ends up looking like an hourglass forever.
func (c *RdpClient) onSystemPointer(t uint32) {
	if c.haveSystem && c.lastSystem == t {
		return
	}
	c.haveSystem, c.lastSystem = true, t
	c.lastShape = nil
	c.pdu.Emit("cursor", &Cursor{System: true, SystemType: t})
}

// sameShape compares two pointer shapes by what they draw, so that a shape the
// server sends twice is only reported once.
func sameShape(a, b *pdu.PointerShape) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Width == b.Width && a.Height == b.Height &&
		a.HotspotX == b.HotspotX && a.HotspotY == b.HotspotY &&
		a.XorBpp == b.XorBpp &&
		bytes.Equal(a.Xor, b.Xor) && bytes.Equal(a.And, b.And)
}

// composite copies decoded bitmaps into the framebuffer and reports the merged
// area that changed. A bitmap that lands entirely outside the frame changes
// nothing and is dropped, which is what keeps a server that draws off screen
// from growing the dirty region to something the caller cannot use.
func (c *RdpClient) composite(bs []Bitmap) {
	if c == nil || c.screen == nil || len(bs) == 0 {
		return
	}
	screenW, screenH := c.screen.Size()
	// A server that starts drawing outside the frame it was told about has
	// changed the desktop size, which happens after a display control resize.
	// The buffer follows rather than clipping the new area away, and the
	// change is reported so a caller can resize whatever it is drawing into.
	wantW, wantH := screenW, screenH
	for _, b := range bs {
		if b.DestLeft+b.Width > wantW {
			wantW = b.DestLeft + b.Width
		}
		if b.DestTop+b.Height > wantH {
			wantH = b.DestTop + b.Height
		}
	}
	if wantW > screenW || wantH > screenH {
		c.resizeFramebuffer(wantW, wantH)
		screenW, screenH = c.screen.Size()
	}
	bounds := image.Rect(0, 0, screenW, screenH)
	var dirty []image.Rectangle
	for _, b := range bs {
		if b.Width <= 0 || b.Height <= 0 || len(b.Data) == 0 {
			continue
		}
		// The update gives the destination by its top left corner, and the
		// bitmap's own size says how far it reaches. DestRight and DestBottom
		// are inclusive on the wire, so they are deliberately not used here:
		// adding the size to the corner cannot be off by one.
		dst := image.Rect(b.DestLeft, b.DestTop, b.DestLeft+b.Width, b.DestTop+b.Height)
		dirty = addDirty(dirty, c.screen.Blit(b.Data, b.Width, b.Height, b.BitsPerPixel, dst), bounds)
	}
	if len(dirty) == 0 {
		return
	}
	c.pdu.Emit("frame", dirty)
}

// resizeFramebuffer changes the buffer's size and reports it. Everything is
// redrawn at the new size, since the old contents no longer describe the
// desktop.
func (c *RdpClient) resizeFramebuffer(w, h int) {
	if c == nil || c.screen == nil || w <= 0 || h <= 0 {
		return
	}
	curW, curH := c.screen.Size()
	if w == curW && h == curH {
		return
	}
	glog.Infof("client: desktop is now %dx%d", w, h)
	c.screen.Resize(w, h)
	c.pdu.Emit("resize", image.Point{X: w, Y: h})
	c.pdu.Emit("frame", []image.Rectangle{image.Rect(0, 0, w, h)})
}

// compositeSurfaces copies the surfaces of one graphics channel frame into the
// framebuffer, each at the position it was mapped to, resampled if the server
// asked for a size other than the surface's own.
func (c *RdpClient) compositeSurfaces(surfaces []*rdpgfx.Surface) {
	if c == nil || c.screen == nil || len(surfaces) == 0 {
		return
	}
	screenW, screenH := c.screen.Size()
	bounds := image.Rect(0, 0, screenW, screenH)
	var dirty []image.Rectangle
	for _, s := range surfaces {
		pixels, w, h := s.CompositePixels()
		if w <= 0 || h <= 0 || len(pixels) == 0 {
			continue
		}
		x, y := s.Origin()
		// The graphics channel delivers BGRA, which is the framebuffer's own
		// format, so nothing is converted here.
		dirty = addDirty(dirty, c.screen.Blit(pixels, w, h, 4, image.Rect(x, y, x+w, y+h)), bounds)
	}
	if len(dirty) == 0 {
		return
	}
	c.pdu.Emit("frame", dirty)
}

// Close closes the underlying transport. It is safe on a partially built client
// and more than once; input methods become no-ops afterwards.
func (c *RdpClient) Close() {
	if c != nil && c.tpkt != nil {
		c.tpkt.Close()
	}
}
