// Package client implements the RDP client, and carries the RFB (VNC) client
// this fork inherited from upstream alongside it.
//
// The VNC client (VncClient) builds and is kept, but it has never been run
// against a VNC server in this project, so it is not a supported path; its
// clipboard methods are stubs that do nothing. The RFB parse steps it uses are
// unit tested in protocol/rfb.
package client

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/adfnekc/grdp/protocol/rfb"
)

type VncClient struct {
	vnc     *rfb.RFB
	timeout time.Duration
}

func newVncClient(s *Setting) *VncClient {
	c := &VncClient{timeout: DefaultLoginTimeout}
	if s != nil && s.Timeout > 0 {
		c.timeout = s.Timeout
	}
	return c
}

// Login dials the server and returns only once the RFB handshake has completed,
// the handshake has failed, or the timeout expires. The handshake is event
// driven, so it is driven to a result the caller can see instead of returning
// while it is still running. There is no VNC server in this project to exercise
// it against.
func (c *VncClient) Login(host, user, pwd string, width, height int) error {
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return fmt.Errorf("[dial err] %v", err)
	}

	c.vnc = rfb.NewRFB(rfb.NewRFBConn(conn, pwd))

	// Register the listeners before Connect, which starts the handshake, so no
	// handshake event can arrive before there is a listener for it.
	done := make(chan struct{})
	var once sync.Once
	var loginErr error
	c.vnc.On("ready", func() {
		once.Do(func() { close(done) })
	})
	c.vnc.On("error", func(e error) {
		once.Do(func() {
			loginErr = e
			close(done)
		})
	})

	if err := c.vnc.Connect(); err != nil {
		c.vnc = nil
		conn.Close()
		return fmt.Errorf("[vnc connect err] %v", err)
	}

	select {
	case <-done:
		if loginErr != nil {
			c.vnc = nil
			conn.Close()
			return fmt.Errorf("[vnc authentication err] %v", loginErr)
		}
		return nil
	case <-time.After(c.timeout):
		c.vnc = nil
		conn.Close()
		return fmt.Errorf("[vnc connect timeout] no handshake response within %s", c.timeout)
	}
}

// SetClipboardText is not implemented for RFB.
func (c *VncClient) SetClipboardText(string) error { return nil }

// RequestClipboardText is not implemented for RFB.
func (c *VncClient) RequestClipboardText() error { return nil }

// On registers a listener. It is a no-op before Login succeeds, so a caller
// that lost the connection cannot panic here.
func (c *VncClient) On(event string, f interface{}) {
	if c.vnc == nil {
		return
	}
	c.vnc.On(event, f)
}

func (c *VncClient) KeyUp(sc int, name string) {
	if c.vnc == nil {
		return
	}
	k := &rfb.KeyEvent{}
	k.Key = uint32(sc)
	c.vnc.SendKeyEvent(k)
}
func (c *VncClient) KeyDown(sc int, name string) {
	if c.vnc == nil {
		return
	}
	k := &rfb.KeyEvent{}
	k.DownFlag = 1
	k.Key = uint32(sc)
	c.vnc.SendKeyEvent(k)
}

func (c *VncClient) MouseMove(x, y int) {
	if c.vnc == nil {
		return
	}
	p := &rfb.PointerEvent{}
	time.Sleep(8 * time.Millisecond)
	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.vnc.SendPointEvent(p)
}

func (c *VncClient) MouseWheel(scroll, x, y int) {
}

func (c *VncClient) MouseUp(button int, x, y int) {
	if c.vnc == nil {
		return
	}
	p := &rfb.PointerEvent{}

	switch button {
	case 0:
		p.Mask = 1
	case 2:
		p.Mask = 1<<3 - 1
	case 1:
		p.Mask = 1<<2 - 1
	default:
		p.Mask = 0
	}
	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.vnc.SendPointEvent(p)
}
func (c *VncClient) MouseDown(button int, x, y int) {
	if c.vnc == nil {
		return
	}
	p := &rfb.PointerEvent{}

	switch button {
	case 0:
		p.Mask = 1
	case 2:
		p.Mask = 1<<3 - 1
	case 1:
		p.Mask = 1<<2 - 1
	default:
		p.Mask = 0
	}

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.MouseMove(x, y)
	c.vnc.SendPointEvent(p)
}

func (c *VncClient) Close() {
	if c.vnc != nil {
		c.vnc.Close()
	}
}
