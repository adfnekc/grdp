// Package client implements the RDP client, and carries the RFB (VNC) client
// this fork inherited from upstream alongside it.
//
// The VNC client (VncClient) is exercised against a real server by
// scripts/vnc-dev.sh, which starts TigerVNC's Xtigervnc and runs the live tests
// in protocol/rfb. Its clipboard is implemented in both directions: text the
// server cuts arrives as the "clipboard-text" event, and SetClipboardText
// publishes with ClientCutText.
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
	// pending holds listeners registered before Login created the connection.
	// VncClient.On attaches to the rfb.RFB, which does not exist until then, so
	// a listener registered earlier is held here and attached once it does - the
	// same thing RdpClient does for the RDP path.
	pending []pendingEvent
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
// while it is still running. scripts/vnc-dev.sh runs this against a real server.
func (c *VncClient) Login(host, user, pwd string, width, height int) error {
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return fmt.Errorf("[dial err] %v", err)
	}

	c.vnc = rfb.NewRFB(rfb.NewRFBConn(conn, pwd))

	// The protocol layer emits ServerCutText as "CutText" with its raw bytes;
	// translate it to the string event the rest of the client already uses for
	// the clipboard, so Client.OnClipboardText works the same way it does for
	// RDP.
	c.vnc.On("CutText", func(data []byte) {
		c.vnc.Emit("clipboard-text", string(data))
	})

	// Attach any listeners that were registered before the connection existed.
	for _, p := range c.pending {
		c.vnc.On(p.event, p.f)
	}
	c.pending = nil

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

// SetClipboardText publishes text on the server's clipboard. RFB carries it as
// one ClientCutText message. There is no acknowledgement, so a nil error only
// means the bytes were written to the socket.
func (c *VncClient) SetClipboardText(text string) error {
	if c.vnc == nil {
		return fmt.Errorf("client: not connected")
	}
	c.vnc.SendClientCutText(&rfb.ClientCutText{Message: text})
	return nil
}

// RequestClipboardText is not supported by RFB and says so. Unlike the RDP
// clipboard channel, RFB has no client message that asks for the server's cut
// text: the server sends ServerCutText whenever its clipboard changes, and the
// only client-to-server message, ClientCutText, publishes rather than requests.
// Text therefore arrives through OnClipboardText with no request behind it.
func (c *VncClient) RequestClipboardText() error {
	return fmt.Errorf("rfb: the protocol has no clipboard request; the server sends cut text when it changes")
}

// On registers a listener. A listener registered before Login is held until
// there is a connection to attach it to, so a caller that registers before
// connecting still receives events.
func (c *VncClient) On(event string, f interface{}) {
	if c.vnc == nil {
		c.pending = append(c.pending, pendingEvent{event, f})
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
