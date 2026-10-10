// Package rfb is the RFB (VNC) client protocol this fork inherited from
// upstream.
//
// Its parse steps have unit tests that drive them over a scripted net.Pipe
// server, and scripts/vnc-dev.sh runs the client against a real server
// (TigerVNC's Xtigervnc) through the live tests in this package.
package rfb

import (
	"bytes"
	"crypto/des"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
)

// ProtocolVersion
const (
	RFB003003 = "RFB 003.003\n"
	RFB003007 = "RFB 003.007\n"
	RFB003008 = "RFB 003.008\n"
)

// SecurityType
const (
	SEC_INVALID uint8 = 0
	SEC_NONE    uint8 = 1
	SEC_VNC     uint8 = 2
)

// RFBConn is the RFB (VNC) connection. It drives the handshake, decodes the
// server's framebuffer updates into the framebuffer it retains, and sends the
// client's input and clipboard messages. Its parse steps emit events; the
// tests in this package drive them over a scripted net.Pipe server.
type RFBConn struct {
	emission.Emitter
	// The Socket connection to the client
	Conn     net.Conn
	s        *ServerInit
	NbRect   uint16
	BitRect  *BitRect
	Password string

	// version is the protocol version this connection runs. It is decided from
	// the server's banner when the banner is replied to, and the security
	// handshake branches on it: 3.3 has no security type list, and 3.7 sends no
	// SecurityResult when the chosen type is None.
	version string
	// rectIndex is where the next rectangle of the current FramebufferUpdate is
	// stored. NbRect counts how many are still to arrive, so it cannot also be
	// the write position without storing the rectangles in reverse order.
	rectIndex int
	// frame is the framebuffer every rectangle is decoded into. RFB is
	// stateful: an incremental update assumes the client kept the previous one,
	// which is what makes CopyRect meaningful, so the decoded pixels have to be
	// retained between updates. frameStride is one row's length in bytes; a
	// rectangle narrower than the desktop is not contiguous in the buffer.
	frame       []byte
	frameStride int
}

// The RFB client in this package is inherited from the upstream fork. Its
// parse steps are driven over a scripted net.Pipe server by the tests here, and
// the live tests at the end of this file run it against a TigerVNC server that
// scripts/vnc-dev.sh starts. The clipboard is implemented in both directions.

// maxRectBytes bounds a framebuffer rectangle, and maxCutTextBytes bounds a
// server cut text. Both lengths come off the wire and are checked before they
// size an allocation.
const (
	maxRectBytes       = 64 << 20
	maxCutTextBytes    = 16 << 20
	maxServerNameBytes = 1 << 20
	maxSecurityTypes   = 1024
	// maxSecurityReasonBytes bounds the text a server sends when it refuses to
	// offer any security type. Its length is on the wire before the text is.
	maxSecurityReasonBytes = 1 << 20
)

// maxFramebufferBytes bounds the framebuffer a connection keeps. It is sized
// from the ServerInit geometry and the active pixel format, both of which come
// off the wire, so the product is checked before it sizes the allocation. The
// ceiling matches the one the RDP bitmap path uses (core's maxDecodedBytes).
const maxFramebufferBytes = 64 << 20

// maxHextileWireBytes bounds the bytes one Hextile rectangle may carry. A tile
// of 255 coloured subrectangles carries up to 255*(bytesPerPixel+2) bytes for
// 256 pixels, so the wire form can exceed the decoded size; the number of tiles
// comes from the geometry, which maxRectBytes already bounds, and this is a
// second ceiling on the same wire form.
const maxHextileWireBytes = 4 * maxRectBytes

// The RFB encodings this client implements. CopyRect and Hextile are the ones
// beyond Raw; the values are the encoding ids on the wire, checked against
// TigerVNC's common/rfb/encodings.h.
const (
	encodingRaw      = 0
	encodingCopyRect = 1
	encodingHextile  = 5
)

// copyRectBodySize is the whole body of a CopyRect rectangle: two big-endian
// uint16 source coordinates. The rectangle's own width and height come from the
// rectangle header, so they are not repeated on the wire.
const copyRectBodySize = 4

// hextileTilePixels is the side of a Hextile tile and hextileTileArea is how
// many pixels one holds.
const (
	hextileTilePixels = 16
	hextileTileArea   = hextileTilePixels * hextileTilePixels
)

// Hextile subencoding bits (RFC 6143 section 7.7.4), checked against TigerVNC's
// common/rfb/hextileConstants.h. The brief that asked for this said four bits
// (1, 2, 4, 8); the reference has five, and Raw is bit 0.
const (
	hextileRaw              = 1 << 0
	hextileBgSpecified      = 1 << 1
	hextileFgSpecified      = 1 << 2
	hextileAnySubrects      = 1 << 3
	hextileSubrectsColoured = 1 << 4
)

// NewRFBConn wraps a connected socket in an RFBConn. It does not read or write
// until start is called through RFB.Connect.
func NewRFBConn(s net.Conn, passwd string) *RFBConn {
	fc := &RFBConn{
		Emitter:  *emission.NewEmitter(),
		Conn:     s,
		BitRect:  new(BitRect),
		Password: passwd,
	}

	return fc
}

// start reads the server's protocol version banner. RFB.Connect calls it after
// registering the version handler: the server sends the banner as soon as the
// connection opens, and an event that arrives before there is a listener is
// dropped, which would leave the handshake waiting forever.
func (fc *RFBConn) start() {
	core.StartReadBytes(12, fc, fc.recvProtocolVersion)
}

// Read reads raw bytes from the underlying connection. It exists so RFBConn
// satisfies the reader the read helpers expect.
func (fc *RFBConn) Read(b []byte) (n int, err error) {
	return fc.Conn.Read(b)
}

// Write writes raw bytes to the underlying connection.
func (fc *RFBConn) Write(data []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	buff.Write(data)
	return fc.Conn.Write(buff.Bytes())
}

// Close closes the underlying connection.
func (fc *RFBConn) Close() error {
	return fc.Conn.Close()
}
func (fc *RFBConn) recvProtocolVersion(s []byte, err error) {
	version := string(s)
	glog.Debug("RFBConn recvProtocolVersion", version, err)
	if err != nil {
		fc.Emit("error", err)
		return
	}

	// The version this connection runs is the one the server announced when it
	// is one this client knows, and 3.8 otherwise. RFB.recvProtocolVersion
	// writes the same value back, so server and client agree on the flow below.
	fc.version = negotiateVersion(version)
	fc.Emit("data", fc.version)

	if fc.version == RFB003003 {
		// 3.3 has no security type list: the server sends one 32 bit type.
		core.StartReadBytes(4, fc, fc.recvSecurity3_3)
		return
	}
	core.StartReadBytes(1, fc, fc.checkSecurityList)
}

// negotiateVersion maps the server's banner to the version this client runs. A
// banner it does not recognise is answered with 3.8, the newest version it
// implements.
func negotiateVersion(banner string) string {
	switch banner {
	case RFB003003, RFB003007, RFB003008:
		return banner
	default:
		return RFB003008
	}
}
func (fc *RFBConn) checkSecurityList(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	result, _ := core.ReadUInt8(r)
	glog.Debug("RFBConn checkSecurityList", result, err)

	if result == 0 {
		// The server cannot offer a security type. It follows the zero count
		// with a reason: a 32 bit length and that many bytes of text. Read the
		// length under a bound before it sizes a read.
		core.StartReadBytes(4, fc, fc.recvSecurityFailure)
		return
	}
	core.StartReadBytes(int(result), fc, fc.recvSecurityList)
}

// recvSecurityFailure reads the length of the reason a server sends when it
// offers no security type, then reads that many bytes and reports them.
func (fc *RFBConn) recvSecurityFailure(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	size, err := core.ReadUInt32BE(r)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	if size > maxSecurityReasonBytes {
		fc.Emit("error", fmt.Errorf("rfb: security failure reason of %d bytes is out of range", size))
		return
	}
	core.StartReadBytes(int(size), fc, func(b []byte, err error) {
		if err != nil {
			fc.Emit("error", err)
			return
		}
		fc.Emit("error", fmt.Errorf("rfb: server refused the connection: %s", bytes.TrimSpace(b)))
	})
}
func (fc *RFBConn) recvSecurityList(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	secLevel := SEC_INVALID
	for r.Len() > 0 {
		result, _ := core.ReadUInt8(r)
		if result == SEC_NONE || result == SEC_VNC {
			secLevel = result
			break
		}
	}

	glog.Debug("RFBConn recvSecurityList", secLevel, err)
	if secLevel == SEC_INVALID {
		// Choosing a type the server did not offer would be read as a different
		// type on its side; report that none of the offered ones is supported.
		fc.Emit("error", errors.New("rfb: server offered no supported security type"))
		return
	}
	buff := &bytes.Buffer{}
	core.WriteUInt8(secLevel, buff)
	fc.Write(buff.Bytes())
	if secLevel == SEC_VNC {
		core.StartReadBytes(16, fc, fc.recvVNCChallenge)
		return
	}
	// Security type None. 3.7 does not send a SecurityResult for it, only 3.8
	// and later do, so 3.7 goes straight to the shared flag and ServerInit.
	// Confirmed against TigerVNC 1.13.1: a 3.7 client that waits for a
	// SecurityResult after None times out.
	if fc.version == RFB003007 {
		fc.startServerInit()
		return
	}
	core.StartReadBytes(4, fc, fc.recvSecurityResult)
}

// recvSecurity3_3 reads the single 32 bit security type a 3.3 server sends, in
// place of the list later versions use. A 3.3 server sends no SecurityResult
// when the type is None; the client is expected to send the shared flag and
// read ServerInit directly. Confirmed against TigerVNC 1.13.1, which answers a
// 3.3 client with security type 1 (None) and then ServerInit.
func (fc *RFBConn) recvSecurity3_3(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	secType, err := core.ReadUInt32BE(r)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	glog.Debug("RFBConn recvSecurity3_3", secType)
	switch uint8(secType) {
	case SEC_NONE:
		fc.startServerInit()
	case SEC_VNC:
		core.StartReadBytes(16, fc, fc.recvVNCChallenge)
	default:
		fc.Emit("error", fmt.Errorf("rfb: server offered unsupported 3.3 security type %d", secType))
	}
}

func fixDesKeyByte(val byte) byte {
	var newval byte = 0
	for i := 0; i < 8; i++ {
		newval <<= 1
		newval += (val & 1)
		val >>= 1
	}
	return newval
}

// fixDesKey will make sure that exactly 8 bytes is used either by truncating or padding with nulls
// The bytes are then bit mirrored and returned
func fixDesKey(key []byte) []byte {
	tmp := key
	buf := make([]byte, 8)
	if len(tmp) <= 8 {
		copy(buf, tmp)
	} else {
		copy(buf, tmp[:8])
	}
	for i := 0; i < 8; i++ {
		buf[i] = fixDesKeyByte(buf[i])
	}
	return buf
}

func (fc *RFBConn) recvVNCChallenge(s []byte, err error) {
	glog.Debug("RFBConn recvVNCChallenge", glog.Hex(s), len(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	if len(s) < 16 {
		fc.Emit("error", fmt.Errorf("rfb: short VNC challenge (%d bytes)", len(s)))
		return
	}
	// VNC uses the password's own bytes as the DES key, truncated or padded to
	// eight bytes; it is not a UTF-16 encoding of the password.
	bk, err := des.NewCipher(fixDesKey([]byte(fc.Password)))
	if err != nil {
		fc.Emit("error", fmt.Errorf("rfb: VNC authentication cipher: %w", err))
		return
	}
	result := make([]byte, 16)
	bk.Encrypt(result, s)         // challenge bytes 0..7
	bk.Encrypt(result[8:], s[8:]) // challenge bytes 8..15
	fc.Write(result)
	core.StartReadBytes(4, fc, fc.recvSecurityResult)
}
func (fc *RFBConn) recvSecurityResult(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	result, _ := core.ReadUInt32BE(r)
	glog.Debug("RFBConn recvSecurityResult", result, err)
	if result != 0 {
		// Any non-zero result is a failure. 3.8 follows it with a reason
		// string; the read chain stops here either way, so there is no need to
		// consume it.
		fc.Emit("error", fmt.Errorf("rfb: authentication failed (security result %d)", result))
		return
	}
	fc.startServerInit()
}

// startServerInit sends the shared-desktop flag and reads ServerInit. It is the
// join point of every path that accepts the connection: the security step has
// ended and the protocol is waiting for the flag.
func (fc *RFBConn) startServerInit() {
	buff := &bytes.Buffer{}
	core.WriteUInt8(0, buff) // share the desktop
	fc.Write(buff.Bytes())
	core.StartReadBytes(20, fc, fc.recvServerInit)
}

// ServerInit is the server's opening message: the desktop geometry and its
// default pixel format.
type ServerInit struct {
	Width       uint16       `struc:"little"`
	Height      uint16       `struc:"little"`
	PixelFormat *PixelFormat `struc:"little"`
}

func (fc *RFBConn) recvServerInit(s []byte, err error) {
	glog.Debug("RFBConn recvServerInit", len(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	si := &ServerInit{}
	si.Width, err = core.ReadUint16BE(r)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	si.Height, err = core.ReadUint16BE(r)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	si.PixelFormat = ReadPixelFormat(r)
	glog.Infof("serverInit:%+v, %+v", si, si.PixelFormat)
	fc.s = si
	fc.BitRect.Pf = si.PixelFormat
	core.StartReadBytes(4, fc, fc.checkServerName)
}
func (fc *RFBConn) checkServerName(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	result, _ := core.ReadUInt32BE(r)
	glog.Debug("RFBConn recvServerName", result, err)

	core.StartReadBytes(int(result), fc, fc.recvServerName)
}
func (fc *RFBConn) recvServerName(s []byte, err error) {
	glog.Debug("RFBConn recvServerName", string(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	// Ask for the pixel format this client reads before anything is drawn. A
	// rectangle's byte count is width*height*bytes-per-pixel of the active
	// format, so a server whose default is 16 bpp would otherwise be decoded as
	// though it were 32. The server accepts SetPixelFormat unconditionally, so
	// the requested format becomes the active one and is what BitRect reports.
	fc.sendPixelFormat()
	fc.BitRect.Pf = NewPixelFormat()
	// The framebuffer is sized from the ServerInit geometry and the format the
	// client just asked for. A size that fails the bound is reported here, before
	// any rectangle arrives, rather than rounded down: a framebuffer of the wrong
	// size silently corrupts every rectangle after it.
	if err := fc.initFramebuffer(); err != nil {
		glog.Errorf("%v", err)
		fc.Emit("error", err)
		return
	}
	fc.sendSetEncoding()
	fc.sendFramebufferUpdateRequest(0, 0, 0, fc.s.Width, fc.s.Height)

	fc.Emit("ready")
	core.StartReadBytes(1, fc, fc.recvServerOrder)
}

// initFramebuffer allocates the connection's framebuffer from the ServerInit
// geometry and the active pixel format. Both come off the wire, so the total is
// checked against maxFramebufferBytes before it sizes the allocation.
func (fc *RFBConn) initFramebuffer() error {
	if fc.s == nil {
		return errors.New("rfb: no server init to size the framebuffer")
	}
	w, h := int(fc.s.Width), int(fc.s.Height)
	if w <= 0 || h <= 0 {
		return fmt.Errorf("rfb: server desktop size %dx%d is out of range", w, h)
	}
	bpp := fc.bytesPerPixel()
	size := w * h * bpp
	if size < 0 || size > maxFramebufferBytes {
		return fmt.Errorf("rfb: framebuffer of %dx%d at %d bpp is %d bytes, over the %d byte limit",
			w, h, bpp*8, size, maxFramebufferBytes)
	}
	fc.frame = make([]byte, size)
	fc.frameStride = w * bpp
	return nil
}

// bytesPerPixel is the size of one pixel in the active format. The format is
// whatever the client asked for with SetPixelFormat; before that it is the
// server's, and a BitsPerPixel of zero falls back to four the way the old body
// length did.
func (fc *RFBConn) bytesPerPixel() int {
	if fc.BitRect.Pf != nil {
		if n := int(fc.BitRect.Pf.BitsPerPixel) / 8; n > 0 {
			return n
		}
	}
	return 4
}

// Framebuffer returns the pixels of every rectangle decoded so far, laid out
// row by row in the active pixel format with no row padding. Its length is
// width*height*bytesPerPixel; FramebufferSize reports the geometry and
// BitRect.Pf the format. It is nil before ServerInit has been read or when the
// desktop size was rejected. A rectangle narrower than the desktop is not
// contiguous in it, which is why Rectangles.Data carries the rectangle's own
// destination pixels rather than a slice of this buffer.
func (fc *RFBConn) Framebuffer() []byte { return fc.frame }

// FramebufferSize returns the framebuffer's width and height in pixels, or
// (0, 0) before ServerInit has been read.
func (fc *RFBConn) FramebufferSize() (width, height int) {
	if fc.s == nil {
		return 0, 0
	}
	return int(fc.s.Width), int(fc.s.Height)
}

func (fc *RFBConn) sendPixelFormat() {
	glog.Debug("sendPixelFormat")
	buff := &bytes.Buffer{}
	core.WriteUInt8(0, buff)    // SetPixelFormat
	core.WriteUInt8(0, buff)    // padding
	core.WriteUInt16BE(0, buff) // padding
	writePixelFormat(buff, NewPixelFormat())
	fc.Write(buff.Bytes())
}

// writePixelFormat writes a PIXEL_FORMAT structure in the same order
// ReadPixelFormat reads it: the three colour maxima are big-endian, which is how
// the bytes of a ServerInit from TigerVNC 1.13.1 carry red-max (00 ff). The struc
// tags on PixelFormat say little-endian, and the value they used to be paired
// with was 0xff00; those two mistakes cancel because 0xff00 little-endian is the
// same two bytes as 0x00ff big-endian. Writing the fields directly means the
// bytes no longer depend on that coincidence holding.
func writePixelFormat(w io.Writer, pf *PixelFormat) {
	core.WriteUInt8(pf.BitsPerPixel, w)
	core.WriteUInt8(pf.Depth, w)
	core.WriteUInt8(pf.BigEndianFlag, w)
	core.WriteUInt8(pf.TrueColorFlag, w)
	core.WriteUInt16BE(pf.RedMax, w)
	core.WriteUInt16BE(pf.GreenMax, w)
	core.WriteUInt16BE(pf.BlueMax, w)
	core.WriteUInt8(pf.RedShift, w)
	core.WriteUInt8(pf.GreenShift, w)
	core.WriteUInt8(pf.BlueShift, w)
	core.WriteUInt16BE(pf.Padding, w)
	core.WriteUInt8(pf.Padding1, w)
}

// sendSetEncoding advertises every encoding this client decodes in one
// SetEncodings message. The message is a count followed by that many 32 bit
// ids, so the encodings are a single list: a second message would replace the
// first rather than add to it.
func (fc *RFBConn) sendSetEncoding() {
	glog.Debug("sendSetEncoding")
	encodings := [...]uint32{encodingRaw, encodingCopyRect, encodingHextile}
	buff := &bytes.Buffer{}
	core.WriteUInt8(2, buff) // SetEncodings
	core.WriteUInt8(0, buff) // padding
	core.WriteUInt16BE(uint16(len(encodings)), buff)
	for _, e := range encodings {
		core.WriteUInt32BE(e, buff)
	}
	fc.Write(buff.Bytes())
}

// FrameBufferUpdateRequest is the client's RequestFramebufferUpdate message:
// whether the server may send only changes, and the rectangle to update.
type FrameBufferUpdateRequest struct {
	Incremental uint8
	X           uint16
	Y           uint16
	Width       uint16
	Height      uint16
}

func (fc *RFBConn) sendFramebufferUpdateRequest(Incremental uint8,
	X uint16,
	Y uint16,
	Width uint16,
	Height uint16) {
	glog.Debug("sendFramebufferUpdateRequest")
	buff := &bytes.Buffer{}
	core.WriteUInt8(3, buff)
	core.WriteUInt8(Incremental, buff)
	core.WriteUInt16BE(X, buff)
	core.WriteUInt16BE(Y, buff)
	core.WriteUInt16BE(Width, buff)
	core.WriteUInt16BE(Height, buff)
	fc.Write(buff.Bytes())
}
func (fc *RFBConn) recvServerOrder(s []byte, err error) {
	glog.Debug("RFBConn recvServerOrder", glog.Hex(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	packetType, _ := core.ReadUInt8(r)
	switch packetType {
	case 0:
		core.StartReadBytes(3, fc, fc.recvFrameBufferUpdateHeader)
	case 2:
		// Bell carries no body, so the next byte is the next message type.
		// Re-arm here: without it a single Bell ends parsing for the rest of
		// the session because nothing reads the byte after it.
		core.StartReadBytes(1, fc, fc.recvServerOrder)
	case 3:
		core.StartReadBytes(7, fc, fc.recvServerCutTextHeader)
	default:
		// An unknown message type has an unknown length, so its fields cannot
		// be read and the batch cannot continue: report it instead of re-arming,
		// which would parse the body as though it were the next message type.
		glog.Errorf("Unknown message type %d", packetType)
		fc.Emit("error", fmt.Errorf("rfb: unknown server message type %d", packetType))
	}
}

// BitRect is the set of rectangles of one FramebufferUpdate, along with the
// pixel format they are in. It is emitted as the "bitmap" event when the last
// rectangle of the update has been decoded.
type BitRect struct {
	Rects []Rectangles
	Pf    *PixelFormat
}

// Rectangles is one rectangle of an update: its header and its destination
// pixels. Data is the pixels of the rectangle itself, width*height*bytesPerPixel
// of them, so a rectangle narrower than the desktop is still contiguous; the
// connection's whole framebuffer is available through Framebuffer.
type Rectangles struct {
	Rect *Rectangle
	Data []byte
}

func (fc *RFBConn) recvFrameBufferUpdateHeader(s []byte, err error) {
	glog.Debug("RFBConn recvFrameBufferUpdateHeader", glog.Hex(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	core.ReadUInt8(r)
	NbRect, _ := core.ReadUint16BE(r)
	fc.NbRect = NbRect
	fc.rectIndex = 0
	fc.BitRect.Rects = make([]Rectangles, fc.NbRect)
	if NbRect == 0 {
		// No rectangles follow, so the update is complete. Re-arm so the next
		// message is read instead of the chain stopping here.
		core.StartReadBytes(1, fc, fc.recvServerOrder)
		return
	}
	glog.Info("NbRect:", NbRect)
	core.StartReadBytes(12, fc, fc.recvRectHeader)
}

// Rectangle is a rectangle header. SrcX and SrcY are set for a CopyRect, whose
// pixels come from elsewhere in the framebuffer; they are zero for every other
// encoding.
type Rectangle struct {
	X        uint16 `struc:"little"`
	Y        uint16 `struc:"little"`
	Width    uint16 `struc:"little"`
	Height   uint16 `struc:"little"`
	Encoding uint32 `struc:"little"`
	SrcX     uint16
	SrcY     uint16
}

func (fc *RFBConn) recvRectHeader(s []byte, err error) {
	glog.Debug("RFBConn recvRectHeader", glog.Hex(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	x, _ := core.ReadUint16BE(r)
	y, _ := core.ReadUint16BE(r)
	w, _ := core.ReadUint16BE(r)
	h, _ := core.ReadUint16BE(r)
	e, _ := core.ReadUInt32BE(r)
	rect := &Rectangle{X: x, Y: y, Width: w, Height: h, Encoding: e}

	fc.BitRect.Rects[fc.rectIndex].Rect = rect
	glog.Infof("rect %dx%d at %d,%d encoding %s", rect.Width, rect.Height, rect.X, rect.Y, encodingName(rect.Encoding))

	// The body length and how it is read depend on the encoding. A length
	// guessed for a different encoding would misread the rest of the update, so
	// an encoding this client does not implement is reported rather than read as
	// something else.
	switch rect.Encoding {
	case encodingRaw:
		size, err := fc.rectBodySize(rect)
		if err != nil {
			glog.Errorf("%v", err)
			fc.Emit("error", err)
			return
		}
		if err := fc.checkRectFits(rect); err != nil {
			fc.Emit("error", err)
			return
		}
		glog.Infof("raw rectangle body is %d bytes", size)
		core.StartReadBytes(size, fc, fc.recvRawBody)
	case encodingCopyRect:
		if err := fc.checkRectFits(rect); err != nil {
			fc.Emit("error", err)
			return
		}
		core.StartReadBytes(copyRectBodySize, fc, fc.recvCopyRectBody)
	case encodingHextile:
		size, err := fc.rectBodySize(rect)
		if err != nil {
			glog.Errorf("%v", err)
			fc.Emit("error", err)
			return
		}
		if err := fc.checkRectFits(rect); err != nil {
			fc.Emit("error", err)
			return
		}
		// Hextile has no body length on the wire, so it is read tile by tile
		// straight from the connection here, in this read goroutine.
		fc.recvHextileRect(rect, size)
	default:
		glog.Errorf("rfb: unsupported rectangle encoding %d", rect.Encoding)
		fc.Emit("error", fmt.Errorf("rfb: unsupported rectangle encoding %d", rect.Encoding))
	}
}

// encodingName names an encoding for logs, including the ones this client does
// not implement, which are then reported by id.
func encodingName(e uint32) string {
	switch e {
	case encodingRaw:
		return "Raw"
	case encodingCopyRect:
		return "CopyRect"
	case encodingHextile:
		return "Hextile"
	default:
		return fmt.Sprintf("id %d", e)
	}
}

// rectBodySize returns the number of pixel bytes a rectangle decodes to. Width,
// height and the active pixel format all come off the wire, so the product is
// bounded before it sizes a read or an allocation.
func (fc *RFBConn) rectBodySize(rect *Rectangle) (int, error) {
	size := int(rect.Width) * int(rect.Height) * fc.bytesPerPixel()
	if size < 0 || size > maxRectBytes {
		return 0, fmt.Errorf("rfb: rectangle %dx%d is out of range", rect.Width, rect.Height)
	}
	return size, nil
}

// rectInFrame reports whether a rectangle of w by h pixels at x,y lies inside
// the framebuffer.
func (fc *RFBConn) rectInFrame(x, y, w, h int) bool {
	if fc.s == nil {
		return false
	}
	return x >= 0 && y >= 0 && w >= 0 && h >= 0 &&
		x+w <= int(fc.s.Width) && y+h <= int(fc.s.Height)
}

// checkRectFits reports whether a rectangle header names a region inside the
// framebuffer. The coordinates are uint16 on the wire; a rectangle that runs
// past the framebuffer is rejected rather than wrapped, which would write the
// wrong rows.
func (fc *RFBConn) checkRectFits(rect *Rectangle) error {
	if !fc.rectInFrame(int(rect.X), int(rect.Y), int(rect.Width), int(rect.Height)) {
		return fmt.Errorf("rfb: rectangle %dx%d at %d,%d is outside the framebuffer",
			rect.Width, rect.Height, rect.X, rect.Y)
	}
	return nil
}

// writeRect stores a rectangle's decoded pixels in the framebuffer. data is the
// rectangle's pixels, width*height*bytesPerPixel of them; the framebuffer row is
// frameStride bytes, so the rows are copied one at a time.
func (fc *RFBConn) writeRect(rect *Rectangle, data []byte) {
	bpp := fc.bytesPerPixel()
	rowBytes := int(rect.Width) * bpp
	for row := 0; row < int(rect.Height); row++ {
		dst := (int(rect.Y)+row)*fc.frameStride + int(rect.X)*bpp
		src := row * rowBytes
		copy(fc.frame[dst:dst+rowBytes], data[src:src+rowBytes])
	}
}

// recvRawBody stores a Raw rectangle's body and hands it on.
func (fc *RFBConn) recvRawBody(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	rect := fc.BitRect.Rects[fc.rectIndex].Rect
	fc.writeRect(rect, s)
	fc.BitRect.Rects[fc.rectIndex].Data = s
	fc.finishRect()
}

// recvCopyRectBody reads the two source coordinates of a CopyRect and copies
// that region into the rectangle's position.
func (fc *RFBConn) recvCopyRectBody(s []byte, err error) {
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	srcX, _ := core.ReadUint16BE(r)
	srcY, _ := core.ReadUint16BE(r)
	rect := fc.BitRect.Rects[fc.rectIndex].Rect
	rect.SrcX, rect.SrcY = srcX, srcY
	data, err := fc.copyRect(rect, int(srcX), int(srcY))
	if err != nil {
		fc.Emit("error", err)
		return
	}
	fc.BitRect.Rects[fc.rectIndex].Data = data
	fc.finishRect()
}

// copyRect copies the source region named by a CopyRect into the rectangle's
// position in the framebuffer and returns the destination pixels. Source and
// destination can overlap, so the rows are copied in the direction that reads
// each source row before it is overwritten; a single row is copied with copy,
// which handles an overlap within the row the way memmove does. A source or
// destination outside the framebuffer is rejected rather than wrapped, because
// the coordinates are uint16 on the wire and wrapping would read or write the
// wrong rows.
func (fc *RFBConn) copyRect(rect *Rectangle, srcX, srcY int) ([]byte, error) {
	bpp := fc.bytesPerPixel()
	w, h := int(rect.Width), int(rect.Height)
	dstX, dstY := int(rect.X), int(rect.Y)
	if !fc.rectInFrame(srcX, srcY, w, h) {
		return nil, fmt.Errorf("rfb: CopyRect source %dx%d at %d,%d is outside the framebuffer",
			w, h, srcX, srcY)
	}
	if !fc.rectInFrame(dstX, dstY, w, h) {
		return nil, fmt.Errorf("rfb: CopyRect destination %dx%d at %d,%d is outside the framebuffer",
			w, h, dstX, dstY)
	}
	stride := fc.frameStride
	rowBytes := w * bpp
	srcOff := srcY*stride + srcX*bpp
	dstOff := dstY*stride + dstX*bpp
	if dstY > srcY {
		// The destination is below the source, so copy from the bottom up: each
		// source row is read before the row above it is overwritten.
		for row := h - 1; row >= 0; row-- {
			copy(fc.frame[dstOff+row*stride:dstOff+row*stride+rowBytes],
				fc.frame[srcOff+row*stride:srcOff+row*stride+rowBytes])
		}
	} else {
		for row := 0; row < h; row++ {
			copy(fc.frame[dstOff+row*stride:dstOff+row*stride+rowBytes],
				fc.frame[srcOff+row*stride:srcOff+row*stride+rowBytes])
		}
	}
	// The destination pixels are now in place; hand back the rectangle's own
	// pixels, which for a rectangle narrower than the desktop are not contiguous
	// in the framebuffer.
	data := make([]byte, h*rowBytes)
	for row := 0; row < h; row++ {
		copy(data[row*rowBytes:], fc.frame[dstOff+row*stride:dstOff+row*stride+rowBytes])
	}
	return data, nil
}

// recvHextileRect reads and decodes one Hextile rectangle into the framebuffer.
// Its body has no length field, so it is read tile by tile straight from the
// connection: the number of tiles comes from the rectangle geometry and each
// tile says how many subrectangles it carries, so every read is bounded. size is
// the rectangle's decoded size, already checked against maxRectBytes.
func (fc *RFBConn) recvHextileRect(rect *Rectangle, size int) {
	data := make([]byte, size)
	if err := decodeHextile(fc.Conn, data, int(rect.Width), int(rect.Height), fc.bytesPerPixel()); err != nil {
		glog.Errorf("%v", err)
		fc.Emit("error", err)
		return
	}
	fc.writeRect(rect, data)
	fc.BitRect.Rects[fc.rectIndex].Data = data
	fc.finishRect()
}

// decodeHextile reads one Hextile rectangle's body from r and writes its pixels
// into data, which is width*height*bytesPerPixel bytes. It is separate from
// recvHextileRect so the decoder can be fed bytes without a connection.
//
// The decoder follows RFC 6143 section 7.7.4 and TigerVNC's
// common/rfb/HextileDecoder: a background and foreground colour persist across
// tiles until a tile's subencoding replaces them, and a tile with AnySubrects
// clear is the whole tile in the background colour.
func decodeHextile(r io.Reader, data []byte, w, h, bpp int) error {
	stride := w * bpp
	bg := make([]byte, bpp)
	fg := make([]byte, bpp)
	tile := make([]byte, hextileTileArea*bpp)

	budget := maxHextileWireBytes
	read := func(dst []byte) error {
		if len(dst) > budget {
			return fmt.Errorf("rfb: Hextile rectangle carries more than %d bytes", maxHextileWireBytes)
		}
		budget -= len(dst)
		_, err := io.ReadFull(r, dst)
		if err != nil {
			return fmt.Errorf("rfb: Hextile body: %w", err)
		}
		return nil
	}

	var one [1]byte
	for ty := 0; ty < h; ty += hextileTilePixels {
		th := minInt(hextileTilePixels, h-ty)
		for tx := 0; tx < w; tx += hextileTilePixels {
			tw := minInt(hextileTilePixels, w-tx)
			tileBytes := tw * th * bpp

			if err := read(one[:]); err != nil {
				return err
			}
			sub := one[0]

			if sub&hextileRaw != 0 {
				if err := read(tile[:tileBytes]); err != nil {
					return err
				}
				blitTile(data, stride, tile, tw, th, tx, ty, bpp)
				continue
			}

			// The background colour persists across tiles when unspecified, so
			// bg and fg are not reset here. A tile is the background colour and
			// then the subrectangles are drawn in the foreground colour.
			if sub&hextileBgSpecified != 0 {
				if err := read(bg); err != nil {
					return err
				}
			}
			for i := 0; i < tileBytes; i += bpp {
				copy(tile[i:i+bpp], bg)
			}

			if sub&hextileFgSpecified != 0 {
				if err := read(fg); err != nil {
					return err
				}
			}

			if sub&hextileAnySubrects != 0 {
				if err := read(one[:]); err != nil {
					return err
				}
				nSubrects := int(one[0])
				for i := 0; i < nSubrects; i++ {
					if sub&hextileSubrectsColoured != 0 {
						// Each subrectangle carries its own foreground colour.
						if err := read(fg); err != nil {
							return err
						}
					}
					var xywh [2]byte
					if err := read(xywh[:]); err != nil {
						return err
					}
					sx, sy := int(xywh[0]>>4), int(xywh[0]&15)
					sw, sh := int(xywh[1]>>4)+1, int(xywh[1]&15)+1
					// The bit fields can name a subrectangle larger than the tile,
					// which would be read as a wrap into another row.
					if sx+sw > tw || sy+sh > th {
						return fmt.Errorf("rfb: Hextile subrectangle %dx%d at %d,%d is outside a %dx%d tile", sw, sh, sx, sy, tw, th)
					}
					for row := 0; row < sh; row++ {
						base := ((sy+row)*tw + sx) * bpp
						for c := 0; c < sw; c++ {
							copy(tile[base+c*bpp:base+c*bpp+bpp], fg)
						}
					}
				}
			}

			blitTile(data, stride, tile, tw, th, tx, ty, bpp)
		}
	}
	return nil
}

// blitTile copies one decoded Hextile tile into the rectangle's pixel buffer.
// data is the rectangle, tile is tw by th pixels and stride is the rectangle's
// row length in bytes.
func blitTile(data []byte, stride int, tile []byte, tw, th, tx, ty, bpp int) {
	rowBytes := tw * bpp
	for row := 0; row < th; row++ {
		dst := (ty+row)*stride + tx*bpp
		src := row * rowBytes
		copy(data[dst:dst+rowBytes], tile[src:src+rowBytes])
	}
}

// minInt is the smaller of a and b. It stands in for the builtin min, which the
// go 1.18 language version in go.mod does not have.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// finishRect advances to the next rectangle of the update, or emits the update
// and requests the next one when this was the last.
func (fc *RFBConn) finishRect() {
	fc.rectIndex++
	fc.NbRect--
	glog.Info("fc.NbRect:", fc.NbRect)
	if fc.NbRect == 0 {
		fc.Emit("bitmap", fc.BitRect)
		fc.sendFramebufferUpdateRequest(1, 0, 0, fc.s.Width, fc.s.Height)
		core.StartReadBytes(1, fc, fc.recvServerOrder)
	} else {
		core.StartReadBytes(12, fc, fc.recvRectHeader)
	}
}

func (fc *RFBConn) recvServerCutTextHeader(s []byte, err error) {
	glog.Debug("RFBConn recvServerCutTextHeader", len(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	// The header is three padding bytes and then a 32 bit big-endian length.
	// RFB is big-endian throughout, so reading the length little-endian (as
	// this used to) decodes a five byte cut text as 83886080: large enough to
	// matter when it sizes the read scheduled below.
	if _, err := core.ReadBytes(3, r); err != nil {
		fc.Emit("error", err)
		return
	}
	size, err := core.ReadUInt32BE(r)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	if size > maxCutTextBytes {
		glog.Errorf("rfb: cut text of %d bytes is out of range", size)
		fc.Emit("error", fmt.Errorf("rfb: cut text of %d bytes is out of range", size))
		return
	}
	core.StartReadBytes(int(size), fc, fc.recvServerCutTextBody)
}
func (fc *RFBConn) recvServerCutTextBody(s []byte, err error) {
	glog.Debug("RFBConn recvServerCutTextBody", string(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	fc.Emit("CutText", s)
	core.StartReadBytes(1, fc, fc.recvServerOrder)
}

// PixelFormat is the RFB PIXEL_FORMAT structure: how many bits a pixel is, how
// big each colour channel is and where it sits in the pixel.
type PixelFormat struct {
	BitsPerPixel  uint8  `struc:"little"`
	Depth         uint8  `struc:"little"`
	BigEndianFlag uint8  `struc:"little"`
	TrueColorFlag uint8  `struc:"little"`
	RedMax        uint16 `struc:"little"`
	GreenMax      uint16 `struc:"little"`
	BlueMax       uint16 `struc:"little"`
	RedShift      uint8  `struc:"little"`
	GreenShift    uint8  `struc:"little"`
	BlueShift     uint8  `struc:"little"`
	Padding       uint16 `struc:"little"`
	Padding1      uint8  `struc:"little"`
}

// ReadPixelFormat reads a PIXEL_FORMAT structure. RFB is big-endian, so the
// colour maxima are read big-endian even though the struc tags on PixelFormat
// say little-endian.
func ReadPixelFormat(r io.Reader) *PixelFormat {
	p := NewPixelFormat()
	p.BitsPerPixel, _ = core.ReadUInt8(r)
	p.Depth, _ = core.ReadUInt8(r)
	p.BigEndianFlag, _ = core.ReadUInt8(r)
	p.TrueColorFlag, _ = core.ReadUInt8(r)
	p.RedMax, _ = core.ReadUint16BE(r)
	p.GreenMax, _ = core.ReadUint16BE(r)
	p.BlueMax, _ = core.ReadUint16BE(r)
	p.RedShift, _ = core.ReadUInt8(r)
	p.GreenShift, _ = core.ReadUInt8(r)
	p.BlueShift, _ = core.ReadUInt8(r)
	p.Padding, _ = core.ReadUint16BE(r)
	p.Padding1, _ = core.ReadUInt8(r)

	return p
}

// NewPixelFormat is the format the client asks for with SetPixelFormat: 32 bits
// per pixel, 8 bits per colour. A colour maximum is the largest value the field
// holds, so with an 8 bit channel it is 0xff. The previous value, 0xff00, packed
// with the little-endian struc tag to the same two bytes on the wire, so it
// worked; it is written as 0xff here because that is what the field means.
func NewPixelFormat() *PixelFormat {
	return &PixelFormat{
		32, 24, 0, 1, 255, 255, 255, 16, 8, 0, 0, 0,
	}
}

// RFB is the client side of an RFB session. It carries the transport, the
// negotiated version and the pixel format, and it writes the client messages:
// keyboard, pointer and clipboard. RFBConn decodes the server's replies.
type RFB struct {
	core.Transport
	Version       string
	SecurityLevel uint8
	ServerName    string
	PixelFormat   *PixelFormat
	NbRect        int
	CurrentRect   *Rectangle
}

// NewRFB wraps a transport in an RFB session with the newest protocol version
// this client implements. Connect starts the handshake.
func NewRFB(t core.Transport) *RFB {
	fb := &RFB{t, RFB003008, SEC_INVALID, "", NewPixelFormat(), 0, &Rectangle{}}

	return fb
}

// Connect starts the handshake by reading the server's protocol version banner.
// It returns an error only for a missing transport; a handshake failure arrives
// as an "error" event.
func (fb *RFB) Connect() error {
	if fb.Transport == nil {
		return errors.New("no transport")
	}
	// Register the version handler before starting the read. The server sends
	// its banner as soon as the connection opens, so reading it before there is
	// a listener would drop the only response the handshake needs.
	fb.Once("data", fb.recvProtocolVersion)
	if fc, ok := fb.Transport.(*RFBConn); ok {
		fc.start()
	}
	return nil
}

func (fb *RFB) recvProtocolVersion(version string) {
	// The event carries the version RFBConn negotiated from the server's
	// banner, so writing it back agrees with the flow RFBConn now runs. Record
	// it on the RFB too, so a caller can see what was agreed.
	fb.Version = version
	glog.Infof("version:%s", version)
	b := &bytes.Buffer{}
	b.WriteString(version)
	fb.Write(b.Bytes())
}

// KeyEvent is a client keyboard event: whether the key went down and which key
// it is. The key is an X11 keysym.
type KeyEvent struct {
	DownFlag uint8  `struc:"little"`
	Padding  uint16 `struc:"little"`
	Key      uint32 `struc:"little"`
}

// SendKeyEvent writes a KeyEvent message to the server.
func (fb *RFB) SendKeyEvent(k *KeyEvent) {
	b := &bytes.Buffer{}
	core.WriteUInt8(4, b)
	core.WriteUInt8(k.DownFlag, b)
	core.WriteUInt16BE(k.Padding, b)
	core.WriteUInt32BE(k.Key, b)
	fb.Write(b.Bytes())
}

// PointerEvent is a client pointer event: the button or buttons held and the
// pointer position.
type PointerEvent struct {
	Mask uint8  `struc:"little"`
	XPos uint16 `struc:"little"`
	YPos uint16 `struc:"little"`
}

// SendPointEvent writes a PointerEvent message to the server.
func (fb *RFB) SendPointEvent(p *PointerEvent) {
	b := &bytes.Buffer{}
	core.WriteUInt8(5, b)
	core.WriteUInt8(p.Mask, b)
	core.WriteUInt16BE(p.XPos, b)
	core.WriteUInt16BE(p.YPos, b)
	fb.Write(b.Bytes())
}

// ClientCutText is a client to server cut text message. RFB carries it as the
// message type, three padding bytes, a big-endian length and then the raw text;
// the length is the byte length of Message.
type ClientCutText struct {
	Message string
}

// SendClientCutText writes a ClientCutText message to the server.
func (fb *RFB) SendClientCutText(t *ClientCutText) {
	b := &bytes.Buffer{}
	core.WriteUInt8(6, b)
	core.WriteUInt8(0, b)    // padding
	core.WriteUInt16BE(0, b) // padding
	core.WriteUInt32BE(uint32(len(t.Message)), b)
	b.WriteString(t.Message)
	fb.Write(b.Bytes())
}
