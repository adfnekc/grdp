// client.go
package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/pdu"
	"github.com/adfnekc/grdp/protocol/rfb"
)

// DefaultLoginTimeout bounds how long Login waits for the RDP session to
// become ready after the transport handshake succeeds.
const DefaultLoginTimeout = 30 * time.Second

const (
	CLIP_OFF = 0
	CLIP_IN  = 0x1
	CLIP_OUT = 0x2
)

const (
	TC_RDP = 0
	TC_VNC = 1
)

type Control interface {
	Login(host, user, passwd string, width, height int) error
	KeyUp(sc int, name string)
	KeyDown(sc int, name string)
	MouseMove(x, y int)
	MouseWheel(scroll, x, y int)
	MouseUp(button int, x, y int)
	MouseDown(button int, x, y int)
	On(event string, msg interface{})
	Close()
}

func init() {
	glog.SetLevel(glog.INFO)
	logger := log.New(os.Stdout, "", 0)
	glog.SetLogger(logger)
}

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

func (c *Client) KeyUp(sc int, name string) {
	c.ctl.KeyUp(sc, name)
}
func (c *Client) KeyDown(sc int, name string) {
	c.ctl.KeyDown(sc, name)
}
func (c *Client) MouseMove(x, y int) {
	c.ctl.MouseMove(x, y)
}
func (c *Client) MouseWheel(scroll, x, y int) {
	c.ctl.MouseWheel(scroll, x, y)
}
func (c *Client) MouseUp(button, x, y int) {
	c.ctl.MouseUp(button, x, y)
}
func (c *Client) MouseDown(button, x, y int) {
	c.ctl.MouseDown(button, x, y)
}
func (c *Client) Close() {
	if c != nil && c.ctl != nil {
		c.ctl.Close()
	}
}
func (c *Client) OnError(f func(e error)) {
	c.ctl.On("error", f)
}
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
func (c *Client) OnSuccess(f func()) {
	c.ctl.On("success", f)
}
func (c *Client) OnReady(f func()) {
	c.ctl.On("ready", f)
}
func (c *Client) OnBitmap(f func([]Bitmap)) {
	f1 := func(data interface{}) {
		bs := make([]Bitmap, 0, 50)
		if c.tc == TC_VNC {
			br := data.(*rfb.BitRect)
			for _, v := range br.Rects {
				b := Bitmap{int(v.Rect.X), int(v.Rect.Y), int(v.Rect.X + v.Rect.Width), int(v.Rect.Y + v.Rect.Height),
					int(v.Rect.Width), int(v.Rect.Height),
					Bpp(uint16(br.Pf.BitsPerPixel)), false, v.Data}
				bs = append(bs, b)
			}
		} else {
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
		}
		f(bs)
	}

	c.ctl.On("bitmap", f1)
}

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

func Bpp(bp uint16) int {
	return int(bp / 8)
}

type Setting struct {
	Width    int
	Height   int
	Protocol string
	Timeout  time.Duration
	LogLevel glog.LEVEL
	// NoFastPathInput forces keyboard/mouse input over the slow path even
	// when the server advertised fast-path input. Useful for debugging.
	NoFastPathInput bool
}

func NewSetting() *Setting {
	return &Setting{
		Width:    1024,
		Height:   768,
		Timeout:  DefaultLoginTimeout,
		LogLevel: glog.INFO,
	}
}
func (s *Setting) SetLogLevel() {
	glog.SetLevel(s.LogLevel)
}

func (s *Setting) SetRequestedProtocol(p uint32) {}
func (s *Setting) SetClipboard(c int)            {}
