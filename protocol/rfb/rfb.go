// Package rfb is the RFB (VNC) client protocol this fork inherited from
// upstream.
//
// It builds and is kept. Its parse steps have unit tests that drive them over a
// scripted net.Pipe server, but nothing in this project has run the client
// against a real VNC server, so it is not a supported path.
package rfb

import (
	"bytes"
	"crypto/des"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/lunixbochs/struc"

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

type RFBConn struct {
	emission.Emitter
	// The Socket connection to the client
	Conn     net.Conn
	s        *ServerInit
	NbRect   uint16
	BitRect  *BitRect
	Password string
}

// The RFB client in this package is inherited from the upstream fork. Nothing
// in this project has run it against a VNC server. The tests here drive the
// parse steps over a scripted net.Pipe server, which covers the wire formats
// but not a live session; the clipboard is not implemented.

// maxRectBytes bounds a framebuffer rectangle, and maxCutTextBytes bounds a
// server cut text. Both lengths come off the wire and are checked before they
// size an allocation.
const (
	maxRectBytes       = 64 << 20
	maxCutTextBytes    = 16 << 20
	maxServerNameBytes = 1 << 20
	maxSecurityTypes   = 1024
)

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

func (fc *RFBConn) Read(b []byte) (n int, err error) {
	return fc.Conn.Read(b)
}

func (fc *RFBConn) Write(data []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	buff.Write(data)
	return fc.Conn.Write(buff.Bytes())
}
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
	fc.Emit("data", version)

	if version == RFB003003 {
		fc.Emit("error", fmt.Errorf("%s", "Not Support RFB003003"))
		return
		//core.StartReadBytes(4, fc, fc.recvSecurityServer)
	} else {
		core.StartReadBytes(1, fc, fc.checkSecurityList)
	}
}
func (fc *RFBConn) checkSecurityList(s []byte, err error) {
	r := bytes.NewReader(s)
	result, _ := core.ReadUInt8(r)
	glog.Debug("RFBConn recvSecurityList", result, err)

	core.StartReadBytes(int(result), fc, fc.recvSecurityList)
}
func (fc *RFBConn) recvSecurityList(s []byte, err error) {
	r := bytes.NewReader(s)
	secLevel := SEC_VNC
	for r.Len() > 0 {
		result, _ := core.ReadUInt8(r)
		if result == SEC_NONE || result == SEC_VNC {
			secLevel = result
			break
		}
	}

	glog.Debug("RFBConn recvSecurityList", secLevel, err)
	buff := &bytes.Buffer{}
	core.WriteUInt8(secLevel, buff)
	fc.Write(buff.Bytes())
	if secLevel == SEC_VNC {
		core.StartReadBytes(16, fc, fc.recvVNCChallenge)
	} else {
		core.StartReadBytes(4, fc, fc.recvSecurityResult)
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
	glog.Debug("RFBConn recvVNCChallenge", hex.EncodeToString(s), len(s), err)
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
	r := bytes.NewReader(s)
	result, _ := core.ReadUInt32BE(r)
	glog.Debug("RFBConn recvSecurityResult", result, err)
	if result == 1 {
		fc.Emit("error", fmt.Errorf("%s", "Authentification failed"))
		return
	}
	buff := &bytes.Buffer{}
	core.WriteUInt8(0, buff) //share
	fc.Write(buff.Bytes())
	core.StartReadBytes(20, fc, fc.recvServerInit)
}

type ServerInit struct {
	Width       uint16       `struc:"little"`
	Height      uint16       `struc:"little"`
	PixelFormat *PixelFormat `struc:"little"`
}

func (fc *RFBConn) recvServerInit(s []byte, err error) {
	glog.Debug("RFBConn recvServerInit", len(s), err)
	r := bytes.NewReader(s)
	si := &ServerInit{}
	si.Width, err = core.ReadUint16BE(r)
	si.Height, err = core.ReadUint16BE(r)
	si.PixelFormat = ReadPixelFormat(r)
	glog.Infof("serverInit:%+v, %+v", si, si.PixelFormat)
	fc.s = si
	fc.BitRect.Pf = si.PixelFormat
	core.StartReadBytes(4, fc, fc.checkServerName)
}
func (fc *RFBConn) checkServerName(s []byte, err error) {
	r := bytes.NewReader(s)
	result, _ := core.ReadUInt32BE(r)
	glog.Debug("RFBConn recvSecurityList", result, err)

	core.StartReadBytes(int(result), fc, fc.recvServerName)
}
func (fc *RFBConn) recvServerName(s []byte, err error) {
	glog.Debug("RFBConn recvServerName", string(s), err)
	//fc.sendPixelFormat()
	fc.sendSetEncoding()
	fc.sendFramebufferUpdateRequest(0, 0, 0, fc.s.Width, fc.s.Height)

	fc.Emit("ready")
	core.StartReadBytes(1, fc, fc.recvServerOrder)
}

func (fc *RFBConn) sendPixelFormat() {
	glog.Debug("sendPixelFormat")
	buff := &bytes.Buffer{}
	core.WriteUInt8(0, buff)
	core.WriteUInt16BE(0, buff)
	core.WriteUInt8(0, buff)
	err := struc.Pack(buff, NewPixelFormat())
	if err != nil {
		fc.Emit("error", err)
		return
	}
	fc.Write(buff.Bytes())
}
func (fc *RFBConn) sendSetEncoding() {
	glog.Debug("sendSetEncoding")
	buff := &bytes.Buffer{}
	core.WriteUInt8(2, buff)
	core.WriteUInt8(0, buff)
	core.WriteUInt16BE(1, buff)
	core.WriteUInt32BE(0, buff)
	fc.Write(buff.Bytes())
}

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
	glog.Debug("RFBConn recvServerOrder", hex.EncodeToString(s), err)
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

type BitRect struct {
	Rects []Rectangles
	Pf    *PixelFormat
}

type Rectangles struct {
	Rect *Rectangle
	Data []byte
}

func (fc *RFBConn) recvFrameBufferUpdateHeader(s []byte, err error) {
	glog.Debug("RFBConn recvFrameBufferUpdateHeader", hex.EncodeToString(s), err)
	if err != nil {
		fc.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	core.ReadUInt8(r)
	NbRect, _ := core.ReadUint16BE(r)
	fc.NbRect = NbRect
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

type Rectangle struct {
	X        uint16 `struc:"little"`
	Y        uint16 `struc:"little"`
	Width    uint16 `struc:"little"`
	Height   uint16 `struc:"little"`
	Encoding uint32 `struc:"little"`
}

func (fc *RFBConn) recvRectHeader(s []byte, err error) {
	glog.Debug("RFBConn recvRectHeader", hex.EncodeToString(s), err)
	r := bytes.NewReader(s)
	x, err := core.ReadUint16BE(r)
	y, err := core.ReadUint16BE(r)
	w, err := core.ReadUint16BE(r)
	h, err := core.ReadUint16BE(r)
	e, err := core.ReadUInt32BE(r)
	rect := &Rectangle{x, y, w, h, e}

	fc.BitRect.Rects[fc.NbRect-1].Rect = rect

	size := int(rect.Width) * int(rect.Height) * 4
	if size < 0 || size > maxRectBytes {
		glog.Errorf("rfb: rectangle %dx%d is out of range", rect.Width, rect.Height)
		fc.Emit("error", fmt.Errorf("rfb: rectangle %dx%d is out of range", rect.Width, rect.Height))
		return
	}
	glog.Infof("rect:%+v, len=%d", rect, size)
	core.StartReadBytes(size, fc, fc.recvRectBody)
}
func (fc *RFBConn) recvRectBody(s []byte, err error) {
	glog.Debug("RFBConn recvRectBody", hex.EncodeToString(s), err)
	fc.BitRect.Rects[fc.NbRect-1].Data = s
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
func NewPixelFormat() *PixelFormat {
	return &PixelFormat{
		32, 24, 0, 1, 65280, 65280, 65280, 16, 8, 0, 0, 0,
	}
}

type RFB struct {
	core.Transport
	Version       string
	SecurityLevel uint8
	ServerName    string
	PixelFormat   *PixelFormat
	NbRect        int
	CurrentRect   *Rectangle
}

func NewRFB(t core.Transport) *RFB {
	fb := &RFB{t, RFB003008, SEC_INVALID, "", NewPixelFormat(), 0, &Rectangle{}}

	return fb
}

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
	if version != RFB003003 && version != RFB003007 && version != RFB003008 {
		version = RFB003008
	}
	glog.Infof("version:%s", version)
	b := &bytes.Buffer{}
	b.WriteString(version)
	fb.Write(b.Bytes())
}

type KeyEvent struct {
	DownFlag uint8  `struc:"little"`
	Padding  uint16 `struc:"little"`
	Key      uint32 `struc:"little"`
}

func (fb *RFB) SendKeyEvent(k *KeyEvent) {
	b := &bytes.Buffer{}
	core.WriteUInt8(4, b)
	core.WriteUInt8(k.DownFlag, b)
	core.WriteUInt16BE(k.Padding, b)
	core.WriteUInt32BE(k.Key, b)
	fb.Write(b.Bytes())
}

type PointerEvent struct {
	Mask uint8  `struc:"little"`
	XPos uint16 `struc:"little"`
	YPos uint16 `struc:"little"`
}

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

func (fb *RFB) SendClientCutText(t *ClientCutText) {
	b := &bytes.Buffer{}
	core.WriteUInt8(6, b)
	core.WriteUInt8(0, b)    // padding
	core.WriteUInt16BE(0, b) // padding
	core.WriteUInt32BE(uint32(len(t.Message)), b)
	b.WriteString(t.Message)
	fb.Write(b.Bytes())
}
