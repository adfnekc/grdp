package client

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/plugin"
	"github.com/adfnekc/grdp/protocol/nla"
	"github.com/adfnekc/grdp/protocol/pdu"
	"github.com/adfnekc/grdp/protocol/sec"
	"github.com/adfnekc/grdp/protocol/t125"
	"github.com/adfnekc/grdp/protocol/tpkt"
	"github.com/adfnekc/grdp/protocol/x224"
)

type RdpClient struct {
	tpkt     *tpkt.TPKT
	x224     *x224.X224
	mcs      *t125.MCSClient
	sec      *sec.Client
	pdu      *pdu.Client
	channels *plugin.Channels
	setting  *Setting
	pending  []pendingEvent
}

type pendingEvent struct {
	event string
	f     interface{}
}

func newRdpClient(s *Setting) *RdpClient {
	return &RdpClient{setting: s}
}

// requestedProtocol maps the configured security protocol name to the X.224
// negotiation bitmask. The default is TLS, which is what modern servers offer;
// Standard RDP Security is not supported by this client yet.
func requestedProtocol(s *Setting) uint32 {
	if s == nil {
		return x224.PROTOCOL_SSL
	}
	switch strings.ToLower(s.Protocol) {
	case "rdp", "standard":
		return x224.PROTOCOL_RDP
	case "nla", "hybrid", "credssp":
		return x224.PROTOCOL_HYBRID
	default: // "", "auto", "tls", "ssl"
		return x224.PROTOCOL_SSL
	}
}

func bitmapDecompress(bitmap *pdu.BitmapData) []byte {
	return core.Decompress(bitmap.BitmapDataStream, int(bitmap.Width), int(bitmap.Height), Bpp(bitmap.BitsPerPixel))
}
func split(user string) (domain string, uname string) {
	if strings.Index(user, "\\") != -1 {
		t := strings.Split(user, "\\")
		domain = t[0]
		uname = t[len(t)-1]
	} else if strings.Index(user, "/") != -1 {
		t := strings.Split(user, "/")
		domain = t[0]
		uname = t[len(t)-1]
	} else {
		uname = user
	}
	return
}
func (c *RdpClient) Login(host, user, pwd string, width, height int) error {
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return fmt.Errorf("[dial err] %v", err)
	}

	domain, user := split(user)
	c.tpkt = tpkt.New(core.NewSocketLayer(conn), nla.NewNTLMv2(domain, user, pwd))
	c.x224 = x224.New(c.tpkt)
	c.mcs = t125.NewMCSClient(c.x224)
	c.sec = sec.NewClient(c.mcs)
	c.pdu = pdu.NewClient(c.sec)
	c.channels = plugin.NewChannels(c.sec)
	// Replay handlers that were registered before the layers existed.
	for _, p := range c.pending {
		c.pdu.On(p.event, p.f)
	}
	c.pending = nil

	c.mcs.SetClientDesktop(uint16(width), uint16(height))

	c.sec.SetUser(user)
	c.sec.SetPwd(pwd)
	c.sec.SetDomain(domain)

	c.tpkt.SetFastPathListener(c.sec)
	c.sec.SetFastPathListener(c.pdu)
	c.sec.SetChannelSender(c.mcs)
	c.channels.SetChannelSender(c.sec)
	c.pdu.SetFastPathSender(c.tpkt)

	c.x224.SetRequestedProtocol(requestedProtocol(c.setting))

	err = c.x224.Connect()
	if err != nil {
		return fmt.Errorf("[x224 connect err] %v", err)
	}
	return nil
}
func (c *RdpClient) On(event string, f interface{}) {
	if c == nil {
		return
	}
	if c.pdu == nil {
		// Registered before Login: buffer and replay once the PDU layer exists.
		c.pending = append(c.pending, pendingEvent{event, f})
		return
	}
	c.pdu.On(event, f)
}

// scancodeFlags splits an 0xE0-prefixed extended scancode into the base
// scancode and the KBDFLAGS_EXTENDED flag required by MS-RDPBCGR.
func scancodeFlags(sc int) (uint16, uint16) {
	if sc&0xFF00 == 0xE000 {
		return uint16(sc & 0xFF), pdu.KBDFLAGS_EXTENDED
	}
	return uint16(sc), 0
}

func (c *RdpClient) KeyUp(sc int, name string) {
	if c == nil || c.pdu == nil {
		return
	}
	code, flags := scancodeFlags(sc)
	p := &pdu.ScancodeKeyEvent{}
	p.KeyCode = code
	p.KeyboardFlags = flags | pdu.KBDFLAGS_RELEASE
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
}
func (c *RdpClient) KeyDown(sc int, name string) {
	if c == nil || c.pdu == nil {
		return
	}
	code, flags := scancodeFlags(sc)
	p := &pdu.ScancodeKeyEvent{}
	p.KeyCode = code
	p.KeyboardFlags = flags
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
}

func (c *RdpClient) MouseMove(x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}
	p.PointerFlags |= pdu.PTRFLAGS_MOVE
	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

func (c *RdpClient) MouseWheel(scroll, x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}
	p.PointerFlags |= pdu.PTRFLAGS_WHEEL | pdu.PTRFLAGS_MOVE
	rotation := scroll
	if rotation < 0 {
		p.PointerFlags |= pdu.PTRFLAGS_WHEEL_NEGATIVE
		rotation = -rotation
	}
	p.PointerFlags |= uint16(rotation) & pdu.WheelRotationMask
	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

func (c *RdpClient) MouseUp(button int, x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}

	switch button {
	case 0:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON1
	case 2:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON2
	case 1:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON3
	default:
		p.PointerFlags |= pdu.PTRFLAGS_MOVE
	}
	p.PointerFlags |= pdu.PTRFLAGS_MOVE

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}
func (c *RdpClient) MouseDown(button int, x, y int) {
	if c == nil || c.pdu == nil {
		return
	}
	p := &pdu.PointerEvent{}

	p.PointerFlags |= pdu.PTRFLAGS_DOWN | pdu.PTRFLAGS_MOVE

	switch button {
	case 0:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON1
	case 2:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON2
	case 1:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON3
	default:
		p.PointerFlags |= pdu.PTRFLAGS_MOVE
	}

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}
func (c *RdpClient) Close() {
	if c != nil && c.tpkt != nil {
		c.tpkt.Close()
	}
}
