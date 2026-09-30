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
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

// x11ScrollerSource is a tiny X11 client that maps a window, paints it with
// three colour blocks and then scrolls it with XCopyArea. The RFB CopyRect
// encoding exists because a server can tell the client that pixels moved, and
// an XCopyArea is what makes TigerVNC's X server report that move.
const x11ScrollerSource = `
#include <X11/Xlib.h>
#include <unistd.h>
int main(int argc, char **argv) {
    Display *d = XOpenDisplay(argc > 1 ? argv[1] : 0);
    if (!d) return 1;
    int s = DefaultScreen(d);
    Window root = RootWindow(d, s);
    Window win = XCreateSimpleWindow(d, root, 50, 50, 400, 300, 1,
                                     BlackPixel(d, s), WhitePixel(d, s));
    XMapWindow(d, win);
    GC gc = XCreateGC(d, win, 0, 0);
    XSetForeground(d, gc, 0xcc2222); XFillRectangle(d, win, gc, 0, 0, 200, 150);
    XSetForeground(d, gc, 0x22cc22); XFillRectangle(d, win, gc, 200, 0, 200, 150);
    XSetForeground(d, gc, 0x2222cc); XFillRectangle(d, win, gc, 0, 150, 400, 150);
    XFlush(d);
    usleep(500000);
    for (int i = 0; i < 600; i++) {
        XCopyArea(d, win, win, gc, 0, 8, 400, 292, 0, 0);
        XSetForeground(d, gc, (unsigned long)((i * 2654435761u) & 0xffffff));
        XFillRectangle(d, win, gc, 0, 292, 400, 8);
        XFlush(d);
        usleep(60000);
    }
    return 0;
}
`

// startX11Scroller compiles and starts the XCopyArea scroller on display and
// stops it when the test ends. It skips the test when there is no C compiler or
// no X11 headers, because the scroller is only how a CopyRect is provoked; the
// decoding still has to be checked whenever one is available.
//
// An X11 terminal (xterm) was tried first and does not produce a CopyRect here:
// it redraws moved lines instead of using XCopyArea, so the server never sees a
// copy to report.
func startX11Scroller(t *testing.T, display string) {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skipf("cc is not installed; the CopyRect check needs a small X11 scroller: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "scroller.c")
	bin := filepath.Join(dir, "scroller")
	if err := os.WriteFile(src, []byte(x11ScrollerSource), 0o644); err != nil {
		t.Fatalf("write scroller source: %v", err)
	}
	build := exec.Command("cc", "-o", bin, src, "-lX11")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build the X11 scroller (no X11 headers?): %v\n%s", err, out)
	}
	cmd := exec.Command(bin, display)
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start the X11 scroller: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
}

// TestLiveCopyRect drives the protocol layer against a real server until a
// CopyRect rectangle arrives, then checks the pixels it copied are the ones the
// pre-copy framebuffer held at the source the rectangle named. Scrolling a
// window with XCopyArea is what makes the server choose CopyRect, so the test
// starts the X11 scroller on the server's display and stops it afterwards.
//
// It skips when no C toolchain is available to build the scroller, or when the
// server never sends a CopyRect, naming the encodings it did send, because
// whether a server uses CopyRect is its choice.
func TestLiveCopyRect(t *testing.T) {
	addr := liveAddr(t)
	gw, gh := liveGeometry()
	if gw == 0 || gh == 0 {
		t.Skip("GRDP_VNC_WIDTH/HEIGHT are not set; the CopyRect check needs the desktop geometry")
	}
	display := os.Getenv("GRDP_VNC_XDISPLAY")
	if display == "" {
		t.Skip("GRDP_VNC_XDISPLAY is not set; the CopyRect check scrolls a window on the server's display")
	}

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

	startX11Scroller(t, display)

	// frame mirrors the connection's framebuffer, built from the pixels of every
	// rectangle that arrives, so a CopyRect can be checked against the state it
	// copied from.
	frame := make([]byte, gw*gh*4)
	seen := map[uint32]int{}
	copyRects := 0
	deadline := time.After(30 * time.Second)

	for copyRects == 0 {
		select {
		case update := <-bitmap:
			for _, r := range update.Rects {
				w, h := int(r.Rect.Width), int(r.Rect.Height)
				if len(r.Data) != w*h*4 {
					t.Fatalf("rectangle %dx%d at %d,%d carrying encoding %d has %d bytes, want %d",
						w, h, r.Rect.X, r.Rect.Y, r.Rect.Encoding, len(r.Data), w*h*4)
				}
				seen[r.Rect.Encoding]++

				var pixels []byte
				if r.Rect.Encoding == 1 { // CopyRect
					sx, sy := int(r.Rect.SrcX), int(r.Rect.SrcY)
					if sx < 0 || sy < 0 || sx+w > gw || sy+h > gh {
						t.Fatalf("CopyRect at %d,%d %dx%d names a source at %d,%d outside the %dx%d desktop",
							r.Rect.X, r.Rect.Y, w, h, sx, sy, gw, gh)
					}
					pixels = make([]byte, w*h*4)
					for row := 0; row < h; row++ {
						src := ((sy+row)*gw + sx) * 4
						copy(pixels[row*w*4:], frame[src:src+w*4])
					}
					if !bytes.Equal(pixels, r.Data) {
						t.Fatalf("CopyRect at %d,%d from %d,%d copied pixels that are not what the source held",
							r.Rect.X, r.Rect.Y, sx, sy)
					}
					copyRects++
					t.Logf("CopyRect at %d,%d %dx%d copied from %d,%d and the pixels match",
						r.Rect.X, r.Rect.Y, w, h, sx, sy)
				} else {
					pixels = r.Data
				}

				for row := 0; row < h; row++ {
					dst := ((int(r.Rect.Y)+row)*gw + int(r.Rect.X)) * 4
					copy(frame[dst:dst+w*4], pixels[row*w*4:(row+1)*w*4])
				}
			}
		case e := <-errCh:
			t.Fatalf("session error before a CopyRect: %v", e)
		case <-deadline:
			t.Skipf("the server sent no CopyRect within 30s; encodings seen: %v", seen)
		}
	}
	t.Logf("encodings seen before the CopyRect: %v", seen)
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
