package rfb

import (
	"bytes"
	"crypto/des"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
)

// The tests here drive the RFB parse steps the way a server would: a scripted
// peer on a net.Pipe connection performs the handshake in the order the client
// reads it, and the test asserts what came out. Nothing here is a live VNC
// server; a real session still has to be exercised before this path is called
// supported.

func testPixelFormat() *PixelFormat {
	return &PixelFormat{
		BitsPerPixel:  32,
		Depth:         24,
		BigEndianFlag: 0,
		TrueColorFlag: 1,
		RedMax:        0x001f,
		GreenMax:      0x003f,
		BlueMax:       0x001f,
		RedShift:      11,
		GreenShift:    5,
		BlueShift:     0,
	}
}

func encodeServerInit(si *ServerInit) []byte {
	b := &bytes.Buffer{}
	core.WriteUInt16BE(si.Width, b)
	core.WriteUInt16BE(si.Height, b)
	pf := si.PixelFormat
	core.WriteUInt8(pf.BitsPerPixel, b)
	core.WriteUInt8(pf.Depth, b)
	core.WriteUInt8(pf.BigEndianFlag, b)
	core.WriteUInt8(pf.TrueColorFlag, b)
	core.WriteUInt16BE(pf.RedMax, b)
	core.WriteUInt16BE(pf.GreenMax, b)
	core.WriteUInt16BE(pf.BlueMax, b)
	core.WriteUInt8(pf.RedShift, b)
	core.WriteUInt8(pf.GreenShift, b)
	core.WriteUInt8(pf.BlueShift, b)
	core.WriteUInt16BE(pf.Padding, b)
	core.WriteUInt8(pf.Padding1, b)
	return b.Bytes()
}

// writeServerCutText writes a ServerCutText message: the message type, three
// padding bytes, a big-endian length and the text.
func writeServerCutText(w io.Writer, text []byte) {
	b := []byte{3, 0, 0, 0}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(text)))
	b = append(b, l[:]...)
	b = append(b, text...)
	w.Write(b)
}

// serveRFBHandshake runs the server half of an RFB 003.008 handshake on conn and
// then calls after with the connection. The order mirrors what the client reads
// so that a synchronous net.Pipe connection cannot deadlock. Errors stop the
// script; the test then fails by timing out.
func serveRFBHandshake(conn net.Conn, si *ServerInit, name string, after func(net.Conn)) {
	writeAll := func(b []byte) bool {
		_, err := conn.Write(b)
		return err == nil
	}
	readAll := func(n int) bool {
		_, err := io.ReadFull(conn, make([]byte, n))
		return err == nil
	}

	if !writeAll([]byte(RFB003008)) {
		return
	}
	if !readAll(12) { // client version
		return
	}
	if !writeAll([]byte{1, SEC_NONE}) { // one security type: None
		return
	}
	if !readAll(1) { // client's chosen type
		return
	}
	if !writeAll([]byte{0, 0, 0, 0}) { // security result: OK
		return
	}
	if !readAll(1) { // shared-desktop flag
		return
	}

	init := encodeServerInit(si)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(name)))
	init = append(init, l[:]...)
	init = append(init, []byte(name)...)
	if !writeAll(init) {
		return
	}

	if !readAll(20 + 16 + 10) { // SetPixelFormat + SetEncodings (3 ids) + FramebufferUpdateRequest
		return
	}

	if after != nil {
		after(conn)
	}
}

type testClient struct {
	fc      *RFBConn
	rf      *RFB
	server  net.Conn
	cutText chan []byte
	errCh   chan error
	ready   chan struct{}
}

func newTestClient(t *testing.T, si *ServerInit, name string, after func(net.Conn)) *testClient {
	t.Helper()
	return newTestClientScript(t, func(conn net.Conn) {
		serveRFBHandshake(conn, si, name, after)
	})
}

// newTestClientScript builds a client whose peer runs script, so a test can
// drive a handshake that differs from the 3.8 one serveRFBHandshake writes.
func newTestClientScript(t *testing.T, script func(net.Conn)) *testClient {
	t.Helper()
	client, server := net.Pipe()
	fc := NewRFBConn(client, "")
	tc := &testClient{
		fc:      fc,
		rf:      NewRFB(fc),
		server:  server,
		cutText: make(chan []byte, 1),
		errCh:   make(chan error, 1),
		ready:   make(chan struct{}, 1),
	}
	tc.rf.On("CutText", func(b []byte) {
		select {
		case tc.cutText <- b:
		default:
		}
	})
	tc.rf.On("error", func(e error) {
		select {
		case tc.errCh <- e:
		default:
		}
	})
	tc.rf.On("ready", func() {
		select {
		case tc.ready <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	go script(server)
	return tc
}

func (tc *testClient) connect(t *testing.T) {
	t.Helper()
	if err := tc.rf.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// TestRecvServerCutTextHeaderIsBigEndian is the endianness bug: a five byte cut
// text was decoded as 83886080 because the length was read little-endian. The
// length heads into an allocation, so the wrong value is also the dangerous one.
func TestRecvServerCutTextHeaderIsBigEndian(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})
	cutCh := make(chan []byte, 1)
	fc.On("CutText", func(b []byte) {
		select {
		case cutCh <- b:
		default:
		}
	})

	// Three padding bytes and a big-endian length of five.
	fc.recvServerCutTextHeader([]byte{0, 0, 0, 0, 0, 0, 5}, nil)
	go func() { server.Write([]byte("hello")) }()

	select {
	case got := <-cutCh:
		if string(got) != "hello" {
			t.Fatalf("cut text = %q, want %q", got, "hello")
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cut text body")
	}
}

// TestRecvServerCutTextHeaderRejectsOversized checks the bound that guards the
// allocation is still enforced after the endianness fix.
func TestRecvServerCutTextHeaderRejectsOversized(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	header := make([]byte, 7)
	binary.BigEndian.PutUint32(header[3:], maxCutTextBytes+1)
	fc.recvServerCutTextHeader(header, nil)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "out of range") {
			t.Fatalf("error = %v, want an out-of-range error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an oversized cut text header was not rejected")
	}
}

// TestServerCutTextEndToEnd drives a whole handshake to a cut text, so the
// header parse is exercised from the message type onwards rather than in
// isolation.
func TestServerCutTextEndToEnd(t *testing.T) {
	cut := []byte("end-to-end")
	tc := newTestClient(t, &ServerInit{Width: 1024, Height: 768, PixelFormat: testPixelFormat()}, "test", func(s net.Conn) {
		writeServerCutText(s, cut)
	})
	tc.connect(t)

	select {
	case got := <-tc.cutText:
		if !bytes.Equal(got, cut) {
			t.Fatalf("cut text = %q, want %q", got, cut)
		}
	case err := <-tc.errCh:
		t.Fatalf("unexpected handshake error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server cut text")
	}
}

// TestServerInitParsesBanner checks the ServerInit and pixel format arrive with
// the values the server sent. The geometry is kept inside the framebuffer bound,
// which a 0x1234 x 0x5678 desktop would exceed and be rejected for.
func TestServerInitParsesBanner(t *testing.T) {
	pf := testPixelFormat()
	si := &ServerInit{Width: 0x0123, Height: 0x0456, PixelFormat: pf}
	tc := newTestClient(t, si, "myhost", nil)
	tc.connect(t)

	select {
	case <-tc.ready:
	case err := <-tc.errCh:
		t.Fatalf("unexpected handshake error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the session to become ready")
	}

	if tc.fc.s == nil {
		t.Fatal("server init was not stored")
	}
	if tc.fc.s.Width != 0x0123 || tc.fc.s.Height != 0x0456 {
		t.Fatalf("size = %d x %d, want 0x0123 x 0x0456", tc.fc.s.Width, tc.fc.s.Height)
	}
	got := tc.fc.s.PixelFormat
	if got == nil {
		t.Fatal("server pixel format was not stored")
	}
	if got.RedMax != pf.RedMax || got.GreenMax != pf.GreenMax || got.BlueShift != pf.BlueShift {
		t.Fatalf("server pixel format = %+v, want red max %#x, green max %#x, blue shift %d",
			got, pf.RedMax, pf.GreenMax, pf.BlueShift)
	}
	// The format the client asks for with SetPixelFormat, not the server's
	// default. Confirmed against TigerVNC 1.13.1, which accepts it and keeps
	// sending 32 bpp data.
	active := tc.fc.BitRect.Pf
	if active == nil || active.BitsPerPixel != 32 || active.RedMax != 255 || active.GreenShift != 8 {
		t.Fatalf("active pixel format = %+v, want the requested 32 bpp format", active)
	}
}

// TestServerOrderBellRearms checks a Bell does not end the read chain: the cut
// text that follows it must still be parsed.
func TestServerOrderBellRearms(t *testing.T) {
	cut := []byte("after-bell")
	tc := newTestClient(t, &ServerInit{Width: 1024, Height: 768, PixelFormat: testPixelFormat()}, "test", func(s net.Conn) {
		s.Write([]byte{2}) // Bell: no body
		writeServerCutText(s, cut)
	})
	tc.connect(t)

	select {
	case got := <-tc.cutText:
		if !bytes.Equal(got, cut) {
			t.Fatalf("cut text = %q, want %q", got, cut)
		}
	case err := <-tc.errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: the read chain stopped at the Bell")
	}
}

// TestServerFrameBufferUpdateZeroRectsRearms checks an update with no rectangles
// does not end the read chain.
func TestServerFrameBufferUpdateZeroRectsRearms(t *testing.T) {
	cut := []byte("after-empty-update")
	tc := newTestClient(t, &ServerInit{Width: 1024, Height: 768, PixelFormat: testPixelFormat()}, "test", func(s net.Conn) {
		s.Write([]byte{0, 0, 0, 0}) // FramebufferUpdate, padding, rectangle count 0
		writeServerCutText(s, cut)
	})
	tc.connect(t)

	select {
	case got := <-tc.cutText:
		if !bytes.Equal(got, cut) {
			t.Fatalf("cut text = %q, want %q", got, cut)
		}
	case err := <-tc.errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: the read chain stopped at the empty update")
	}
}

// TestServerOrderUnknownTypeFails checks an unknown message type is reported
// rather than re-armed: its length is unknown, so re-arming would parse the
// body as though it were the next message type.
func TestServerOrderUnknownTypeFails(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	fc.recvServerOrder([]byte{9}, nil)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "unknown server message type 9") {
			t.Fatalf("error = %v, want an unknown-message-type error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unknown server message type was not reported")
	}
}

// recordingTransport captures what the client writes and never reads.
type recordingTransport struct {
	*emission.Emitter
	buf bytes.Buffer
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{Emitter: emission.NewEmitter()}
}

func (r *recordingTransport) Read([]byte) (int, error)    { return 0, io.EOF }
func (r *recordingTransport) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recordingTransport) Close() error                { return nil }

// TestSendClientCutTextWireFormat checks the ClientCutText message is the type,
// three padding bytes, a big-endian length and the raw text, not a
// length-prefixed little-endian struct.
func TestSendClientCutTextWireFormat(t *testing.T) {
	rt := newRecordingTransport()
	rf := NewRFB(rt)
	rf.SendClientCutText(&ClientCutText{Message: "hi"})

	want := []byte{6, 0, 0, 0, 0, 0, 0, 2, 'h', 'i'}
	if got := rt.buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("client cut text = % x, want % x", got, want)
	}
}

// TestRecvVNCChallengeUsesPasswordBytes checks the DES key is the password's
// bytes, not a UTF-16 encoding of it. The expected response is computed here
// from the raw bytes, so the UTF-16 key produces a different answer.
func TestRecvVNCChallengeUsesPasswordBytes(t *testing.T) {
	const password = "passwd"
	challenge := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	}

	want := make([]byte, 16)
	cipher, err := des.NewCipher(fixDesKey([]byte(password)))
	if err != nil {
		t.Fatalf("des.NewCipher: %v", err)
	}
	cipher.Encrypt(want, challenge)
	cipher.Encrypt(want[8:], challenge[8:])

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, password)

	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16)
		if _, err := io.ReadFull(server, buf); err == nil {
			got <- buf
		}
	}()

	fc.recvVNCChallenge(challenge, nil)

	select {
	case response := <-got:
		if !bytes.Equal(response, want) {
			t.Fatalf("challenge response = % x, want % x", response, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the challenge response")
	}
}

// writeServerInit writes a ServerInit message followed by the desktop name.
func writeServerInit(w io.Writer, si *ServerInit, name string) {
	init := encodeServerInit(si)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(name)))
	init = append(init, l[:]...)
	init = append(init, []byte(name)...)
	w.Write(init)
}

// discard reads and drops n bytes, reporting whether it could.
func discard(conn net.Conn, n int) bool {
	_, err := io.ReadFull(conn, make([]byte, n))
	return err == nil
}

// writeFrameBufferUpdateRaw writes a FramebufferUpdate whose rectangles are all
// Raw and 32 bpp, with zeroed pixel bytes.
func writeFrameBufferUpdateRaw(w io.Writer, rects []Rectangle) {
	b := &bytes.Buffer{}
	core.WriteUInt8(0, b) // FramebufferUpdate
	core.WriteUInt8(0, b) // padding
	core.WriteUInt16BE(uint16(len(rects)), b)
	for _, r := range rects {
		core.WriteUInt16BE(r.X, b)
		core.WriteUInt16BE(r.Y, b)
		core.WriteUInt16BE(r.Width, b)
		core.WriteUInt16BE(r.Height, b)
		core.WriteUInt32BE(r.Encoding, b)
		b.Write(make([]byte, int(r.Width)*int(r.Height)*4))
	}
	w.Write(b.Bytes())
}

// TestSendPixelFormatWireFormat pins the SetPixelFormat message to the bytes a
// server reads: message type 0, three padding bytes and a PIXEL_FORMAT with 8 bit
// colour maxima and shifts 16/8/0. This is how the message has to look on the
// wire; the values are checked against a real ServerInit's pixel format, whose
// red-max arrives as 00 ff.
func TestSendPixelFormatWireFormat(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")

	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 20)
		if _, err := io.ReadFull(server, buf); err == nil {
			got <- buf
		}
	}()

	fc.sendPixelFormat()

	want := []byte{
		0x00, 0x00, 0x00, 0x00, // message type and padding
		0x20, 0x18, 0x00, 0x01, // 32 bpp, depth 24, little endian, true colour
		0x00, 0xff, 0x00, 0xff, 0x00, 0xff, // red, green and blue max
		0x10, 0x08, 0x00, 0x00, 0x00, 0x00, // shifts and padding
	}
	select {
	case b := <-got:
		if !bytes.Equal(b, want) {
			t.Fatalf("SetPixelFormat = % x, want % x", b, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the SetPixelFormat message")
	}
}

// TestHandshake3_3None drives the 3.3 flow: the server sends one 32 bit security
// type and, for None, no SecurityResult. The client used to reject 3.3 outright,
// so this only becomes ready with the version handling.
func TestHandshake3_3None(t *testing.T) {
	si := &ServerInit{Width: 1024, Height: 768, PixelFormat: testPixelFormat()}
	tc := newTestClientScript(t, func(conn net.Conn) {
		if _, err := conn.Write([]byte(RFB003003)); err != nil {
			return
		}
		if !discard(conn, 12) { // client version
			return
		}
		conn.Write([]byte{0, 0, 0, 1}) // security type 1 (None), 32 bit
		if !discard(conn, 1) {         // shared-desktop flag, no SecurityResult
			return
		}
		writeServerInit(conn, si, "three-three")
		discard(conn, 46) // SetPixelFormat + SetEncodings (3 ids) + FramebufferUpdateRequest
	})
	tc.connect(t)

	select {
	case <-tc.ready:
	case err := <-tc.errCh:
		t.Fatalf("unexpected handshake error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: 3.3 with security type None never became ready")
	}
	if tc.rf.Version != RFB003003 {
		t.Fatalf("negotiated version = %q, want %q", tc.rf.Version, RFB003003)
	}
}

// TestHandshake3_7NoneSkipsSecurityResult drives the 3.7 flow. 3.7 sends no
// SecurityResult when the chosen type is None, which was confirmed against
// TigerVNC 1.13.1; a client that waits for one never sees ServerInit.
func TestHandshake3_7NoneSkipsSecurityResult(t *testing.T) {
	si := &ServerInit{Width: 640, Height: 480, PixelFormat: testPixelFormat()}
	tc := newTestClientScript(t, func(conn net.Conn) {
		if _, err := conn.Write([]byte(RFB003007)); err != nil {
			return
		}
		if !discard(conn, 12) { // client version
			return
		}
		conn.Write([]byte{1, SEC_NONE}) // one security type: None
		if !discard(conn, 1) {          // client's chosen type
			return
		}
		if !discard(conn, 1) { // shared-desktop flag, no SecurityResult in 3.7
			return
		}
		writeServerInit(conn, si, "three-seven")
		discard(conn, 46) // SetPixelFormat + SetEncodings (3 ids) + FramebufferUpdateRequest
	})
	tc.connect(t)

	select {
	case <-tc.ready:
	case err := <-tc.errCh:
		t.Fatalf("unexpected handshake error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: 3.7 with None waited for a SecurityResult it does not send")
	}
}

// TestHandshake3_7VncAuthKeepsSecurityResult checks the other half of the 3.7
// rule: VNC authentication still gets a SecurityResult in 3.7, so the client
// must read it rather than skip straight to the shared flag.
func TestHandshake3_7VncAuthKeepsSecurityResult(t *testing.T) {
	si := &ServerInit{Width: 800, Height: 600, PixelFormat: testPixelFormat()}
	challenge := []byte{
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00,
	}
	tc := newTestClientScript(t, func(conn net.Conn) {
		if _, err := conn.Write([]byte(RFB003007)); err != nil {
			return
		}
		if !discard(conn, 12) { // client version
			return
		}
		conn.Write([]byte{1, SEC_VNC}) // one security type: VNC authentication
		if !discard(conn, 1) {         // client's chosen type
			return
		}
		conn.Write(challenge)
		if !discard(conn, 16) { // challenge response
			return
		}
		conn.Write([]byte{0, 0, 0, 0}) // SecurityResult: OK, sent even in 3.7
		if !discard(conn, 1) {         // shared-desktop flag
			return
		}
		writeServerInit(conn, si, "three-seven-auth")
		discard(conn, 46) // SetPixelFormat + SetEncodings (3 ids) + FramebufferUpdateRequest
	})
	tc.connect(t)

	select {
	case <-tc.ready:
	case err := <-tc.errCh:
		t.Fatalf("unexpected handshake error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: 3.7 VNC authentication did not reach ready")
	}
}

// TestSecurityListRejectsUnsupportedType checks a server that offers only types
// this client does not implement is reported, rather than answered with a type
// it did not offer.
func TestSecurityListRejectsUnsupportedType(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")
	fc.version = RFB003008

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	fc.recvSecurityList([]byte{18}, nil) // 18 is TLS, not offered by this client

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "no supported security type") {
			t.Fatalf("error = %v, want an unsupported-security-type error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unsupported security list was not reported")
	}
}

// TestSecurityFailureReadsReason checks the zero-count security list, which the
// server follows with a length-prefixed reason, is read and reported instead of
// being treated as an empty list of supported types.
func TestSecurityFailureReadsReason(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	// A reason length of 5, then the reason.
	fc.recvSecurityFailure([]byte{0, 0, 0, 5}, nil)
	go server.Write([]byte("nope!"))

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "nope!") {
			t.Fatalf("error = %v, want the server's reason", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the zero-count security list reason was not read")
	}
}

// TestFramebufferRectsKeepOrder checks the rectangles of one update are stored
// in the order they arrived. The index used to count down from the rectangle
// count, so the first rectangle ended up last.
func TestFramebufferRectsKeepOrder(t *testing.T) {
	si := &ServerInit{Width: 64, Height: 64, PixelFormat: testPixelFormat()}
	tc := newTestClient(t, si, "order", func(s net.Conn) {
		writeFrameBufferUpdateRaw(s, []Rectangle{
			{X: 10, Y: 20, Width: 2, Height: 2},
			{X: 40, Y: 50, Width: 2, Height: 2},
		})
	})
	tc.connect(t)

	bitmap := make(chan *BitRect, 1)
	tc.rf.On("bitmap", func(b *BitRect) {
		select {
		case bitmap <- b:
		default:
		}
	})

	select {
	case b := <-bitmap:
		if len(b.Rects) != 2 {
			t.Fatalf("got %d rectangles, want 2", len(b.Rects))
		}
		if b.Rects[0].Rect.X != 10 || b.Rects[1].Rect.X != 40 {
			t.Fatalf("rectangle order = %d then %d, want 10 then 40",
				b.Rects[0].Rect.X, b.Rects[1].Rect.X)
		}
	case err := <-tc.errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the framebuffer update")
	}
}

// TestRectHeaderRejectsUnsupportedEncoding checks a rectangle this client did
// not advertise is reported rather than read with a length guessed for another
// encoding, which would misread the rest of the update. RRE (2) is the example:
// CopyRect (1) and Hextile (5) are implemented and are not rejected.
func TestRectHeaderRejectsUnsupportedEncoding(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")
	fc.BitRect.Pf = NewPixelFormat()
	fc.NbRect = 1
	fc.BitRect.Rects = make([]Rectangles, 1)

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	// RRE (encoding 2) has its own body, not width*height*4 bytes.
	header := []byte{0, 0, 0, 0, 0, 10, 0, 10, 0, 0, 0, 2}
	fc.recvRectHeader(header, nil)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "unsupported rectangle encoding 2") {
			t.Fatalf("error = %v, want an unsupported-encoding error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unsupported rectangle encoding was not reported")
	}
}

// TestRectHeaderUsesPixelFormatBpp checks the body length is computed from the
// active pixel format. With 16 bpp a 4x2 rectangle is 16 bytes; the parser used
// to read width*height*4 regardless, which would overrun into the next message.
func TestRectHeaderUsesPixelFormatBpp(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")
	fc.s = &ServerInit{Width: 4, Height: 2}
	fc.BitRect.Pf = &PixelFormat{BitsPerPixel: 16, Depth: 16}
	fc.NbRect = 1
	fc.BitRect.Rects = make([]Rectangles, 1)
	if err := fc.initFramebuffer(); err != nil {
		t.Fatalf("initFramebuffer: %v", err)
	}

	bitmap := make(chan *BitRect, 1)
	fc.On("bitmap", func(b *BitRect) {
		select {
		case bitmap <- b:
		default:
		}
	})

	// A Raw 4x2 rectangle: the body is 4*2*2 = 16 bytes, not 32.
	header := []byte{0, 0, 0, 0, 0, 4, 0, 2, 0, 0, 0, 0}
	fc.recvRectHeader(header, nil)
	go func() {
		server.Write(make([]byte, 16)) // exactly the 16 bpp body
		discard(server, 10)            // the client's next update request
	}()

	select {
	case b := <-bitmap:
		if len(b.Rects[0].Data) != 16 {
			t.Fatalf("rectangle body = %d bytes, want 16 for 16 bpp", len(b.Rects[0].Data))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: the rectangle body was not read at 16 bpp")
	}
}

// TestSendSetEncodingsWireFormat pins SetEncodings to the bytes a server reads:
// message type 2, padding, a count of three and the three 32 bit encoding ids.
// The message is one list with a count, so all three encodings have to be in it
// rather than in a second message.
func TestSendSetEncodingsWireFormat(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	fc := NewRFBConn(client, "")

	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16)
		if _, err := io.ReadFull(server, buf); err == nil {
			got <- buf
		}
	}()

	fc.sendSetEncoding()

	want := []byte{
		0x02, 0x00, 0x00, 0x03, // SetEncodings, padding, three encodings
		0x00, 0x00, 0x00, 0x00, // Raw
		0x00, 0x00, 0x00, 0x01, // CopyRect
		0x00, 0x00, 0x00, 0x05, // Hextile
	}
	select {
	case b := <-got:
		if !bytes.Equal(b, want) {
			t.Fatalf("SetEncodings = % x, want % x", b, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the SetEncodings message")
	}
}

// lePixel is a four byte little-endian pixel with distinct channel bytes, so a
// decoder that wrote the wrong channel or at the wrong offset is visible.
func lePixel(r, g, b byte) []byte { return []byte{r, g, b, 0} }

// repeatPixel returns count copies of one pixel.
func repeatPixel(p []byte, count int) []byte {
	out := make([]byte, 0, len(p)*count)
	for i := 0; i < count; i++ {
		out = append(out, p...)
	}
	return out
}

// decodeHextileBody decodes a Hextile body at four bytes per pixel and returns
// the rectangle's pixels, failing the test on a decode error.
func decodeHextileBody(t *testing.T, body []byte, w, h int) []byte {
	t.Helper()
	data := make([]byte, w*h*4)
	if err := decodeHextile(bytes.NewReader(body), data, w, h, 4); err != nil {
		t.Fatalf("decodeHextile(%dx%d): %v", w, h, err)
	}
	return data
}

// rectHeader encodes a rectangle header the way a server writes it.
func rectHeader(r Rectangle) []byte {
	b := &bytes.Buffer{}
	core.WriteUInt16BE(r.X, b)
	core.WriteUInt16BE(r.Y, b)
	core.WriteUInt16BE(r.Width, b)
	core.WriteUInt16BE(r.Height, b)
	core.WriteUInt32BE(r.Encoding, b)
	return b.Bytes()
}

// framedTestConn returns an RFBConn over a net.Pipe whose framebuffer is w by h
// at 32 bpp. A goroutine drains everything the client writes, so the update
// request that follows a rectangle does not block the test.
func framedTestConn(t *testing.T, w, h int) (*RFBConn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	fc := NewRFBConn(client, "")
	fc.s = &ServerInit{Width: uint16(w), Height: uint16(h)}
	fc.BitRect.Pf = NewPixelFormat()
	if err := fc.initFramebuffer(); err != nil {
		t.Fatalf("initFramebuffer: %v", err)
	}
	go io.Copy(io.Discard, server)
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return fc, server
}

// TestDecodeHextileBackgroundOnly checks a tile with no subrectangles is filled
// with the background colour, and that the colour is read as the pixel format's
// bytes and not a fixed four.
func TestDecodeHextileBackgroundOnly(t *testing.T) {
	bg := lePixel(0x11, 0x22, 0x33)
	body := append([]byte{hextileBgSpecified}, bg...)

	got := decodeHextileBody(t, body, 16, 16)
	want := repeatPixel(bg, 16*16)
	if !bytes.Equal(got, want) {
		t.Fatalf("tile = % x, want the whole 16x16 tile in the background colour % x", got[:16], bg)
	}
}

// TestDecodeHextileUncolouredSubrects checks the two-colour fast path: with
// SubrectsColoured clear, every subrectangle is the one foreground colour and
// only its position and size follow on the wire.
func TestDecodeHextileUncolouredSubrects(t *testing.T) {
	bg := lePixel(0x01, 0x02, 0x03)
	fg := lePixel(0x04, 0x05, 0x06)
	// subencoding, bg, fg, one subrectangle at 0,0 that is 2x2.
	body := []byte{
		hextileBgSpecified | hextileFgSpecified | hextileAnySubrects,
		bg[0], bg[1], bg[2], bg[3],
		fg[0], fg[1], fg[2], fg[3],
		1,    // one subrectangle
		0x00, // x=0, y=0
		0x11, // 2 wide, 2 high
	}

	got := decodeHextileBody(t, body, 16, 16)
	want := repeatPixel(bg, 16*16)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			copy(want[(y*16+x)*4:], fg)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tile = % x, want the 2x2 subrectangle in the foreground colour", got[:16])
	}
}

// TestDecodeHextileColouredSubrects checks that with SubrectsColoured set one
// pixel's worth of colour precedes each subrectangle's position and size.
func TestDecodeHextileColouredSubrects(t *testing.T) {
	bg := lePixel(0x01, 0x02, 0x03)
	sub0 := lePixel(0xaa, 0xbb, 0xcc)
	sub1 := lePixel(0x0a, 0x0b, 0x0c)
	body := []byte{
		hextileBgSpecified | hextileFgSpecified | hextileAnySubrects | hextileSubrectsColoured,
		bg[0], bg[1], bg[2], bg[3],
		0x00, 0x00, 0x00, 0x00, // an unused foreground colour
		2,                                              // two subrectangles
		sub0[0], sub0[1], sub0[2], sub0[3], 0x00, 0x11, // colour, 0,0 2x2
		sub1[0], sub1[1], sub1[2], sub1[3], 0x23, 0x00, // colour, 2,3 1x1
	}

	got := decodeHextileBody(t, body, 16, 16)
	want := repeatPixel(bg, 16*16)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			copy(want[(y*16+x)*4:], sub0)
		}
	}
	copy(want[(3*16+2)*4:], sub1)
	if !bytes.Equal(got, want) {
		t.Fatalf("tile = % x, want two coloured subrectangles", got[:16])
	}
}

// TestDecodeHextileRawTile checks the raw subencoding: the tile's pixels follow
// with no tile header of their own.
func TestDecodeHextileRawTile(t *testing.T) {
	body := []byte{hextileRaw}
	for i := 0; i < 16*16; i++ {
		body = append(body, byte(i), byte(i>>8), 0, 0)
	}

	got := decodeHextileBody(t, body, 16, 16)
	if !bytes.Equal(got, body[1:]) {
		t.Fatalf("raw tile pixels differ from the bytes that carried them")
	}
}

// TestDecodeHextilePersistsColoursAcrossTiles checks the background and
// foreground colours persist across tiles when a later tile does not specify
// them. A decoder that reset them per tile would fill the second tile black.
func TestDecodeHextilePersistsColoursAcrossTiles(t *testing.T) {
	bg := lePixel(0x21, 0x22, 0x23)
	fg := lePixel(0x31, 0x32, 0x33)
	body := []byte{
		// Tile 0: background and foreground specified, one 1x1 subrect at 0,0.
		hextileBgSpecified | hextileFgSpecified | hextileAnySubrects,
		bg[0], bg[1], bg[2], bg[3],
		fg[0], fg[1], fg[2], fg[3],
		1, 0x00, 0x00,
		// Tile 1: neither colour specified, one 1x1 subrect at 0,0.
		hextileAnySubrects,
		1, 0x00, 0x00,
	}

	got := decodeHextileBody(t, body, 32, 16)
	want := repeatPixel(bg, 32*16)
	copy(want[0:4], fg)     // tile 0 at 0,0
	copy(want[(16)*4:], fg) // tile 1 at x=16
	if !bytes.Equal(got, want) {
		t.Fatalf("tile 1 = % x, want the persisted colours", got[16*4:16*4+8])
	}
}

// TestDecodeHextilePartialTile checks a rectangle smaller than a tile decodes
// only its own pixels, so a 4x2 rectangle allocates and fills 4x2.
func TestDecodeHextilePartialTile(t *testing.T) {
	bg := lePixel(0x51, 0x52, 0x53)
	body := append([]byte{hextileBgSpecified}, bg...)

	got := decodeHextileBody(t, body, 4, 2)
	if !bytes.Equal(got, repeatPixel(bg, 4*2)) {
		t.Fatalf("partial tile = % x, want 4x2 of the background colour", got)
	}
}

// TestDecodeHextileRejectsSubrectOutsideTile checks a subrectangle whose bit
// fields name an area larger than the tile is reported, not drawn as a wrap
// into the next row.
func TestDecodeHextileRejectsSubrectOutsideTile(t *testing.T) {
	body := []byte{
		hextileAnySubrects,
		1,    // one subrectangle
		0xf0, // x=15, y=0
		0xf0, // 16 wide, 1 high: 15+16 is past the 16 pixel tile
	}
	err := decodeHextile(bytes.NewReader(body), make([]byte, 16*16*4), 16, 16, 4)
	if err == nil || !strings.Contains(err.Error(), "outside a") {
		t.Fatalf("err = %v, want an out-of-tile error", err)
	}
}

// TestDecodeHextileShortBodyFails checks a truncated Hextile body is reported
// rather than decoded as far as it goes, which would leave the stream
// misaligned for the next message.
func TestDecodeHextileShortBodyFails(t *testing.T) {
	// BackgroundSpecified with no background colour behind it.
	err := decodeHextile(bytes.NewReader([]byte{hextileBgSpecified}), make([]byte, 16*16*4), 16, 16, 4)
	if err == nil {
		t.Fatal("a truncated Hextile body was not reported")
	}
}

// TestCopyRectOverlapDirections checks a CopyRect whose source and destination
// overlap copies the pixels that were there before the copy, whichever way the
// two regions overlap. Each pixel encodes its x and y so a wrong source or a
// wrong copy direction changes the bytes.
func TestCopyRectOverlapDirections(t *testing.T) {
	const w, h = 8, 4
	cases := []struct {
		name       string
		dstX, dstY int
		srcX, srcY int
		cw, ch     int
	}{
		{"down-overlap", 0, 1, 0, 0, 8, 3},
		{"up-overlap", 0, 0, 0, 1, 8, 3},
		{"same-row-overlap", 2, 0, 0, 0, 6, 1},
		{"disjoint", 0, 2, 0, 0, 4, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc, _ := framedTestConn(t, w, h)
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					copy(fc.frame[(y*w+x)*4:], lePixel(byte(x), byte(y), 0))
				}
			}
			before := append([]byte(nil), fc.frame...)

			rect := &Rectangle{X: uint16(c.dstX), Y: uint16(c.dstY), Width: uint16(c.cw), Height: uint16(c.ch), Encoding: encodingCopyRect}
			got, err := fc.copyRect(rect, c.srcX, c.srcY)
			if err != nil {
				t.Fatalf("copyRect: %v", err)
			}

			want := make([]byte, c.cw*c.ch*4)
			for row := 0; row < c.ch; row++ {
				src := ((c.srcY+row)*w + c.srcX) * 4
				copy(want[row*c.cw*4:], before[src:src+c.cw*4])
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("copied pixels = % x, want the pre-copy source % x", got, want)
			}
			for row := 0; row < c.ch; row++ {
				dst := ((c.dstY+row)*w + c.dstX) * 4
				wantRow := want[row*c.cw*4 : (row+1)*c.cw*4]
				if !bytes.Equal(fc.frame[dst:dst+c.cw*4], wantRow) {
					t.Fatalf("framebuffer row %d = % x, want % x", row, fc.frame[dst:dst+c.cw*4], wantRow)
				}
			}
		})
	}
}

// TestCopyRectRejectsSourceOutsideFrame checks a source the server names outside
// the framebuffer is reported rather than read, which would be a read past the
// end of the buffer.
func TestCopyRectRejectsSourceOutsideFrame(t *testing.T) {
	fc, _ := framedTestConn(t, 4, 4)
	rect := &Rectangle{X: 0, Y: 0, Width: 4, Height: 4, Encoding: encodingCopyRect}
	if _, err := fc.copyRect(rect, 2, 0); err == nil || !strings.Contains(err.Error(), "outside the framebuffer") {
		t.Fatalf("err = %v, want an out-of-framebuffer error", err)
	}
}

// TestRectHeaderRejectsRectangleOutsideFramebuffer checks a rectangle header
// that runs past the framebuffer is reported before its body is read.
func TestRectHeaderRejectsRectangleOutsideFramebuffer(t *testing.T) {
	fc, _ := framedTestConn(t, 4, 4)
	fc.NbRect = 1
	fc.BitRect.Rects = make([]Rectangles, 1)

	errCh := make(chan error, 1)
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	fc.recvRectHeader(rectHeader(Rectangle{X: 2, Y: 0, Width: 4, Height: 4, Encoding: encodingRaw}), nil)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "outside the framebuffer") {
			t.Fatalf("error = %v, want an out-of-framebuffer error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a rectangle outside the framebuffer was not reported")
	}
}

// TestFramebufferUpdateMixedEncodingsEndToEnd feeds one update with a Raw, a
// Hextile and a CopyRect rectangle over a scripted connection and checks the
// pixels each one produced and the framebuffer they left behind. The CopyRect
// copies the Hextile half over the Raw half, so the framebuffer ends with the
// Hextile pixels on both halves while the Raw rectangle's own data still holds
// what Raw carried.
func TestFramebufferUpdateMixedEncodingsEndToEnd(t *testing.T) {
	const w, h = 4, 4
	fc, server := framedTestConn(t, w, h)
	fc.NbRect = 3
	fc.BitRect.Rects = make([]Rectangles, 3)

	bitmap := make(chan *BitRect, 1)
	errCh := make(chan error, 1)
	fc.On("bitmap", func(b *BitRect) {
		select {
		case bitmap <- b:
		default:
		}
	})
	fc.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	patternA := make([]byte, 4*2*4)
	for i := range patternA {
		patternA[i] = byte(0xa0 + i)
	}
	patternB := lePixel(0xb1, 0xb2, 0xb3)
	hextileBody := append([]byte{hextileBgSpecified}, patternB...)

	stream := &bytes.Buffer{}
	stream.Write(patternA) // rectangle 0: Raw 4x2 at 0,0
	stream.Write(rectHeader(Rectangle{X: 0, Y: 2, Width: 4, Height: 2, Encoding: encodingHextile}))
	stream.Write(hextileBody) // rectangle 1: Hextile 4x2 at 0,2
	stream.Write(rectHeader(Rectangle{X: 0, Y: 0, Width: 4, Height: 2, Encoding: encodingCopyRect}))
	stream.Write([]byte{0, 0, 0, 2}) // rectangle 2: CopyRect 4x2 at 0,0 from 0,2
	go server.Write(stream.Bytes())

	fc.recvRectHeader(rectHeader(Rectangle{X: 0, Y: 0, Width: 4, Height: 2, Encoding: encodingRaw}), nil)

	select {
	case b := <-bitmap:
		if len(b.Rects) != 3 {
			t.Fatalf("got %d rectangles, want 3", len(b.Rects))
		}
		if !bytes.Equal(b.Rects[0].Data, patternA) {
			t.Fatalf("Raw rectangle data = % x, want % x", b.Rects[0].Data, patternA)
		}
		hextilePixels := repeatPixel(patternB, 4*2)
		if !bytes.Equal(b.Rects[1].Data, hextilePixels) {
			t.Fatalf("Hextile rectangle data = % x, want % x", b.Rects[1].Data, hextilePixels)
		}
		if !bytes.Equal(b.Rects[2].Data, hextilePixels) {
			t.Fatalf("CopyRect rectangle data = % x, want the copied Hextile pixels % x", b.Rects[2].Data, hextilePixels)
		}
		if b.Rects[2].Rect.SrcX != 0 || b.Rects[2].Rect.SrcY != 2 {
			t.Fatalf("CopyRect source = %d,%d, want 0,2", b.Rects[2].Rect.SrcX, b.Rects[2].Rect.SrcY)
		}
		wantFrame := append(append([]byte(nil), hextilePixels...), hextilePixels...)
		if !bytes.Equal(fc.Framebuffer(), wantFrame) {
			t.Fatalf("framebuffer = % x, want % x", fc.Framebuffer(), wantFrame)
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the mixed update")
	}
}

// TestInitFramebufferRejectsZeroSize checks a server that announces an empty
// desktop is reported rather than allocated.
func TestInitFramebufferRejectsZeroSize(t *testing.T) {
	fc := NewRFBConn(nil, "")
	fc.s = &ServerInit{Width: 0, Height: 10}
	fc.BitRect.Pf = NewPixelFormat()
	if err := fc.initFramebuffer(); err == nil {
		t.Fatal("a zero width desktop was not reported")
	}
}

// TestInitFramebufferRejectsOversize checks a desktop whose framebuffer would be
// larger than the bound is reported before it sizes the allocation.
func TestInitFramebufferRejectsOversize(t *testing.T) {
	fc := NewRFBConn(nil, "")
	fc.s = &ServerInit{Width: 65535, Height: 65535}
	fc.BitRect.Pf = NewPixelFormat()
	if err := fc.initFramebuffer(); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("err = %v, want an over-the-limit error", err)
	}
}

// TestInitFramebufferAllocates checks the framebuffer is the desktop's
// width*height*bytesPerPixel and its stride is one row.
func TestInitFramebufferAllocates(t *testing.T) {
	fc := NewRFBConn(nil, "")
	fc.s = &ServerInit{Width: 4, Height: 2}
	fc.BitRect.Pf = NewPixelFormat()
	if err := fc.initFramebuffer(); err != nil {
		t.Fatalf("initFramebuffer: %v", err)
	}
	if len(fc.frame) != 4*2*4 || fc.frameStride != 4*4 {
		t.Fatalf("framebuffer %d bytes, stride %d; want 32 bytes, stride 16", len(fc.frame), fc.frameStride)
	}
}
