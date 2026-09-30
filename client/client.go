// client.go
package client

import (
	"image"

	"context"
	"errors"
	"fmt"
	"github.com/adfnekc/grdp/orders"
	"log"
	"os"
	"time"

	"github.com/adfnekc/grdp/codec"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/plugin/cliprdr"
	"github.com/adfnekc/grdp/plugin/rdpgfx"
	"github.com/adfnekc/grdp/protocol/pdu"
	"github.com/adfnekc/grdp/protocol/rfb"
)

// DefaultLoginTimeout bounds how long Login waits for the RDP session to
// become ready after the transport handshake succeeds.
const DefaultLoginTimeout = 30 * time.Second

// Clipboard directions accepted by the (currently inert) Setting.SetClipboard.
const (
	CLIP_OFF = 0
	CLIP_IN  = 0x1
	CLIP_OUT = 0x2
)

// Transport kinds accepted by NewClient: TC_RDP for RDP, TC_VNC for VNC (RFB).
const (
	TC_RDP = 0
	TC_VNC = 1
)

// Control is the protocol-independent session surface Client delegates to. It
// is implemented by RdpClient and VncClient; their protocols differ but the
// input, clipboard and event methods here do not.

// Control is the small surface both clients implement, so that Client can hold
// either one. Callers normally use Client rather than this.
type Control interface {
	Login(host, user, passwd string, width, height int) error
	KeyUp(sc int, name string)
	KeyDown(sc int, name string)
	UnicodeKeyDown(r rune)
	UnicodeKeyUp(r rune)
	MouseMove(x, y int)
	MouseWheel(scroll, x, y int)
	MouseUp(button int, x, y int)
	MouseDown(button int, x, y int)
	On(event string, msg interface{})
	SetClipboardText(text string) error
	RequestClipboardText() error
	Close()
}

func init() {
	glog.SetLevel(glog.INFO)
	logger := log.New(os.Stdout, "", 0)
	glog.SetLogger(logger)
}

// Client is one session with an RDP or VNC server. It picks a protocol-specific
// implementation from the type given to NewClient (RdpClient or VncClient) and
// exposes one API for input, events, the clipboard and the framebuffer.
//
// A Client is safe for concurrent use. Build one with NewClient; the zero value
// is not usable. Nothing is sent until Login is called.
type Client struct {
	host    string
	user    string
	passwd  string
	ctl     Control
	tc      int
	setting *Setting
}

func init() {
	logger := log.New(os.Stdout, "", 0)
	glog.SetLogger(logger)
	glog.SetLevel(glog.NONE)
}

// NewClient returns an unconnected session for host, which must be
// "address:port". t selects the transport: TC_RDP for RDP, TC_VNC for VNC. A nil
// s is replaced by NewSetting, so the common call is
// NewClient(host, user, pass, TC_RDP, nil).
//
// No connection is made here. Call Login to connect, and Close when done.
func NewClient(host, user, passwd string, t int, s *Setting) *Client {
	if s == nil {
		s = NewSetting()
	}
	c := &Client{
		host:    host,
		user:    user,
		passwd:  passwd,
		tc:      t,
		setting: s,
	}

	switch t {
	case TC_VNC:
		c.ctl = newVncClient(s)
	default:
		c.ctl = newRdpClient(s)
	}

	s.SetLogLevel()
	return c
}

// Login connects and waits until the session is ready (or fails/times out).
func (c *Client) Login() error {
	timeout := c.setting.Timeout
	if timeout <= 0 {
		timeout = DefaultLoginTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.LoginContext(ctx)
}

// LoginContext is Login with an explicit context. It returns only once the
// server has completed the connection sequence and the session is ready, or
// when the handshake fails, the connection is closed, or ctx expires.
func (c *Client) LoginContext(ctx context.Context) error {
	// The VNC client does not emit a "ready" event; its Login is synchronous.
	if c.tc == TC_VNC {
		return c.ctl.Login(c.host, c.user, c.passwd, c.setting.Width, c.setting.Height)
	}

	ready := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	closed := make(chan struct{}, 1)

	// Register before the handshake so no event can be missed.
	c.ctl.On("ready", func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
	c.ctl.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})
	c.ctl.On("close", func() {
		select {
		case closed <- struct{}{}:
		default:
		}
	})

	if err := c.ctl.Login(c.host, c.user, c.passwd, c.setting.Width, c.setting.Height); err != nil {
		return err
	}

	select {
	case <-ready:
		return nil
	case err := <-errCh:
		return err
	case <-closed:
		return errors.New("client: connection closed before the session became ready")
	case <-ctx.Done():
		return fmt.Errorf("client: session not ready: %w", ctx.Err())
	}
}

// KeyUp releases the key whose PC set 1 scancode is sc. A value in
// 0xE000..0xE0FF is sent as an extended key: the low byte is the scancode and
// the 0xE0 prefix becomes the extended flag. name is accepted for source
// compatibility and ignored; the scancode is what reaches the server.
func (c *Client) KeyUp(sc int, name string) {
	c.ctl.KeyUp(sc, name)
}

// KeyDown presses the key whose PC set 1 scancode is sc, with the same extended
// key handling as KeyUp. name is ignored.
func (c *Client) KeyDown(sc int, name string) {
	c.ctl.KeyDown(sc, name)
}

// MouseMove moves the pointer to (x, y) in screen pixels from the top left.
func (c *Client) MouseMove(x, y int) {
	c.ctl.MouseMove(x, y)
}

// MouseWheel turns the wheel by scroll notches at (x, y); positive is up (away
// from the user) and the magnitude is clamped to the protocol's 9 bit field.
func (c *Client) MouseWheel(scroll, x, y int) {
	c.ctl.MouseWheel(scroll, x, y)
}

// MouseUp releases button 0 (left), 1 (middle) or 2 (right) at (x, y). Any
// other value only moves the pointer. The numbering is the sender's, not the
// protocol's.
func (c *Client) MouseUp(button, x, y int) {
	c.ctl.MouseUp(button, x, y)
}

// MouseDown presses button 0 (left), 1 (middle) or 2 (right) at (x, y), with
// the same numbering as MouseUp.
func (c *Client) MouseDown(button, x, y int) {
	c.ctl.MouseDown(button, x, y)
}

// Close ends the session and closes the underlying connection. It is safe to
// call more than once and on a client that never connected, and it is safe to
// call concurrently with input methods, which become no-ops once the connection
// is gone.
func (c *Client) Close() {
	if c != nil && c.ctl != nil {
		c.ctl.Close()
	}
}

// OnError registers f to be called with a handshake or transport error. Handlers
// may be registered before Login; they are kept and delivered once the session
// layers exist.
func (c *Client) OnError(f func(e error)) {
	c.ctl.On("error", f)
}

// OnClose registers f to be called when the connection to the server is gone.
// Like the other handlers it may be registered before Login.
func (c *Client) OnClose(f func()) {
	c.ctl.On("close", f)
}

// OnPointerPosition reports the server-side pointer position (fast-path
// FASTPATH_UPDATETYPE_PTR_POSITION). It is useful to confirm that mouse input
// reached the remote session.
func (c *Client) OnPointerPosition(f func(x, y int)) {
	c.ctl.On("pointer-position", func(data interface{}) {
		p := data.(*pdu.FastPathPointerPositionPDU)
		f(int(p.XPos), int(p.YPos))
	})
}

// OnPointer reports slow-path pointer updates (PDUTYPE2_POINTER), which carry
// either a new position or a pointer shape.
func (c *Client) OnPointer(f func(p *pdu.PointerDataPDU)) {
	c.ctl.On("pointer", func(data interface{}) {
		f(data.(*pdu.PointerDataPDU))
	})
}

// Framebuffer returns the buffer every decoded frame lands in, or nil before
// Login has been called.
//
// It is the one place to read pixels from, whichever way the server is drawing:
// bitmap updates, drawing orders and graphics channel surface bits all composite
// into it. Use OnFrame to learn what changed rather than assuming the whole
// buffer did.
//
// This is the RDP path. The VNC client does not composite into a framebuffer,
// because an RFB server chooses the pixel format per connection and the buffer
// here is defined as BGRA; a VNC session therefore returns nil and has to be
// driven from OnBitmap instead.
func (c *Client) Framebuffer() *Framebuffer {
	if r, ok := c.ctl.(*RdpClient); ok {
		return r.framebuffer()
	}
	return nil
}

// OnFrame registers f to be called once per batch of updates, after every
// rectangle in the batch has been composited, with the areas that changed.
//
// The rectangles have already been merged and clipped to Framebuffer().Bounds(),
// so f can encode exactly those regions without checking them. Neighbouring and
// overlapping rectangles arrive as one, and when the merged area covers most of
// the frame it arrives as the whole frame. The slice is only valid for the
// duration of the call.
//
// f runs on the goroutine that decodes the session's data, so anything that can
// block belongs on another one. The pixels it reads from Framebuffer().Pix()
// stay as they are until the next call.
func (c *Client) OnFrame(f func(dirty []image.Rectangle)) {
	c.ctl.On("frame", func(data interface{}) {
		f(data.([]image.Rectangle))
	})
}

// Screen returns the raw framebuffer that drawing orders are rendered into, or
// nil when Setting.EnableOrders was not set.
//
// Deprecated: use Framebuffer, which exists whether or not orders are enabled
// and is the only supported way to read pixels. This remains for callers that
// need the orders-specific machinery (the caches and Unsupported counts).
func (c *Client) Screen() *orders.Screen {
	if r, ok := c.ctl.(*RdpClient); ok {
		return r.screen
	}
	return nil
}

// OnOrdersFrame reports that a batch of drawing orders changed the screen,
// giving the area that changed, merged into a single rectangle.
//
// Deprecated: use OnFrame, which reports every drawing path and does not have to
// merge a batch into one rectangle to fit the signature.
func (c *Client) OnOrdersFrame(f func(dirty image.Rectangle)) {
	c.ctl.On("orders-frame", func(data interface{}) {
		f(data.(image.Rectangle))
	})
}

// OnSurfaceFrame reports each completed EGFX frame and the surfaces it
// touched. The pixel data lives on the surfaces, so callers that need it
// should copy what they need before returning.
func (c *Client) OnSurfaceFrame(f func(frameID uint32, surfaces []*rdpgfx.Surface)) {
	c.ctl.On("gfx-frame", func(data interface{}) {
		v := data.(rdpgfx.Frame)
		f(v.ID, v.Surfaces)
	})
}

// OnSurfaceReset reports the desktop size an EGFX server will composite into,
// which also means any previously known surfaces are gone.
func (c *Client) OnSurfaceReset(f func(width, height int)) {
	c.ctl.On("gfx-reset", func(data interface{}) {
		v := data.(rdpgfx.Reset)
		f(v.Width, v.Height)
	})
}

// RequestResize asks the server to change the desktop to w by h pixels.
//
// It reports an error when the session cannot do it: the Display Control channel
// has to be opened at connect time, with Setting.EnableDisplayControl, because
// opening a dynamic channel changes the connect sequence. It also reports an
// error for a size the server will not accept, rather than clamping it, so that
// a caller is not told its request succeeded when the size was changed.
//
// The server applies the change on its own schedule, so the size is not changed
// by the time this returns. OnResize says when it has, and Framebuffer follows.
func (c *Client) RequestResize(w, h int) error {
	r, ok := c.ctl.(*RdpClient)
	if !ok {
		return errors.New("client: resizing is an RDP feature; this session has no display control channel")
	}
	return r.requestResize(w, h)
}

// OnResize registers f to be called when the desktop size changes.
//
// It is driven by what the server draws, not by what was asked for: the server
// decides whether a resize is allowed and when to apply it, and the first update
// at a new size is the confirmation. A shrink is not reported this way on the
// bitmap path, because nothing in a bitmap update says the desktop got smaller;
// the graphics channel does say so, and resizes both ways.
func (c *Client) OnResize(f func(w, h int)) {
	c.ctl.On("resize", func(data interface{}) {
		p := data.(image.Point)
		f(p.X, p.Y)
	})
}

// Files returns the clipboard's file transfer surface, or nil when the
// clipboard channel is not enabled or the session is not RDP.
//
// It is how the application supplies the files it shares (SetFileProvider) and
// receives the files the server offers (OnRemoteFiles, ReadRemoteFile,
// ClearRemoteFiles). A file transfer is not text: the protocol hands the
// application a manifest and then asks it for ranges of bytes, so the
// application decides where the bytes come from and go, and the library never
// touches a path the remote end named. Nothing file shaped is advertised on the
// clipboard until a provider is set, because a format the client cannot produce
// data for leaves the pasting side waiting.
func (c *Client) Files() *cliprdr.CliprdrClient {
	if r, ok := c.ctl.(*RdpClient); ok {
		return r.clip
	}
	return nil
}

// OnClipboardText reports text the server has put on the clipboard. It only
// fires when the clipboard channel is enabled.
func (c *Client) OnClipboardText(f func(text string)) {
	c.ctl.On("clipboard-text", func(data interface{}) {
		f(data.(string))
	})
}

// SetClipboardText publishes text on the shared clipboard, so a paste on the
// server pastes it. It only does anything when the clipboard channel is
// enabled.
func (c *Client) SetClipboardText(text string) error {
	return c.ctl.SetClipboardText(text)
}

// RequestClipboardText asks the server for the clipboard text it last
// announced. OnClipboardText normally triggers this by itself, but a server
// can announce a format before it is able to serve it, in which case an
// explicit request made later is what gets the data.
func (c *Client) RequestClipboardText() error {
	return c.ctl.RequestClipboardText()
}

// OnSuccess registers f to be called when the server accepts the credentials,
// before the session is ready to draw.
func (c *Client) OnSuccess(f func()) {
	c.ctl.On("success", f)
}

// OnReady registers f to be called once the session is negotiated and the
// server is about to send its first update. It fires before any bitmap or
// order callback.
func (c *Client) OnReady(f func()) {
	c.ctl.On("ready", f)
}

// bitmapsFromEvent converts a bitmap update event into the public Bitmap shape,
// decompressing each RDP rectangle's stream on the way. It is shared by OnBitmap
// and by the framebuffer composite, so that the two cannot disagree about what a
// bitmap update contains.
func bitmapsFromEvent(data interface{}, tc int) []Bitmap {
	bs := make([]Bitmap, 0, 50)
	if tc == TC_VNC {
		br := data.(*rfb.BitRect)
		for _, v := range br.Rects {
			b := Bitmap{int(v.Rect.X), int(v.Rect.Y), int(v.Rect.X + v.Rect.Width), int(v.Rect.Y + v.Rect.Height),
				int(v.Rect.Width), int(v.Rect.Height),
				Bpp(uint16(br.Pf.BitsPerPixel)), false, v.Data}
			bs = append(bs, b)
		}
		return bs
	}
	for _, v := range data.([]pdu.BitmapData) {
		IsCompress := v.IsCompress()
		stream := v.BitmapDataStream
		if IsCompress {
			stream = bitmapDecompress(&v)
			IsCompress = false
		}

		b := Bitmap{int(v.DestLeft), int(v.DestTop), int(v.DestRight), int(v.DestBottom),
			int(v.Width), int(v.Height), Bpp(v.BitsPerPixel), IsCompress, stream}
		bs = append(bs, b)
	}
	return bs
}

// surfaceBitmaps converts a surface bits command, whose codec may be NSCodec or
// RemoteFX, into the same Bitmap shape so that consumers only ever see one
// representation. It returns nil when the command cannot be decoded, having
// logged why.
func surfaceBitmaps(cmd *pdu.SurfaceBitsCommand) []Bitmap {
	b := cmd.Bitmap
	w, h := int(b.Width), int(b.Height)
	if w <= 0 || h <= 0 {
		return nil
	}
	pixels, err := codec.Decompress(b.CodecID, b.BitmapData, w, h, int(b.Bpp))
	if err != nil {
		glog.Error("surface bits: ", err)
		return nil
	}
	bpp := int(b.Bpp) / 8
	if b.CodecID != codec.CodecIDNone {
		// Every bitmap codec in use here decodes to 32bpp BGRA.
		bpp = 4
	}
	return []Bitmap{{
		DestLeft:     int(cmd.DestLeft),
		DestTop:      int(cmd.DestTop),
		DestRight:    int(cmd.DestRight),
		DestBottom:   int(cmd.DestBottom),
		Width:        w,
		Height:       h,
		BitsPerPixel: bpp,
		Data:         pixels,
	}}
}

// OnBitmap registers f to be called with each bitmap update, already expanded
// and converted to BGRA at 32 bpp where the source was compressed. f may see
// the same underlying data reused by the next update, so copy anything it keeps.
// For VNC the Bitmap rectangles carry the VNC pixel format instead.
//
// Deprecated: use OnFrame and Framebuffer, whose pixels survive the callback.
// This remains for callers that want the raw update, and it is still what an RFB
// session has to use.
func (c *Client) OnBitmap(f func([]Bitmap)) {
	c.ctl.On("bitmap", func(data interface{}) {
		f(bitmapsFromEvent(data, c.tc))
	})

	// Surface bits commands carry extended bitmap data whose codecID selects
	// a bitmap codec (NSCodec, RemoteFX). Decode them into the same Bitmap
	// shape so consumers only deal with one representation.
	c.ctl.On("surface-bits", func(data interface{}) {
		if bs := surfaceBitmaps(data.(*pdu.SurfaceBitsCommand)); bs != nil {
			f(bs)
		}
	})
}

// Bitmap is one decoded rectangle of the remote screen.
//
// The rectangle is [DestLeft, DestRight) x [DestTop, DestBottom) on the desktop;
// Width and Height are the size of the source data and are usually equal to the
// destination size. Data is top-down BGRA (B first) at BitsPerPixel/8 bytes per
// pixel when IsCompress is false, which is how OnBitmap delivers RDP updates.
type Bitmap struct {
	DestLeft     int    `json:"destLeft"`
	DestTop      int    `json:"destTop"`
	DestRight    int    `json:"destRight"`
	DestBottom   int    `json:"destBottom"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	BitsPerPixel int    `json:"bitsPerPixel"`
	IsCompress   bool   `json:"isCompress"`
	Data         []byte `json:"data"`
}

// Bpp converts a bit depth to a byte count by dividing by eight: 16 to 2, 32 to
// 4. It truncates, so 15 bpp gives 1 rather than 2.
func Bpp(bp uint16) int {
	return int(bp / 8)
}

// Setting configures a session. NewSetting returns one with usable defaults;
// the fields below are only read when Login is called, so they may be changed
// until then.
type Setting struct {
	// Width and Height are the desktop size requested from the server, in
	// pixels. They are sent in the MCS connect sequence and must be positive;
	// NewSetting uses 1024x768.
	Width  int
	Height int

	// Protocol selects the security path: "tls" (the default) or "ssl" for
	// TLS, "nla", "hybrid" or "credssp" for CredSSP, and "rdp" or "standard"
	// for Standard RDP Security with no TLS. The empty string means TLS, and an
	// unrecognised value falls back to TLS with a warning rather than failing.
	Protocol string

	// Timeout bounds the whole of Login, from dial to ready. Zero or negative
	// means DefaultLoginTimeout (30s).
	Timeout time.Duration

	// LogLevel is applied to the package-global glog logger by NewClient. The
	// default is glog.INFO; glog.NONE silences the library.
	LogLevel glog.LEVEL

	// NoFastPathInput forces keyboard/mouse input over the slow path even
	// when the server advertised fast-path input. Useful for debugging.
	NoFastPathInput bool

	// EnableEGFX opens the dynamic virtual channel needed for the RDP
	// Graphics Pipeline Extension, so the server can composite surfaces and
	// send them as compressed bitmaps instead of bitmap updates.
	//
	// This is off by default: it has not been exercised against a server that
	// speaks EGFX, and enabling it changes how the server draws the session.
	EnableEGFX bool

	// EnableClipboard opens the clipboard channel so text can be exchanged
	// with the server. Use Client.OnClipboardText and Client.SetClipboardText
	// to bridge it to the local clipboard.
	EnableClipboard bool

	// EnableDisplayControl opens the Display Control dynamic virtual channel, so
	// that RequestResize can ask the server to change the desktop size while the
	// session runs. It is off by default for the same reason as EGFX: opening a
	// dynamic channel changes the connect sequence, and a caller that is not
	// going to resize anything should not have its negotiation altered.
	EnableDisplayControl bool

	// EnableOrders advertises support for the MEMBLT drawing order and renders
	// the drawing orders that follow, into a framebuffer reachable through
	// Client.Screen.
	//
	// It is off by default because it changes how the server draws the session
	// and it cannot mix: once MEMBLT is advertised the server stops sending
	// bitmap updates and puts everything through the bitmap cache instead. With
	// it off the session is drawn from bitmap updates, which is correct, just
	// larger on the wire.
	EnableOrders bool
}

// NewSetting returns a Setting with the default desktop size (1024x768), the
// default login timeout and INFO logging.
func NewSetting() *Setting {
	return &Setting{
		Width:    1024,
		Height:   768,
		Timeout:  DefaultLoginTimeout,
		LogLevel: glog.INFO,
	}
}

// SetLogLevel pushes LogLevel into the package-global glog logger. NewClient
// already does this, so it only needs calling if the level is changed after the
// client is built.
func (s *Setting) SetLogLevel() {
	glog.SetLevel(s.LogLevel)
}

// SetRequestedProtocol is inert: it does nothing. Set Setting.Protocol instead,
// which is read when Login negotiates the security path.
func (s *Setting) SetRequestedProtocol(p uint32) {}

// SetClipboard is inert: it does nothing. Use Setting.EnableClipboard to open
// the clipboard channel.
func (s *Setting) SetClipboard(c int) {}
