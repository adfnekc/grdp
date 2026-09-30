// Command rdpws is a headless RDP gateway: it holds an RDP session and serves
// the screen to a browser over a WebSocket, taking the browser's keyboard and
// mouse back the other way.
//
// It exists because the library had no headless example. The two that shipped
// were a desktop program and a socket.io page from the library's early days, so
// nothing showed how to drive a session with no window in front of it: which is
// exactly what a gateway is. This is that, in as little code as it takes, and it
// doubles as an integration test because the selftest below drives it through
// the same WebSocket a browser would.
//
// The three things it exists to demonstrate:
//
//   - Client.Framebuffer plus Client.OnFrame, so the pixels come from one place
//     whichever drawing path the server chose, and the dirty region arrives
//     merged and clipped rather than as a rectangle per update.
//   - Client.OnCursor, so the pointer is drawn over the desktop instead of being
//     lost, including when the server sends a cached shape or names a system
//     cursor.
//   - Client.TypeText, so text arrives as characters rather than as scancodes,
//     which is the only way a browser's input method can be passed through.
//
// The wire format to the browser is deliberately tiny: a one byte type and four
// little endian uint16s, then an image. Everything else goes as JSON text.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/adfnekc/grdp/client"
	"github.com/adfnekc/grdp/glog"
	"github.com/gorilla/websocket"
)

// Message types on the binary channel.
const (
	msgFrame  = 1 // a JPEG of one dirty rectangle
	msgCursor = 2 // a PNG of the pointer, with alpha
	msgSize   = 3 // the desktop changed size
)

// binaryHeader is a type, a position and a size: five uint16s.
const binaryHeader = 10

// gateway is one RDP session and the browsers watching it.
type gateway struct {
	client *client.Client
	quality int

	mu     sync.Mutex
	conns  map[*websocket.Conn]bool
	dirty  []image.Rectangle
	// frames counts what has been encoded, which is what the selftest asserts
	// on: a gateway that connected and produced nothing is not working.
	frames int
	lastFrame []byte
}

// addDirty accumulates the regions that changed since the last encode.
//
// The library has already merged and clipped them, so the only work here is to
// fold the new ones into what has not been sent yet. A gateway that encoded
// every callback would send a frame per update, and an update is often a few
// hundred pixels of a text cursor.
func (g *gateway) addDirty(rects []image.Rectangle) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range rects {
		g.dirty = client.MergeDirty(g.dirty, r, g.client.Framebuffer().Bounds())
	}
}

// send writes a binary message to every connected browser, dropping any that is
// too slow rather than blocking the session on a browser tab.
func (g *gateway) send(msg []byte) {
	g.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	for _, c := range conns {
		if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			g.mu.Lock()
			delete(g.conns, c)
			g.mu.Unlock()
			c.Close()
		}
	}
}

// encodeFrame turns one dirty rectangle into a JPEG and sends it. The message
// carries the rectangle's position so the browser can put it where it belongs
// rather than assuming it starts at the corner.
func (g *gateway) encodeFrame(r image.Rectangle, fb *client.Framebuffer) ([]byte, error) {
	bounds := fb.Bounds()
	r = r.Intersect(bounds)
	if r.Empty() {
		return nil, nil
	}
	w, h := r.Dx(), r.Dy()
	pix := fb.Pix()
	stride := fb.Stride()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		src := (r.Min.Y+y)*stride + r.Min.X*4
		dst := y * img.Stride
		if src+4*w > len(pix) {
			return nil, nil
		}
		for x := 0; x < w; x++ {
			o := src + x*4
			img.Pix[dst+x*4] = pix[o+2]
			img.Pix[dst+x*4+1] = pix[o+1]
			img.Pix[dst+x*4+2] = pix[o]
			img.Pix[dst+x*4+3] = 0xff
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: g.quality}); err != nil {
		return nil, err
	}
	msg := make([]byte, binaryHeader, binaryHeader+jpg.Len())
	binary.LittleEndian.PutUint16(msg[0:2], msgFrame)
	binary.LittleEndian.PutUint16(msg[2:4], uint16(r.Min.X))
	binary.LittleEndian.PutUint16(msg[4:6], uint16(r.Min.Y))
	binary.LittleEndian.PutUint16(msg[6:8], uint16(w))
	binary.LittleEndian.PutUint16(msg[8:10], uint16(h))
	return append(msg, jpg.Bytes()...), nil
}

// flush encodes everything accumulated since the last call and sends it.
func (g *gateway) flush() {
	g.mu.Lock()
	dirty := g.dirty
	g.dirty = nil
	g.mu.Unlock()
	fb := g.client.Framebuffer()
	if fb == nil {
		return
	}
	for _, r := range dirty {
		msg, err := g.encodeFrame(r, fb)
		if err != nil {
			log.Printf("encode %v: %v", r, err)
			continue
		}
		if msg == nil {
			continue
		}
		g.mu.Lock()
		g.frames++
		g.lastFrame = msg
		g.mu.Unlock()
		g.send(msg)
	}
}

// encodeCursor turns the pointer into a PNG with alpha, which is what lets a
// browser draw it over the desktop without a background. A system cursor has no
// pixels, so only its type is sent and the page substitutes its own.
func encodeCursor(cur *client.Cursor) []byte {
	if cur.System {
		// A system cursor has no pixels, so this is a marker rather than an
		// image. It is told apart by having no height, which no real image can
		// have; a width of one would also be shared by a one pixel wide cursor,
		// and hiding that would be wrong.
		buf := make([]byte, binaryHeader)
		binary.LittleEndian.PutUint16(buf[0:2], msgCursor)
		binary.LittleEndian.PutUint16(buf[2:4], uint16(cur.SystemType>>16))
		binary.LittleEndian.PutUint16(buf[4:6], uint16(cur.SystemType))
		return buf
	}
	var img bytes.Buffer
	if err := png.Encode(&img, cur.Image); err != nil {
		return nil
	}
	buf := make([]byte, binaryHeader, binaryHeader+img.Len())
	binary.LittleEndian.PutUint16(buf[0:2], msgCursor)
	binary.LittleEndian.PutUint16(buf[2:4], uint16(cur.Hotspot.X))
	binary.LittleEndian.PutUint16(buf[4:6], uint16(cur.Hotspot.Y))
	binary.LittleEndian.PutUint16(buf[6:8], uint16(cur.Width))
	binary.LittleEndian.PutUint16(buf[8:10], uint16(cur.Height))
	return append(buf, img.Bytes()...)
}

// input is what the browser sends back.
type input struct {
	Type   string `json:"type"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button int    `json:"button"`
	Scroll int    `json:"scroll"`
	Scan   int    `json:"scan"`
	Text   string `json:"text"`
}

// scancodes the browser's key events are mapped to. A gateway normally sends
// scancodes for keys and characters for text: the browser reports both, and a
// shortcut like control-c only works as a scancode.
var browserScancodes = map[string]int{
	"Enter": 0x1c, "Escape": 0x01, "Backspace": 0x0e, "Tab": 0x0f, " ": 0x39,
	"ArrowLeft": 0xe04b, "ArrowRight": 0xe04d, "ArrowUp": 0xe048, "ArrowDown": 0xe050,
	"Delete": 0xe053, "Home": 0xe047, "End": 0xe04f,
	"Control": 0x1d, "Shift": 0x2a, "Alt": 0x38,
}

func (g *gateway) handleInput(raw []byte) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		log.Printf("input: %v", err)
		return
	}
	c := g.client
	switch in.Type {
	case "move":
		c.MouseMove(in.X, in.Y)
	case "down":
		c.MouseDown(in.Button, in.X, in.Y)
	case "up":
		c.MouseUp(in.Button, in.X, in.Y)
	case "wheel":
		c.MouseWheel(in.Scroll, in.X, in.Y)
	case "keydown":
		c.KeyDown(in.Scan, "")
	case "keyup":
		c.KeyUp(in.Scan, "")
	case "text":
		// Characters rather than scancodes, so that what the browser's input
		// method composed arrives as it was composed.
		if err := c.TypeText(in.Text, 8*time.Millisecond); err != nil {
			log.Printf("text: %v", err)
		}
	case "refresh":
		// The page asks for a whole frame, which it needs after clearing its
		// canvas: resizing a canvas clears it, so a browser that has just been
		// resized would otherwise show the parts of the desktop that happened
		// to be redrawn and nothing else. Asking is explicit, where the
		// alternative is the page hoping something will happen to redraw it.
		if fb := g.client.Framebuffer(); fb != nil {
			bounds := fb.Bounds()
			g.mu.Lock()
			g.dirty = client.MergeDirty(g.dirty, bounds, bounds)
			g.mu.Unlock()
		}
	default:
		log.Printf("input: unknown type %q", in.Type)
	}
}

var upgrader = websocket.Upgrader{
	// The page is served from here, so a browser's origin check would pass
	// anyway; it is disabled because this is an example that is meant to be
	// reached from wherever it is running.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (g *gateway) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	g.mu.Lock()
	g.conns[conn] = true
	g.mu.Unlock()

	// Tell the newcomer how big the desktop is and send it a whole frame, so
	// that it does not have to wait for something to change.
	if fb := g.client.Framebuffer(); fb != nil {
		bounds := fb.Bounds()
		size, _ := json.Marshal(map[string]interface{}{
			"type": "size", "w": bounds.Dx(), "h": bounds.Dy(),
		})
		conn.WriteMessage(websocket.TextMessage, size)
		g.mu.Lock()
		g.dirty = client.MergeDirty(g.dirty, bounds, bounds)
		g.mu.Unlock()
		g.flush()
	}

	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.conns, conn)
			g.mu.Unlock()
			conn.Close()
		}()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			g.handleInput(msg)
		}
	}()
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to serve on")
	host := flag.String("host", "127.0.0.1:3389", "RDP host to connect to")
	user := flag.String("user", "", "RDP user")
	pass := flag.String("pass", "", "RDP password")
	proto := flag.String("proto", "tls", "security protocol: auto|tls|nla|rdp")
	width := flag.Int("width", 1024, "desktop width to ask for")
	height := flag.Int("height", 768, "desktop height to ask for")
	quality := flag.Int("quality", 70, "JPEG quality for frames, 1 to 100")
	fps := flag.Int("fps", 20, "how many times a second to encode accumulated changes")
	selftest := flag.Bool("selftest", false, "connect, serve, drive one frame through the WebSocket, and exit")
	logLevel := flag.Int("log", int(glog.INFO), "log level 0=TRACE..5=NONE")
	flag.Parse()

	glog.SetLevel(glog.LEVEL(*logLevel))

	setting := client.NewSetting()
	setting.Width, setting.Height = *width, *height
	setting.Protocol = *proto
	setting.EnableClipboard = true
	// EGFX and orders are off by default and stay off here: this example is
	// about the gateway, and the default bitmap path is the one that works
	// against everything.

	c := client.NewClient(*host+":3389", *user, *pass, client.TC_RDP, setting)
	g := &gateway{client: c, quality: *quality, conns: map[*websocket.Conn]bool{}}

	c.OnError(func(err error) { log.Printf("session error: %v", err) })
	c.OnClose(func() { log.Printf("session closed") })
	c.OnFrame(g.addDirty)
	c.OnResize(func(w, h int) {
		msg, _ := json.Marshal(map[string]interface{}{"type": "size", "w": w, "h": h})
		g.send(msg)
	})
	c.OnCursor(func(cur *client.Cursor) {
		if cur.Hidden() {
			g.send([]byte(`{"type":"cursor","hidden":true}`))
			return
		}
		if msg := encodeCursor(cur); msg != nil {
			g.send(msg)
		}
	})
	c.OnClipboardText(func(text string) {
		msg, _ := json.Marshal(map[string]interface{}{"type": "clipboard", "text": text})
		g.send(msg)
	})

	fmt.Printf("connecting to %s as %q ...\n", *host, *user)
	if err := c.Login(); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("session ready")

	// Encoding on a timer rather than per update is the frame pacing: a
	// server that sends a hundred small updates a second should not become a
	// hundred JPEGs, and a browser that draws them all should not have to.
	go func() {
		tick := time.NewTicker(time.Second / time.Duration(*fps))
		defer tick.Stop()
		for range tick.C {
			g.flush()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", g.serveWS)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})

	ln, err := newListener(*listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("serving on http://%s/\n", ln.Addr())

	if *selftest {
		go http.Serve(ln, mux)
		os.Exit(runSelftest(g, ln))
	}
	if err := http.Serve(ln, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

// page is the whole browser side: a canvas, the cursor drawn over it, and the
// events sent back. It is inline so that the example is one file to read.
const page = `<!doctype html>
<html><head><meta charset="utf-8"><title>grdp gateway</title>
<style>
  body { margin: 0; background: #202020; color: #ddd; font: 13px system-ui, sans-serif; }
  #wrap { position: relative; display: inline-block; }
  canvas { display: block; cursor: none; }
  #cursor { position: absolute; pointer-events: none; image-rendering: pixelated; }
  #bar { padding: 6px 8px; }
  input { width: 24em; background: #333; color: #eee; border: 1px solid #555; padding: 3px; }
</style></head><body>
<div id="bar">
  <span id="status">connecting…</span>
  &nbsp;<input id="text" placeholder="type here (any script) and press Enter" autofocus>
</div>
<div id="wrap">
  <canvas id="screen" width="1024" height="768"></canvas>
  <img id="cursor" alt="">
</div>
<script>
const ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws');
ws.binaryType = 'arraybuffer';
const screen = document.getElementById('screen');
const ctx = screen.getContext('2d');
const cursor = document.getElementById('cursor');
const status = document.getElementById('status');
let scale = 1, offX = 0, offY = 0, natural = {w: 1024, h: 768};

function fit() {
  // Scale the canvas to the window, keeping the remote aspect ratio, so the
  // whole desktop is usable on any page size.
  const availW = window.innerWidth - 24, availH = window.innerHeight - 70;
  scale = Math.min(availW / natural.w, availH / natural.h);
  screen.style.width = (natural.w * scale) + 'px';
  screen.style.height = (natural.h * scale) + 'px';
  document.getElementById('wrap').style.width = (natural.w * scale) + 'px';
}
window.addEventListener('resize', fit);

ws.onmessage = (ev) => {
  if (typeof ev.data === 'string') {
    const msg = JSON.parse(ev.data);
    if (msg.type === 'size') {
      natural = {w: msg.w, h: msg.h};
      if (screen.width !== msg.w || screen.height !== msg.h) {
        // Resizing a canvas clears it, so ask for a whole frame instead of
        // waiting to see which parts of the desktop happen to be redrawn.
        screen.width = msg.w; screen.height = msg.h;
        send({type: 'refresh'});
      }
      fit();
      status.textContent = msg.w + 'x' + msg.h;
    } else if (msg.type === 'cursor' && msg.hidden) {
      cursor.style.display = 'none';
    } else if (msg.type === 'clipboard') {
      status.textContent = 'clipboard: ' + msg.text.slice(0, 40);
    }
    return;
  }
  const dv = new DataView(ev.data);
  const type = dv.getUint16(0, true);
  const a = dv.getUint16(2, true), b = dv.getUint16(4, true);
  const w = dv.getUint16(6, true), h = dv.getUint16(8, true);
  const body = new Uint8Array(ev.data, 10);
  const blob = new Blob([body]);
  if (type === 1) {
    // A dirty rectangle of the desktop.
    createImageBitmap(blob).then((bmp) => ctx.drawImage(bmp, a, b));
  } else if (type === 2) {
    // No height means a system cursor, which has no pixels: a fortieth field is
    // its type, and the page substitutes its own artwork. Hiding it is the
    // simple correct thing and what this does.
    if (h === 0) { cursor.style.display = 'none'; return; }
    const url = URL.createObjectURL(blob);
    cursor.onload = () => {
      cursor.style.display = 'block';
      cursor.style.width = (cursor.naturalWidth * scale) + 'px';
      cursor.style.height = (cursor.naturalHeight * scale) + 'px';
      // The hotspot is where the pointer actually is, so it is what has to
      // land on the mouse position.
      cursor.dataset.hx = a; cursor.dataset.hy = b;
      URL.revokeObjectURL(url);
      positionCursor();
    };
    cursor.src = url;
  }
};

let lastCursor = {x: 0, y: 0};
function positionCursor() {
  const hx = Number(cursor.dataset.hx || 0) * scale;
  const hy = Number(cursor.dataset.hy || 0) * scale;
  cursor.style.left = (lastCursor.x - hx) + 'px';
  cursor.style.top = (lastCursor.y - hy) + 'px';
}

function toRemote(ev) {
  const r = screen.getBoundingClientRect();
  return {
    x: Math.round((ev.clientX - r.left) / scale),
    y: Math.round((ev.clientY - r.top) / scale),
  };
}
function send(o) { if (ws.readyState === 1) ws.send(JSON.stringify(o)); }

screen.addEventListener('mousemove', (ev) => {
  const p = toRemote(ev);
  lastCursor = {x: ev.clientX - screen.getBoundingClientRect().left,
                y: ev.clientY - screen.getBoundingClientRect().top};
  positionCursor();
  send({type: 'move', x: p.x, y: p.y});
});
screen.addEventListener('mousedown', (ev) => { const p = toRemote(ev); send({type: 'down', button: ev.button, x: p.x, y: p.y}); });
screen.addEventListener('mouseup',   (ev) => { const p = toRemote(ev); send({type: 'up',   button: ev.button, x: p.x, y: p.y}); });
screen.addEventListener('wheel',     (ev) => { const p = toRemote(ev); send({type: 'wheel', scroll: ev.deltaY < 0 ? 1 : -1, x: p.x, y: p.y}); ev.preventDefault(); }, {passive: false});
screen.addEventListener('contextmenu', (ev) => ev.preventDefault());

const SCAN = {Enter: 0x1c, Escape: 0x01, Backspace: 0x0e, Tab: 0x0f, ' ': 0x39,
  ArrowLeft: 0xe04b, ArrowRight: 0xe04d, ArrowUp: 0xe048, ArrowDown: 0xe050,
  Delete: 0xe053, Home: 0xe047, End: 0xe04f, Control: 0x1d, Shift: 0x2a, Alt: 0x38};

// Keys go as scancodes so that shortcuts work; the text box goes as characters
// so that an input method can compose a character the keyboard has no key for.
window.addEventListener('keydown', (ev) => {
  if (document.activeElement === document.getElementById('text')) return;
  const sc = SCAN[ev.key];
  if (sc !== undefined) { send({type: 'keydown', scan: sc}); ev.preventDefault(); }
});
window.addEventListener('keyup', (ev) => {
  if (document.activeElement === document.getElementById('text')) return;
  const sc = SCAN[ev.key];
  if (sc !== undefined) send({type: 'keyup', scan: sc});
});
const text = document.getElementById('text');
text.addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter') {
    send({type: 'text', text: text.value + '\n'});
    text.value = '';
    ev.preventDefault();
  }
});

fit();
</script></body></html>
`

// newListener exists so that the selftest can learn the port it was given when
// it asked for port zero.
func newListener(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// runSelftest drives the gateway through the same WebSocket a browser would, so
// that "it serves frames" is something that can be run rather than read. It
// returns a process exit code.
func runSelftest(g *gateway, ln net.Listener) int {
	url := "ws://" + ln.Addr().String() + "/ws"
	var conn *websocket.Conn
	var err error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err = websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "selftest: dial %s: %v\n", url, err)
		return 1
	}
	defer conn.Close()

	// The page is what a browser is served, so it is checked too.
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil || resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "selftest: GET / failed: %v %v\n", err, resp)
		return 1
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte("<canvas")) {
		fmt.Fprintln(os.Stderr, "selftest: the page served has no canvas")
		return 1
	}

	sawSize, connMsgs, cursors := false, 0, 0
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for connMsgs < 60 {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Fprintf(os.Stderr, "selftest: read: %v\n", err)
			return 1
		}
		connMsgs++
		if len(msg) > 0 && msg[0] == 0x7b { // a JSON control message
			if bytes.Contains(msg, []byte(`"size"`)) {
				sawSize = true
			}
			continue
		}
		if len(msg) < binaryHeader {
			continue
		}
		if t := binary.LittleEndian.Uint16(msg[0:2]); t == msgCursor {
			// The pointer is a separate stream from the desktop, and it is
			// the thing a browser cannot reconstruct from frames.
			if _, err := png.Decode(bytes.NewReader(msg[binaryHeader:])); err == nil {
				cursors++
			}
			continue
		} else if t != msgFrame {
			continue
		}
		w := int(binary.LittleEndian.Uint16(msg[6:8]))
		h := int(binary.LittleEndian.Uint16(msg[8:10]))
		img, err := jpeg.Decode(bytes.NewReader(msg[binaryHeader:]))
		if err != nil {
			fmt.Fprintf(os.Stderr, "selftest: the frame did not decode as JPEG: %v\n", err)
			return 1
		}
		if img.Bounds().Dx() == 0 || img.Bounds().Dy() == 0 {
			fmt.Fprintln(os.Stderr, "selftest: the frame decoded to nothing")
			return 1
		}
		// Drive one input event back the other way, which is what says the
		// socket is two way rather than a stream of pictures.
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"move","x":10,"y":10}`)); err != nil {
			fmt.Fprintf(os.Stderr, "selftest: write input: %v\n", err)
			return 1
		}
		// The pointer arrives when it changes shape, which is after the first
		// frames rather than with them, so a short window is given to it. The
		// wait is bounded and its result is reported either way: claiming a
		// cursor was seen when the deadline passed is the one thing this must
		// not do.
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if len(msg) < binaryHeader {
				continue
			}
			if binary.LittleEndian.Uint16(msg[0:2]) != msgCursor {
				continue
			}
			if _, err := png.Decode(bytes.NewReader(msg[binaryHeader:])); err == nil {
				cursors++
			}
		}
		// A read deadline that expires leaves the connection unusable for reading,
		// so this one is not talked to again after the window above. The refresh
		// is checked on a connection of its own below.
		g.mu.Lock()
		frames := g.frames
		g.mu.Unlock()
		// The first frame a newcomer gets is the whole desktop, so its size is
		// the desktop size.
		refreshed := checkRefresh(ln, w, h)
		fmt.Printf("selftest: ok — page has a canvas, %s, %d bytes as JPEG (%dx%d), "+
			"%d cursor images, %d frames encoded, full frame on request: %v\n",
			map[bool]string{true: "size announced", false: "no size"}[sawSize],
			len(msg)-binaryHeader, w, h, cursors, frames, refreshed)
		if cursors == 0 {
			// Not a failure: a session whose pointer never changed shape has
			// nothing to send. Saying so is better than implying it was seen.
			fmt.Println("selftest: no pointer shape arrived during the run")
		}
		if !refreshed {
			fmt.Fprintln(os.Stderr, "selftest: asking for a whole frame did not produce one")
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "selftest: no frame arrived")
	return 1
}

// frameAt reports whether a message is a frame covering the whole desktop at the
// origin, which is what a refresh has to produce and what an incremental update
// does not.
func frameAt(msg []byte, w, h int) bool {
	return len(msg) >= binaryHeader &&
		binary.LittleEndian.Uint16(msg[0:2]) == msgFrame &&
		binary.LittleEndian.Uint16(msg[2:4]) == 0 && binary.LittleEndian.Uint16(msg[4:6]) == 0 &&
		int(binary.LittleEndian.Uint16(msg[6:8])) == w &&
		int(binary.LittleEndian.Uint16(msg[8:10])) == h
}

// checkRefresh opens its own connection and asks for a whole frame.
//
// It is separate because the connection above has had a read deadline expire on
// it, which leaves it unusable for reading: a refresh checked there would be
// asking a socket that could no longer answer, and would report the feature
// broken when the test was. A newcomer gets a whole frame as soon as it
// connects, so the check is that a second one arrives when asked for.
func checkRefresh(ln net.Listener, w, h int) bool {
	url := "ws://" + ln.Addr().String() + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selftest: refresh dial: %v\n", err)
		return false
	}
	defer conn.Close()

	msgs := make(chan []byte, 64)
	go func() {
		defer close(msgs)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case msgs <- data:
			default:
			}
		}
	}()
	// Reads happen on their own goroutine and the waits below are timers, so
	// that no deadline ever expires on the socket: that is what made the first
	// version of this check misleading.
	wholeFrames := 0
	timeout := time.After(20 * time.Second)
	asked := false
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				return false
			}
			if frameAt(msg, w, h) {
				wholeFrames++
				if wholeFrames >= 2 {
					return true
				}
				if wholeFrames == 1 && !asked {
					// The whole frame that arrived on connect. Now ask for
					// another: the second one is the evidence.
					asked = true
					if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"refresh"}`)); err != nil {
						fmt.Fprintf(os.Stderr, "selftest: write refresh: %v\n", err)
						return false
					}
				}
			}
		case <-timeout:
			fmt.Fprintf(os.Stderr, "selftest: %d whole frames arrived, want 2\n", wholeFrames)
			return false
		}
	}
}
