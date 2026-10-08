// Package disp implements the display control dynamic virtual channel
// (MS-RDPEDISP), which is how a client asks the server to change the desktop
// size while a session is running.
//
// Without it a gateway cannot follow a browser window: the desktop size is
// fixed by the connect sequence, so a user who resizes the page has to
// reconnect, and reconnecting means authenticating again and losing whatever the
// session was doing.
package disp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/plugin"
	"github.com/adfnekc/grdp/glog"
)

// DVCChannelName is the dynamic virtual channel this package speaks on.
const DVCChannelName = "Microsoft::Windows::RDS::DisplayControl"

// ChannelName is the static virtual channel of the same name. Both are
// advertised: the messages travel on the dynamic one, but a server that is not
// offered the static one does not create the dynamic one either.
const ChannelName = plugin.DISP_SVC_CHANNEL_NAME

// ChannelOption is what the static channel is advertised with.
const ChannelOption = plugin.CHANNEL_OPTION_INITIALIZED | plugin.CHANNEL_OPTION_ENCRYPT_RDP

// Fixed sizes from MS-RDPEDISP. The header is a type and a length, the layout
// PDU adds a monitor size and a count, and each monitor is forty bytes.
const (
	headerLength      = 8
	monitorLayoutSize = 40
	capsBodyLength    = 12
)

// Message types. FreeRDP's channels/disp/disp.h is the reference for the two
// values; the specification's own section numbers are not the ids.
const (
	pduTypeMonitorLayout = 0x00000002
	pduTypeCaps          = 0x00000005
)

// DISPLAY_CONTROL_MONITOR_PRIMARY marks the monitor whose origin is (0, 0).
const monitorPrimary = 0x00000001

// The range the server accepts, from FreeRDP's disp.h. It clamps; this rejects,
// because a caller that asked for 10000 pixels across and got 8192 silently has
// been told its request succeeded when it was changed.
const (
	minMonitorWidth  = 200
	maxMonitorWidth  = 8192
	minMonitorHeight = 200
	maxMonitorHeight = 8192
)

// displayChannelTimeout bounds how long a resize waits for a channel to carry it.
const displayChannelTimeout = 5 * time.Second

// Monitor is one entry of a display layout: where a monitor sits and how big it
// is, in pixels and in millimetres.
type Monitor struct {
	// Flags is monitorPrimary for the primary monitor and zero otherwise.
	Flags uint32
	// Left and Top are the monitor's origin relative to the primary monitor's
	// upper left corner, which is always (0, 0).
	Left int32
	Top  int32
	// Width and Height are the desktop resolution of the monitor. The server
	// requires an even width.
	Width  uint32
	Height uint32
	// PhysicalWidth and PhysicalHeight are the size in millimetres, which is
	// what the server uses to work out the dots per inch.
	PhysicalWidth  uint32
	PhysicalHeight uint32
	// Orientation is one of the DISPLAY_CONTROL_ROTATION values.
	Orientation uint32
	// DesktopScaleFactor and DeviceScaleFactor are the scale percentages.
	DesktopScaleFactor uint32
	DeviceScaleFactor  uint32
}

// Orientation values, in the form the field carries: a rotation from zero,
// together with the flip bits.
const (
	OrientationLandscape  = 0
	OrientationPortrait   = 90
	OrientationLandscapeF = 180
	OrientationPortraitF  = 270
)

// Caps is what the server said it will accept, sent once when the channel
// opens.
type Caps struct {
	// MaxNumMonitors is the most monitors the server will lay out.
	MaxNumMonitors uint32
	// MaxMonitorAreaFactorA and B bound the total area the layout may cover.
	MaxMonitorAreaFactorA uint32
	MaxMonitorAreaFactorB uint32
}

// DisplayControlClient is the channel handler. It is registered with the dynamic
// virtual channel layer, which calls OnOpen, OnData and OnClose.
type DisplayControlClient struct {
	*emission.Emitter

	mu       sync.Mutex
	channel  uint32
	open     bool
	send     func(channelID uint32, data []byte) error
	static   core.ChannelSender
	lastSent []byte

	// ready is closed once a channel can carry a layout, which is either the
	// dynamic channel opening or the static channel being wired. A caller asks
	// for a resize as soon as the session is up, which is usually before the
	// dynamic channel has been negotiated, and sending on whichever channel
	// happens to exist at that moment is how a request ends up on the static
	// channel of a server that only listens on the dynamic one.
	ready chan struct{}
	once  sync.Once

	// channelTimeout bounds how long a resize waits for a channel to carry it.
	// A field so that a test can shorten it; five seconds is what a caller
	// talking to a real server should be prepared to spend.
	channelTimeout time.Duration
}

// NewDisplayControlClient returns a client for the channel.
func NewDisplayControlClient() *DisplayControlClient {
	return &DisplayControlClient{
		Emitter:        emission.NewEmitter(),
		ready:          make(chan struct{}),
		channelTimeout: displayChannelTimeout,
	}
}

// markReady records that a channel is usable. The first one wins.
func (c *DisplayControlClient) markReady() {
	c.once.Do(func() { close(c.ready) })
}

// SetSender installs the callback that writes to the dynamic virtual channel. It
// is what the channel layer supplies; without it nothing can be sent.
func (c *DisplayControlClient) SetSender(f func(channelID uint32, data []byte) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send = f
}

// OnCaps registers f to be called with the server's limits once the channel
// opens.
func (c *DisplayControlClient) OnCaps(f func(Caps)) {
	c.On("caps", func(v interface{}) {
		f(v.(Caps))
	})
}

// OnOpen implements drdynvc.DynChannel.
func (c *DisplayControlClient) OnOpen(channelID uint32) {
	c.mu.Lock()
	c.channel, c.open = channelID, true
	c.mu.Unlock()
	glog.Debugf("disp: channel %d open", channelID)
	c.markReady()
	c.Emit("open")
}

// OnClose implements drdynvc.DynChannel.
func (c *DisplayControlClient) OnClose() {
	c.mu.Lock()
	c.open = false
	c.mu.Unlock()
	c.Emit("close")
}

// OnData implements drdynvc.DynChannel. The only message the server sends on
// this channel is its capabilities, which arrive once.
func (c *DisplayControlClient) OnData(data []byte) {
	if len(data) < headerLength {
		glog.Warnf("disp: message of %d bytes is shorter than the header", len(data))
		return
	}
	typ := binary.LittleEndian.Uint32(data[0:4])
	length := binary.LittleEndian.Uint32(data[4:8])
	if int(length) > len(data) {
		// The length includes the header, so it can never be larger than what
		// arrived. Trusting it would read past the buffer.
		glog.Warnf("disp: declared length %d exceeds the %d bytes received", length, len(data))
		return
	}
	body := data[headerLength:length]
	switch typ {
	case pduTypeCaps:
		if len(body) < capsBodyLength {
			glog.Warnf("disp: capabilities message of %d bytes, want %d", len(body), capsBodyLength)
			return
		}
		caps := Caps{
			MaxNumMonitors:        binary.LittleEndian.Uint32(body[0:4]),
			MaxMonitorAreaFactorA: binary.LittleEndian.Uint32(body[4:8]),
			MaxMonitorAreaFactorB: binary.LittleEndian.Uint32(body[8:12]),
		}
		glog.Infof("disp: server caps monitors=%d area=%dx%d",
			caps.MaxNumMonitors, caps.MaxMonitorAreaFactorA, caps.MaxMonitorAreaFactorB)
		c.Emit("caps", caps)
	case pduTypeMonitorLayout:
		// The server does not send layouts; it applies the client's. A message
		// here means a server that does something unusual, so it is reported
		// rather than parsed into a guess.
		glog.Warnf("disp: server sent a monitor layout, which it is not expected to do")
	default:
		glog.Warnf("disp: unknown message type 0x%08x", typ)
	}
}

// GetType reports the static channel this handler answers for, which is how the
// channel layer knows to route to it.
func (c *DisplayControlClient) GetType() (string, uint32) {
	return ChannelName, ChannelOption
}

// Sender installs the static channel's writer.
//
// Display Control can travel on either channel, and the messages are the same on
// both, so this handler speaks whichever one the server set up. Windows joins the
// static channel and never creates the dynamic one, so this is the path that
// actually works there.
func (c *DisplayControlClient) Sender(f core.ChannelSender) {
	c.mu.Lock()
	c.static = f
	c.mu.Unlock()
	// Deliberately not markReady: the dynamic channel is the protocol and the
	// static one is the fallback, so a caller waits for the former and only
	// falls back when it does not arrive. Marking ready here made a resize go
	// out on the static channel of a server that listens on the dynamic one.
}

// Process consumes one payload from the static channel.
func (c *DisplayControlClient) Process(s []byte) {
	c.OnData(s)
}

// RequestResize asks the server to change the primary monitor to width by
// height. The whole layout has to be sent, not just the one monitor that
// changed, so a single monitor layout is built here.
//
// The width is rounded down to an even number, which is what the server
// requires and what FreeRDP sends. Sizes outside the range the server accepts
// are refused rather than clamped, so that a caller is not told its request
// succeeded when the size was quietly changed.
func (c *DisplayControlClient) RequestResize(width, height int) error {
	if width < minMonitorWidth || width > maxMonitorWidth {
		return fmt.Errorf("disp: width %d is outside the %d to %d the server accepts", width, minMonitorWidth, maxMonitorWidth)
	}
	if height < minMonitorHeight || height > maxMonitorHeight {
		return fmt.Errorf("disp: height %d is outside the %d to %d the server accepts", height, minMonitorHeight, maxMonitorHeight)
	}
	evenWidth := uint32(width - width%2)
	monitor := Monitor{
		Flags:              monitorPrimary,
		Left:               0,
		Top:                0,
		Width:              evenWidth,
		Height:             uint32(height),
		PhysicalWidth:      physicalFor(evenWidth),
		PhysicalHeight:     physicalFor(uint32(height)),
		Orientation:        OrientationLandscape,
		DesktopScaleFactor: 100,
		DeviceScaleFactor:  100,
	}
	pdu := buildMonitorLayout([]Monitor{monitor})
	return c.write(pdu)
}

// buildMonitorLayout serialises a layout PDU. It is a separate function from the
// send so that the bytes can be checked without a channel behind them.
func buildMonitorLayout(monitors []Monitor) []byte {
	length := headerLength + 8 + len(monitors)*monitorLayoutSize
	buf := make([]byte, 0, length)
	buf = appendUint32(buf, pduTypeMonitorLayout)
	// The length covers the header as well as the body.
	buf = appendUint32(buf, uint32(length))
	buf = appendUint32(buf, monitorLayoutSize)
	buf = appendUint32(buf, uint32(len(monitors)))
	for _, m := range monitors {
		buf = appendUint32(buf, m.Flags)
		buf = appendUint32(buf, uint32(m.Left))
		buf = appendUint32(buf, uint32(m.Top))
		buf = appendUint32(buf, m.Width)
		buf = appendUint32(buf, m.Height)
		buf = appendUint32(buf, m.PhysicalWidth)
		buf = appendUint32(buf, m.PhysicalHeight)
		buf = appendUint32(buf, m.Orientation)
		buf = appendUint32(buf, m.DesktopScaleFactor)
		buf = appendUint32(buf, m.DeviceScaleFactor)
	}
	return buf
}

// physicalFor converts a pixel count to millimetres at the 96 dots per inch the
// field assumes, which is what a screen of that size usually measures. It is
// only a physical size hint: the server uses it for the reported dots per inch,
// and a desktop renders the same either way.
func physicalFor(pixels uint32) uint32 {
	mm := pixels * 254 / 960
	if mm < 10 {
		mm = 10
	}
	if mm > 10000 {
		mm = 10000
	}
	return mm
}

func appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// write sends a message, and refuses to say it succeeded when there is no
// channel to send it on. The dynamic channel is preferred when it exists and the
// static one is the fallback, because a server that set up both would rather
// have the dynamic one.
func (c *DisplayControlClient) write(pdu []byte) error {
	// Wait for a channel rather than sending on whichever one exists, and give
	// up rather than waiting forever: a server that will not open the channel
	// has to be reported, not hung on.
	c.mu.Lock()
	timeout := c.channelTimeout
	c.mu.Unlock()
	select {
	case <-c.ready:
		// The dynamic channel is open, which is what the protocol asks for.
	case <-time.After(timeout):
		c.mu.Lock()
		fallback := c.static
		c.mu.Unlock()
		if fallback == nil {
			return fmt.Errorf("disp: no display control channel after %s", timeout)
		}
	}

	c.mu.Lock()
	send, channel, open := c.send, c.channel, c.open
	static := c.static
	c.mu.Unlock()

	if open && send != nil {
		c.mu.Lock()
		c.lastSent = append([]byte(nil), pdu...)
		c.mu.Unlock()
		return send(channel, pdu)
	}
	if static != nil {
		c.mu.Lock()
		c.lastSent = append([]byte(nil), pdu...)
		c.mu.Unlock()
		if _, err := static.SendToChannel(ChannelName, pdu); err != nil {
			return fmt.Errorf("disp: send on the static channel: %w", err)
		}
		return nil
	}
	return fmt.Errorf("disp: the display control channel is not open")
}

// LastSent returns the last layout this client sent, which is what a test
// compares against. It is a copy.
func (c *DisplayControlClient) LastSent() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.lastSent...)
}

// ParseMonitorLayout reads a layout PDU back. It exists so that the bytes this
// package produces can be checked against the structures they claim to be, and
// so that a server's layout could be read if one ever sent it.
func ParseMonitorLayout(data []byte) ([]Monitor, error) {
	if len(data) < headerLength+8 {
		return nil, fmt.Errorf("disp: %d bytes is too short for a layout", len(data))
	}
	typ := binary.LittleEndian.Uint32(data[0:4])
	if typ != pduTypeMonitorLayout {
		return nil, fmt.Errorf("disp: message type 0x%08x is not a monitor layout", typ)
	}
	length := int(binary.LittleEndian.Uint32(data[4:8]))
	if length > len(data) {
		return nil, fmt.Errorf("disp: declared length %d exceeds the %d bytes received", length, len(data))
	}
	size := binary.LittleEndian.Uint32(data[8:12])
	if size != monitorLayoutSize {
		return nil, fmt.Errorf("disp: monitor layout size is %d, want %d", size, monitorLayoutSize)
	}
	count := int(binary.LittleEndian.Uint32(data[12:16]))
	// The count is a wire value and each monitor is forty bytes, so it is
	// checked against what actually arrived before it sizes anything.
	if count < 0 || headerLength+8+count*monitorLayoutSize > length {
		return nil, fmt.Errorf("disp: %d monitors do not fit in %d bytes", count, length)
	}
	r := bytes.NewReader(data[16:length])
	out := make([]Monitor, 0, count)
	for i := 0; i < count; i++ {
		var m Monitor
		if err := readMonitor(r, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// readMonitor reads one monitor entry. The fields are listed in the order they
// are on the wire, which is the order they were written in, because that is the
// only thing that makes a reader and a writer agree.
//
// Which of the two pointers is set says whether the field is signed, rather than
// a flag beside it: a flag that disagrees with the pointer it describes is a nil
// dereference on a path that parses bytes from the network, which is what this
// was first written as, and what its test caught.
func readMonitor(r *bytes.Reader, m *Monitor) error {
	type field struct {
		u *uint32
		i *int32
	}
	fields := []field{
		{u: &m.Flags},
		{i: &m.Left},
		{i: &m.Top},
		{u: &m.Width},
		{u: &m.Height},
		{u: &m.PhysicalWidth},
		{u: &m.PhysicalHeight},
		{u: &m.Orientation},
		{u: &m.DesktopScaleFactor},
		{u: &m.DeviceScaleFactor},
	}
	for _, f := range fields {
		v, err := core.ReadUInt32LE(r)
		if err != nil {
			return err
		}
		if f.i != nil {
			*f.i = int32(v)
		} else {
			*f.u = v
		}
	}
	return nil
}
