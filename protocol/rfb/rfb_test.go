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

	if !readAll(8 + 10) { // SetEncodings + FramebufferUpdateRequest
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
	go serveRFBHandshake(server, si, name, after)
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
// the values the server sent.
func TestServerInitParsesBanner(t *testing.T) {
	pf := testPixelFormat()
	si := &ServerInit{Width: 0x1234, Height: 0x5678, PixelFormat: pf}
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
	if tc.fc.s.Width != 0x1234 || tc.fc.s.Height != 0x5678 {
		t.Fatalf("size = %d x %d, want 0x1234 x 0x5678", tc.fc.s.Width, tc.fc.s.Height)
	}
	got := tc.fc.BitRect.Pf
	if got == nil {
		t.Fatal("pixel format was not stored")
	}
	if got.RedMax != pf.RedMax || got.GreenMax != pf.GreenMax || got.BlueShift != pf.BlueShift {
		t.Fatalf("pixel format = %+v, want red max %#x, green max %#x, blue shift %d",
			got, pf.RedMax, pf.GreenMax, pf.BlueShift)
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
