package rfb_test

// Live tests for the RFB client. They run only when GRDP_VNC_ADDR names a
// running VNC server; scripts/vnc-dev.sh starts TigerVNC's Xtigervnc and sets
// it. Without that, every test here skips, so `go test ./...` stays offline.
//
// This file is package rfb_test rather than rfb on purpose: an external test
// package may import client, which imports protocol/rfb, without an import
// cycle. That is what lets a test drive the client/rfb.go wrapper itself, over
// a real socket, rather than the protocol layer alone.

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adfnekc/grdp/client"
	"github.com/adfnekc/grdp/protocol/rfb"
)

func liveAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("GRDP_VNC_ADDR")
	if addr == "" {
		t.Skip("set GRDP_VNC_ADDR (scripts/vnc-dev.sh) to run the live VNC tests")
	}
	return addr
}

func livePassword() string { return os.Getenv("GRDP_VNC_PASSWORD") }

// liveGeometry returns the framebuffer size the server was started with, or
// (0, 0) when it was not exported and the bounds check should be skipped.
func liveGeometry() (int, int) {
	w, _ := strconv.Atoi(os.Getenv("GRDP_VNC_WIDTH"))
	h, _ := strconv.Atoi(os.Getenv("GRDP_VNC_HEIGHT"))
	return w, h
}

// TestLiveFramebufferUpdate drives the protocol layer to one full framebuffer
// update against a real server. It checks that the rectangles tile the screen,
// that every body is the size its geometry implies for 32 bpp, and that the
// rectangles arrive in the order the server sent them.
func TestLiveFramebufferUpdate(t *testing.T) {
	addr := liveAddr(t)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()

	rf := rfb.NewRFB(rfb.NewRFBConn(conn, livePassword()))
	ready := make(chan struct{}, 1)
	bitmap := make(chan *rfb.BitRect, 4)
	errCh := make(chan error, 4)
	rf.On("ready", func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
	rf.On("bitmap", func(b *rfb.BitRect) {
		select {
		case bitmap <- b:
		default:
		}
	})
	rf.On("error", func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	if err := rf.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-ready:
	case e := <-errCh:
		t.Fatalf("handshake failed: %v", e)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the handshake to become ready")
	}

	var update *rfb.BitRect
	select {
	case update = <-bitmap:
	case e := <-errCh:
		t.Fatalf("session error before a framebuffer update: %v", e)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a framebuffer update")
	}

	if len(update.Rects) == 0 {
		t.Fatal("the first framebuffer update carried no rectangles")
	}
	t.Logf("live framebuffer: %d rectangle(s)", len(update.Rects))

	prevX, prevY := -1, -1
	covered := 0
	maxX, maxY := 0, 0
	for i, r := range update.Rects {
		w, h := int(r.Rect.Width), int(r.Rect.Height)
		if w <= 0 || h <= 0 {
			t.Fatalf("rectangle %d has an empty geometry %dx%d", i, w, h)
		}
		if want := w * h * 4; len(r.Data) != want {
			t.Fatalf("rectangle %d at %d,%d is %dx%d with %d body bytes, want %d at 32 bpp",
				i, r.Rect.X, r.Rect.Y, w, h, len(r.Data), want)
		}
		x, y := int(r.Rect.X), int(r.Rect.Y)
		// The server sends rectangles in draw order; the client must keep it.
		if y < prevY || (y == prevY && x < prevX) {
			t.Fatalf("rectangle %d at %d,%d is out of order after %d,%d", i, x, y, prevX, prevY)
		}
		prevX, prevY = x, y
		covered += w * h
		if x+w > maxX {
			maxX = x + w
		}
		if y+h > maxY {
			maxY = y + h
		}
	}

	if gw, gh := liveGeometry(); gw > 0 && gh > 0 {
		if covered != gw*gh {
			t.Fatalf("the first update covers %d pixels, want the whole %dx%d screen (%d)",
				covered, gw, gh, gw*gh)
		}
		if maxX != gw || maxY != gh {
			t.Fatalf("the first update reaches %d,%d, want %dx%d", maxX, maxY, gw, gh)
		}
	}
}

// TestLiveClientClipboardBothWays drives client/rfb.go against a real server:
// it logs in through the public Client API, receives a framebuffer update, then
// checks the clipboard in both directions using xclip on the server's display.
// Only one framebuffer update is required; the clipboard needs a change on the
// server side to be sent, which xclip provides.
func TestLiveClientClipboardBothWays(t *testing.T) {
	addr := liveAddr(t)

	display := os.Getenv("GRDP_VNC_XDISPLAY")
	if display == "" {
		t.Skip("GRDP_VNC_XDISPLAY is not set; the clipboard check needs the server's X display")
	}
	if _, err := exec.LookPath("xclip"); err != nil {
		t.Skipf("xclip is not installed: %v", err)
	}

	cl := client.NewClient(addr, "", livePassword(), client.TC_VNC, nil)
	defer cl.Close()

	bitmap := make(chan []client.Bitmap, 4)
	clip := make(chan string, 4)
	errCh := make(chan error, 4)

	// Registered before Login on purpose: the VNC client holds listeners until
	// the connection exists, which is what makes this order work.
	cl.OnBitmap(func(bs []client.Bitmap) {
		select {
		case bitmap <- bs:
		default:
		}
	})
	cl.OnClipboardText(func(text string) {
		select {
		case clip <- text:
		default:
		}
	})
	cl.OnError(func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	if err := cl.Login(); err != nil {
		t.Fatalf("client.Login: %v", err)
	}

	select {
	case bs := <-bitmap:
		if len(bs) == 0 {
			t.Fatal("the first update through the client carried no rectangles")
		}

	case e := <-errCh:
		t.Fatalf("session error before a framebuffer update: %v", e)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a framebuffer update through the client")
	}

	// Client to server: publish, then read the selection back with xclip while
	// the connection is still open - Xvnc serves the selection only while the
	// client that set it is connected.
	const published = "grdp-live-clip"
	if err := cl.SetClipboardText(published); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	var readBack string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := xclipGet(display); err == nil && s == published {
			readBack = s
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if readBack != published {
		t.Fatalf("xclip read %q from the server's clipboard, want %q", readBack, published)
	}
	t.Logf("client to server clipboard verified: %q", readBack)

	// Server to client: change the server's clipboard and wait for the event.
	const fromServer = "grdp-server-clip"
	if err := xclipSet(display, fromServer); err != nil {
		t.Fatalf("xclip set: %v", err)
	}
	select {
	case s := <-clip:
		if s != fromServer {
			t.Fatalf("clipboard-text = %q, want %q", s, fromServer)
		}
		t.Logf("server to client clipboard verified: %q", s)
	case e := <-errCh:
		t.Fatalf("session error waiting for the clipboard event: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("no clipboard-text event after the server's clipboard changed")
	}
}

// TestLiveRequestClipboardTextIsUnsupported checks the honest answer RFB can
// give for a request the protocol has no message for.
func TestLiveRequestClipboardTextIsUnsupported(t *testing.T) {
	addr := liveAddr(t)

	cl := client.NewClient(addr, "", livePassword(), client.TC_VNC, nil)
	defer cl.Close()
	if err := cl.Login(); err != nil {
		t.Fatalf("client.Login: %v", err)
	}
	if err := cl.RequestClipboardText(); err == nil {
		t.Fatal("RequestClipboardText returned nil; RFB has no request message")
	}
}

func xclipGet(display string) (string, error) {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-o")
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\x00"), nil
}

func xclipSet(display, text string) error {
	cmd := exec.Command("xclip", "-selection", "clipboard")
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
