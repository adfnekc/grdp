// Command rdpcli is a small headless RDP client used to verify connectivity
// against a live server.
//
// Usage:
//
//	go run ./cmd/rdpcli -host 127.0.0.1:3389 -user rdptest -pass secret
//	go run ./cmd/rdpcli -host host:3389 -user u -pass p -wait 30s -bitmaps 3
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adfnekc/grdp/client"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/orders"
	"github.com/adfnekc/grdp/plugin/rdpgfx"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// keySeq collects repeatable -key input events.
type keySeq []string

func (k *keySeq) String() string { return strings.Join(*k, ";") }
func (k *keySeq) Set(v string) error {
	*k = append(*k, v)
	return nil
}

// inputAction is one ordered -key/-type item, so both flags interleave in
// command-line order (flag.Var callbacks fire in the order seen).
type inputAction struct {
	isType bool
	value  string
}

type actionSeq struct {
	list   *[]inputAction
	isType bool
}

func (a actionSeq) String() string { return "" }
func (a actionSeq) Set(v string) error {
	*a.list = append(*a.list, inputAction{isType: a.isType, value: v})
	return nil
}

// asciiToScancode maps a printable ASCII character to a (scancode, needsShift)
// pair for a US QWERTY layout.
var asciiToScancode = map[rune][2]int{
	'a': {0x1E, 0}, 'b': {0x30, 0}, 'c': {0x2E, 0}, 'd': {0x20, 0}, 'e': {0x12, 0},
	'f': {0x21, 0}, 'g': {0x22, 0}, 'h': {0x23, 0}, 'i': {0x17, 0}, 'j': {0x24, 0},
	'k': {0x25, 0}, 'l': {0x26, 0}, 'm': {0x32, 0}, 'n': {0x31, 0}, 'o': {0x18, 0},
	'p': {0x19, 0}, 'q': {0x10, 0}, 'r': {0x13, 0}, 's': {0x1F, 0}, 't': {0x14, 0},
	'u': {0x16, 0}, 'v': {0x2F, 0}, 'w': {0x11, 0}, 'x': {0x2D, 0}, 'y': {0x15, 0},
	'z': {0x2C, 0},
	'0': {0x0B, 0}, '1': {0x02, 0}, '2': {0x03, 0}, '3': {0x04, 0}, '4': {0x05, 0},
	'5': {0x06, 0}, '6': {0x07, 0}, '7': {0x08, 0}, '8': {0x09, 0}, '9': {0x0A, 0},
	' ': {0x39, 0}, '-': {0x0C, 0}, '_': {0x0C, 1}, '=': {0x0D, 0}, '+': {0x0D, 1},
	'[': {0x1A, 0}, ']': {0x1B, 0}, '{': {0x1A, 1}, '}': {0x1B, 1}, '\\': {0x2B, 0},
	'|': {0x2B, 1}, ';': {0x27, 0}, ':': {0x27, 1}, '\'': {0x28, 0}, '"': {0x28, 1},
	',': {0x33, 0}, '<': {0x33, 1}, '.': {0x34, 0}, '>': {0x34, 1}, '/': {0x35, 0},
	'?': {0x35, 1}, '`': {0x29, 0}, '~': {0x29, 1}, '!': {0x02, 1}, '@': {0x03, 1},
	'#': {0x04, 1}, '$': {0x05, 1}, '%': {0x06, 1}, '^': {0x07, 1}, '&': {0x08, 1},
	'*': {0x09, 1}, '(': {0x0A, 1}, ')': {0x0B, 1},
}

const (
	scShiftLeft = 0x2A
	scEnter     = 0x1C
)

// typeString types an ASCII string into the focused window (US layout).
func typeString(c *client.Client, s string) {
	for _, ch := range s {
		if ch == '\n' {
			c.KeyDown(scEnter, "")
			c.KeyUp(scEnter, "")
			time.Sleep(40 * time.Millisecond)
			continue
		}
		// The map only lists unshifted characters, so upper case letters
		// are looked up as their lower case form with shift added. Missing
		// this silently drops every upper case letter.
		key := ch
		extraShift := 0
		if ch >= 'A' && ch <= 'Z' {
			key = ch + ('a' - 'A')
			extraShift = 1
		}
		m, ok := asciiToScancode[key]
		if !ok {
			continue
		}
		sc, shift := m[0], m[1]|extraShift
		if shift != 0 {
			c.KeyDown(scShiftLeft, "")
			time.Sleep(15 * time.Millisecond)
		}
		c.KeyDown(sc, "")
		time.Sleep(15 * time.Millisecond)
		c.KeyUp(sc, "")
		if shift != 0 {
			time.Sleep(15 * time.Millisecond)
			c.KeyUp(scShiftLeft, "")
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// playInput replays a sequence of input events after the session is ready.
// Each event is one of:
//
//	press:0x1c        key down + up (hex scancode)
//	down:0xe05b       key down
//	up:0xe05b         key up
//	move:x,y          move the pointer
//	click:x,y         move and left click
//	wait:300          pause in milliseconds
func playInput(c *client.Client, events []string) {
	for _, ev := range events {
		parts := strings.SplitN(ev, ":", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "press", "down", "up":
			sc, err := strconv.ParseInt(parts[1], 0, 32)
			if err != nil {
				fmt.Fprintln(os.Stderr, "bad scancode:", ev)
				continue
			}
			if parts[0] != "up" {
				c.KeyDown(int(sc), "")
			}
			if parts[0] != "down" {
				c.KeyUp(int(sc), "")
			}
		case "move", "click":
			xy := strings.SplitN(parts[1], ",", 2)
			if len(xy) != 2 {
				continue
			}
			x, _ := strconv.Atoi(xy[0])
			y, _ := strconv.Atoi(xy[1])
			c.MouseMove(x, y)
			time.Sleep(80 * time.Millisecond)
			if parts[0] == "click" {
				c.MouseDown(0, x, y)
				time.Sleep(80 * time.Millisecond)
				c.MouseUp(0, x, y)
			}
		case "mdown":
			xy := strings.SplitN(parts[1], ",", 2)
			if len(xy) != 2 {
				continue
			}
			x, _ := strconv.Atoi(xy[0])
			y, _ := strconv.Atoi(xy[1])
			c.MouseMove(x, y)
			time.Sleep(80 * time.Millisecond)
			c.MouseDown(0, x, y)
		case "mup":
			xy := strings.SplitN(parts[1], ",", 2)
			if len(xy) != 2 {
				continue
			}
			x, _ := strconv.Atoi(xy[0])
			y, _ := strconv.Atoi(xy[1])
			c.MouseUp(0, x, y)
		case "wait":
			ms, _ := strconv.Atoi(parts[1])
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func main() {
	host := flag.String("host", "127.0.0.1:3389", "RDP server host:port")
	user := flag.String("user", "", "username (DOMAIN\\user or user)")
	pass := flag.String("pass", "", "password")
	proto := flag.String("proto", "auto", "security protocol: auto|tls|rdp")
	width := flag.Int("width", 1024, "desktop width")
	height := flag.Int("height", 768, "desktop height")
	wait := flag.Duration("wait", 20*time.Second, "how long to wait for the session to become ready")
	bitmaps := flag.Int("bitmaps", 0, "exit after this many bitmap updates (0 = wait until timeout)")
	dump := flag.String("dump", "", "write the composited framebuffer to this PNG file")
	frame := flag.String("frame", "", "write the library's own framebuffer (Client.Framebuffer) to this PNG file")
	rects := flag.Bool("rects", false, "log each bitmap rectangle's geometry")
	var actions []inputAction
	keys := actionSeq{list: &actions, isType: false}
	flag.Var(keys, "key", "input event, repeatable: press:0x1c | down:0x1c | up:0x1c | move:x,y | click:x,y | wait:300")
	types := actionSeq{list: &actions, isType: true}
	flag.Var(types, "type", "type an ASCII string into the focused window, repeatable")
	postInput := flag.Duration("post-input", 3*time.Second, "wait after input playback before dumping")
	unicodeText := flag.String("unicode-text", "", "type this string using Unicode key events rather than scancodes")
	unicodeHold := flag.Duration("unicode-hold", 0, "how long to hold each Unicode key down (0 sends the release immediately)")
	slowInput := flag.Bool("slow-input", false, "send input over the slow path (disables fast-path input)")
	pointerLog := flag.Bool("pointer", false, "log server-side pointer updates (position and shape)")
	cursorLog := flag.Bool("cursor", false, "log decoded pointer shapes and system cursors")
	egfx := flag.Bool("egfx", false, "enable the EGFX (RDPGFX) dynamic channel")
	ordersFlag := flag.Bool("orders", false, "advertise and render drawing orders (bitmap cache and MEMBLT)")
	dispFlag := flag.Bool("display-control", false, "open the Display Control channel (needed for -resize)")
	resize := flag.String("resize", "", "ask the server to resize to WxH once the session is ready")
	clip := flag.Bool("clipboard", false, "enable the clipboard channel and log text received")
	setClip := flag.String("set-clipboard", "", "publish this text on the shared clipboard once ready")
	clipReq := flag.Duration("request-clipboard-after", 0, "ask the server for its clipboard text this long after ready (0 disables)")
	logLevel := flag.Int("log", int(glog.INFO), "log level 0=TRACE..5=NONE")
	flag.Parse()

	s := client.NewSetting()
	s.Width = *width
	s.Height = *height
	s.Protocol = *proto
	s.LogLevel = glog.LEVEL(*logLevel)
	s.NoFastPathInput = *slowInput
	s.EnableEGFX = *egfx
	s.EnableOrders = *ordersFlag
	s.EnableDisplayControl = *dispFlag || *resize != ""
	s.EnableClipboard = *clip

	c := client.NewClient(*host, *user, *pass, client.TC_RDP, s)

	ready := make(chan struct{}, 1)
	failed := make(chan error, 1)
	closed := make(chan struct{}, 1)
	var bitmapCount int
	var gfxFrames, gfxSurfaces, orderFrames int
	done := make(chan struct{}, 1)

	var fb *image.RGBA
	if *dump != "" {
		fb = image.NewRGBA(image.Rect(0, 0, *width, *height))
	}
	writeDump := func() {
		if fb == nil || *dump == "" {
			return
		}
		f, err := os.Create(*dump)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dump:", err)
			return
		}
		defer f.Close()
		if err := png.Encode(f, fb); err != nil {
			fmt.Fprintln(os.Stderr, "dump:", err)
			return
		}
		fmt.Println("wrote framebuffer to", *dump)
		if gfxFrames > 0 {
			fmt.Printf("gfx: %d frames carrying %d surfaces\n", gfxFrames, gfxSurfaces)
		}
		if orderFrames > 0 {
			fmt.Printf("orders: %d frames rendered\n", orderFrames)
		}
	}

	// With orders the server draws through the bitmap cache and does not send
	// bitmap updates at all, so the renderer's screen is the whole desktop.
	c.OnOrdersFrame(func(dirty image.Rectangle) {
		orderFrames++
		if fb == nil {
			return
		}
		if screen := c.Screen(); screen != nil {
			blitScreen(fb, screen)
		}
	})

	// With EGFX the server draws into offscreen surfaces instead of sending
	// bitmap updates, and it is the client that puts them on screen. Each
	// surface is drawn where the server mapped it.
	c.OnSurfaceFrame(func(frameID uint32, surfaces []*rdpgfx.Surface) {
		gfxFrames++
		gfxSurfaces += len(surfaces)
		if fb == nil {
			return
		}
		for _, s := range surfaces {
			blitSurface(fb, s)
		}
	})

	var inputPlayed bool
	var unicodePlayed bool
	c.OnResize(func(w, h int) {
		fmt.Printf("desktop resized to %dx%d\n", w, h)
	})
	c.OnReady(func() {
		select {
		case ready <- struct{}{}:
		default:
		}
		if *resize != "" {
			go func() {
				var w, h int
				if _, err := fmt.Sscanf(*resize, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
					fmt.Fprintf(os.Stderr, "resize: %q is not WxH\n", *resize)
					return
				}
				// The server applies this on its own schedule, so a
				// failure here is reported rather than retried, and a
				// server that does not support it says so.
				if err := c.RequestResize(w, h); err != nil {
					fmt.Fprintf(os.Stderr, "resize: %v\n", err)
					return
				}
				fmt.Printf("asked the server to resize to %dx%d\n", w, h)
			}()
		}
		// The clipboard is independent of input playback. It used to sit inside
		// the guard below, so -set-clipboard was silently ignored unless some
		// input action happened to be given too.
		if *setClip != "" || *clipReq > 0 {
			go func() {
				if *setClip != "" {
					if err := c.SetClipboardText(*setClip); err != nil {
						fmt.Fprintln(os.Stderr, "set clipboard:", err)
					} else {
						fmt.Printf("published %d bytes to the shared clipboard\n", len(*setClip))
					}
					time.Sleep(800 * time.Millisecond)
				}
				if *clipReq > 0 {
					// Request outside the handler's own retry window, so the
					// server has had time to acquire the selection.
					time.Sleep(*clipReq)
					if err := c.RequestClipboardText(); err != nil {
						fmt.Fprintln(os.Stderr, "request clipboard:", err)
					}
				}
			}()
		}

		if *unicodeText != "" && !unicodePlayed {
			unicodePlayed = true
			go func() {
				// Unicode key events rather than scancodes, which is how an
				// input method's output reaches the server: a scancode cannot
				// say which character was meant.
				fmt.Printf("typing via Unicode key events: %q\n", *unicodeText)
				if err := c.TypeText(*unicodeText, *unicodeHold); err != nil {
					fmt.Fprintln(os.Stderr, "unicode text:", err)
				}
				time.Sleep(*postInput)
				select {
				case done <- struct{}{}:
				default:
				}
			}()
		}

		if len(actions) > 0 && !inputPlayed {
			inputPlayed = true
			go func() {
				for _, a := range actions {
					if a.isType {
						typeString(c, a.value)
					} else {
						playInput(c, []string{a.value})
					}
				}
				time.Sleep(*postInput)
				select {
				case done <- struct{}{}:
				default:
				}
			}()
		}
	})
	c.OnError(func(err error) {
		select {
		case failed <- err:
		default:
		}
	})
	c.OnClose(func() {
		select {
		case closed <- struct{}{}:
		default:
		}
	})
	c.OnPointerPosition(func(x, y int) {
		if *pointerLog {
			fmt.Printf("pointer position: %d,%d\n", x, y)
		}
	})
	c.OnPointer(func(p *pdu.PointerDataPDU) {
		if *pointerLog {
			fmt.Printf("pointer msg=0x%02x pos=%d,%d cache=%d size=%dx%d data=%d\n",
				p.MessageType, p.XPos, p.YPos, p.CacheIndex, p.Width, p.Height, len(p.Data))
		}
	})

	c.OnClipboardText(func(text string) {
		fmt.Printf("clipboard text (%d bytes): %q\n", len(text), text)
	})

	if *cursorLog {
		c.OnCursor(func(cur *client.Cursor) {
			if cur.System {
				fmt.Printf("cursor: system type %#08x hidden=%v\n", cur.SystemType, cur.Hidden())
				return
			}
			// Count the pixels that will actually be drawn, which is what
			// says whether the masks were applied rather than a shape that
			// decoded into nothing.
			opaque := 0
			for y := 0; y < cur.Height; y++ {
				for x := 0; x < cur.Width; x++ {
					if cur.Image.Pix[y*cur.Image.Stride+x*4+3] != 0 {
						opaque++
					}
				}
			}
			fmt.Printf("cursor: %dx%d hotspot=(%d,%d) opaque=%d/%d\n",
				cur.Width, cur.Height, cur.Hotspot.X, cur.Hotspot.Y, opaque, cur.Width*cur.Height)
		})
		cursorPositions := 0
		c.OnCursorPos(func(x, y int) { cursorPositions++ })
		defer func() { fmt.Printf("cursor: %d position updates\n", cursorPositions) }()
	}

	c.OnBitmap(func(bs []client.Bitmap) {
		bitmapCount += len(bs)
		if *rects {
			for _, b := range bs {
				nz := 0
				for _, v := range b.Data {
					if v != 0 {
						nz++
					}
				}
				fmt.Printf("  rect dst=(%d,%d) size=%dx%d bpp=%d compress=%v data=%d nonzero=%d\n",
					b.DestLeft, b.DestTop, b.Width, b.Height, b.BitsPerPixel, b.IsCompress, len(b.Data), nz)
			}
		}
		if fb != nil {
			for _, b := range bs {
				blit(fb, b)
			}
		}
		fmt.Printf("bitmap update: %d rects (total %d)\n", len(bs), bitmapCount)
		if *bitmaps > 0 && bitmapCount >= *bitmaps {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	fmt.Printf("connecting to %s as %q ...\n", *host, *user)
	start := time.Now()
	if err := c.Login(); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("session ready in %s\n", time.Since(start).Round(time.Millisecond))

	timer := time.NewTimer(*wait)
	defer timer.Stop()

	for {
		select {
		case <-ready:
			fmt.Println("session ready")
		case err := <-failed:
			fmt.Fprintf(os.Stderr, "session error: %v\n", err)
			c.Close()
			os.Exit(2)
		case <-closed:
			fmt.Fprintln(os.Stderr, "connection closed by peer")
			os.Exit(3)
		case <-done:
			fmt.Printf("received %d bitmap rectangles, closing\n", bitmapCount)
			writeDump()
			writeFrameDump(c, *frame)
			c.Close()
			return
		case <-timer.C:
			fmt.Printf("timeout after %s (bitmap rectangles received: %d)\n", *wait, bitmapCount)
			writeDump()
			writeFrameDump(c, *frame)
			c.Close()
			if bitmapCount == 0 {
				os.Exit(4)
			}
			return
		}
	}
}

// blit composites one decoded bitmap into the framebuffer.
// Note: client.Bitmap.BitsPerPixel already holds bytes-per-pixel.
// blitScreen copies an order rendered screen into the framebuffer. Orders draw
// the whole desktop, so this is a full frame rather than a patch.
// writeFrameDump writes the library's own framebuffer, rather than the copy this
// tool builds from OnBitmap, so that the two can be compared. They should agree
// pixel for pixel: the framebuffer is composited from the same updates that this
// tool sees, and a disagreement means one of the two compositors is wrong. That
// comparison is the point of the flag, so it is worth running both and diffing
// the PNGs rather than eyeballing either one.
func writeFrameDump(c *client.Client, path string) {
	if path == "" {
		return
	}
	fb := c.Framebuffer()
	if fb == nil {
		fmt.Fprintln(os.Stderr, "frame: the client has no framebuffer")
		return
	}
	w, h := fb.Size()
	if w <= 0 || h <= 0 {
		fmt.Fprintln(os.Stderr, "frame: the framebuffer is empty")
		return
	}
	px, stride := fb.Pix(), fb.Stride()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*stride + x*4
			if i+4 > len(px) {
				break
			}
			img.Set(x, y, color.RGBA{R: px[i+2], G: px[i+1], B: px[i], A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "frame:", err)
		return
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, "frame:", err)
		return
	}
	fmt.Printf("wrote the library framebuffer to %s (%dx%d)\n", path, w, h)
}

func blitScreen(fb *image.RGBA, s *orders.Screen) {
	w, h := s.Size()
	px := s.Pixels()
	for y := 0; y < h && y < fb.Rect.Dy(); y++ {
		for x := 0; x < w && x < fb.Rect.Dx(); x++ {
			i := (y*w + x) * 4
			if i+4 > len(px) {
				return
			}
			fb.Set(x, y, color.RGBA{R: px[i+2], G: px[i+1], B: px[i], A: 255})
		}
	}
}

// blitSurface copies a decoded EGFX surface into the framebuffer where the
// server mapped it. The surface is BGRA, top down, and is clipped to the
// framebuffer.
func blitSurface(fb *image.RGBA, s *rdpgfx.Surface) {
	// CompositePixels returns the surface at the size the server asked for, which
	// is not its native size when it was mapped with a scale factor. Drawing the
	// native pixels instead would put a scaled surface on screen at the wrong
	// size, so the size comes back with the pixels rather than being read off the
	// surface.
	px, w, h := s.CompositePixels()
	ox, oy := s.Origin()
	for y := 0; y < h; y++ {
		dy := oy + y
		if dy < 0 || dy >= fb.Rect.Dy() {
			continue
		}
		for x := 0; x < w; x++ {
			dx := ox + x
			if dx < 0 || dx >= fb.Rect.Dx() {
				continue
			}
			i := (y*w + x) * 4
			if i+4 > len(px) {
				return
			}
			fb.Set(dx, dy, color.RGBA{R: px[i+2], G: px[i+1], B: px[i], A: 255})
		}
	}
}

func blit(fb *image.RGBA, b client.Bitmap) {
	bpp := b.BitsPerPixel
	if bpp <= 0 {
		return
	}
	for y := 0; y < b.Height; y++ {
		for x := 0; x < b.Width; x++ {
			i := (y*b.Width + x) * bpp
			if i+bpp > len(b.Data) {
				return
			}
			var r, g, bl uint8
			switch bpp {
			case 2:
				// Most significant byte first, which is the order our RLE
				// decoder emits 16bpp pixels in (core/rle.go decompress2 ends
				// with PutUint16BE). Reading it the way a DIB would be read,
				// little endian, swaps the red and blue ends of every pixel.
				// An uncompressed 16bpp update is not covered by that evidence
				// and has never been seen: this client asks for 32bpp.
				v := uint16(b.Data[i])<<8 | uint16(b.Data[i+1])
				r = uint8((v >> 11) & 0x1f)
				g = uint8((v >> 5) & 0x3f)
				bl = uint8(v & 0x1f)
				r, g, bl = r<<3|r>>2, g<<2|g>>4, bl<<3|bl>>2
			default:
				bl, g, r = b.Data[i], b.Data[i+1], b.Data[i+2]
			}
			fb.Set(b.DestLeft+x, b.DestTop+y, color.RGBA{R: r, G: g, B: bl, A: 255})
		}
	}
}
