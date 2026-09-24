// Package drdynvc implements the dynamic virtual channel channel
// (MS-RDPEDYC). It carries the channels that are created at runtime rather
// than negotiated up front, of which EGFX is the one this library uses.
package drdynvc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/plugin"
)

const (
	ChannelName   = plugin.DRDYNVC_SVC_CHANNEL_NAME
	ChannelOption = plugin.CHANNEL_OPTION_INITIALIZED |
		plugin.CHANNEL_OPTION_ENCRYPT_RDP
)

// Dynamic virtual channel command ids (MS-RDPEDYC 2.2.2.1). DYNVC_CREATE_REQ
// is used in both directions: the response carries a status instead of a name.
const (
	cmdCreateRequest       = 0x01
	cmdDataFirst           = 0x02
	cmdData                = 0x03
	cmdClose               = 0x04
	cmdCapabilities        = 0x05
	cmdDataFirstCompressed = 0x06
	cmdDataCompressed      = 0x07
	cmdSoftSyncRequest     = 0x08
	cmdSoftSyncResponse    = 0x09
)

// Capability versions. Version 3 is what this implementation supports.
const (
	capsVersion1 = 0x0001
	capsVersion2 = 0x0002
	capsVersion3 = 0x0003
)

// maxDvcDataSize bounds a single DYNVC_DATA payload.
const maxDvcDataSize = 1600

// DynChannel is implemented by a handler for one dynamic virtual channel.
type DynChannel interface {
	// OnOpen is called once the channel exists locally.
	OnOpen(channelID uint32)
	// OnData is called with a fully reassembled data PDU.
	OnData(data []byte)
	// OnClose is called when the channel is closed or the connection ends.
	OnClose()
}

// dynChannel is the local state of one dynamic virtual channel.
type dynChannel struct {
	id      uint32
	name    string
	handler DynChannel
	buf     bytes.Buffer
	// want is the total length announced by a DATA_FIRST, or -1 when the
	// next data PDU is expected to be complete on its own.
	want int
}

// DvcClient drives the dynamic virtual channel layer.
type DvcClient struct {
	emission.Emitter
	w core.ChannelSender

	mu      sync.Mutex
	byName  map[string]DynChannel
	byID    map[uint32]*dynChannel
	nextID  uint32
	version uint16
}

// NewDvcClient creates a dynamic virtual channel layer.
func NewDvcClient() *DvcClient {
	return &DvcClient{
		Emitter: *emission.NewEmitter(),
		byName:  make(map[string]DynChannel),
		byID:    make(map[uint32]*dynChannel),
		nextID:  1,
		version: capsVersion3,
	}
}

// Register makes a handler available under a channel name. A channel only
// exists once the name is requested or the server asks for it.
func (c *DvcClient) Register(name string, h DynChannel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byName[name] = h
}

func (c *DvcClient) Sender(f core.ChannelSender) { c.w = f }

func (c *DvcClient) GetType() (string, uint32) { return ChannelName, ChannelOption }

// Send implements core.ChannelSender; it is how another layer writes to the
// static drdynvc channel.
func (c *DvcClient) Send(s []byte) (int, error) {
	if c.w == nil {
		return 0, fmt.Errorf("drdynvc: no channel sender")
	}
	name, _ := c.GetType()
	return c.w.SendToChannel(name, s)
}

// SendData writes a complete payload on an open dynamic virtual channel,
// splitting it into a DATA_FIRST followed by as many DATA PDUs as needed.
func (c *DvcClient) SendData(channelID uint32, data []byte) error {
	if len(data) <= maxDvcDataSize {
		h := header{cmd: cmdData, cbChID: c.channelIDBytes()}
		out := append(h.serialize(channelID), data...)
		_, err := c.Send(out)
		return err
	}

	h := header{cmd: cmdDataFirst, sp: 2, cbChID: c.channelIDBytes()}
	out := h.serialize(channelID)
	out = append(out, 0, 0, 0, 0) // total length, patched below
	binary.LittleEndian.PutUint32(out[len(out)-4:], uint32(len(data)))
	out = append(out, data[:maxDvcDataSize]...)
	if _, err := c.Send(out); err != nil {
		return err
	}

	for rest := data[maxDvcDataSize:]; len(rest) > 0; {
		n := len(rest)
		if n > maxDvcDataSize {
			n = maxDvcDataSize
		}
		h := header{cmd: cmdData, cbChID: c.channelIDBytes()}
		out := append(h.serialize(channelID), rest[:n]...)
		if _, err := c.Send(out); err != nil {
			return err
		}
		rest = rest[n:]
	}
	return nil
}

// Open requests a channel by name and returns its local id. The channel is
// only usable once the server confirms with a create response.
func (c *DvcClient) Open(name string) (uint32, error) {
	c.mu.Lock()
	if _, ok := c.byName[name]; !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("drdynvc: no handler registered for %q", name)
	}
	id := c.nextID
	for {
		if _, taken := c.byID[id]; !taken {
			break
		}
		id++
	}
	c.nextID = id + 1
	c.mu.Unlock()

	h := header{cmd: cmdCreateRequest, cbChID: c.channelIDBytes()}
	out := append(h.serialize(id), name...)
	if _, err := c.Send(out); err != nil {
		return 0, err
	}
	return id, nil
}

// HasChannel reports whether a channel name is currently open.
func (c *DvcClient) HasChannel(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.byID {
		if ch.name == name {
			return true
		}
	}
	return false
}

// channelIDBytes returns the cbChId code for the smallest encoding that can
// hold every id handed out so far.
func (c *DvcClient) channelIDBytes() uint8 {
	switch {
	case c.nextID <= 0xFF:
		return 0
	case c.nextID <= 0xFFFF:
		return 1
	default:
		return 2
	}
}

type header struct {
	cmd    uint8
	sp     uint8
	cbChID uint8
}

// readHeader parses the one byte command header.
func readHeader(r io.Reader) (*header, error) {
	v, err := core.ReadUInt8(r)
	if err != nil {
		return nil, err
	}
	return &header{
		cmd:    (v & 0xF0) >> 4,
		sp:     (v & 0x0C) >> 2,
		cbChID: v & 0x03,
	}, nil
}

// serialize writes the command header followed by the channel id.
func (h *header) serialize(channelID uint32) []byte {
	b := &bytes.Buffer{}
	core.WriteUInt8((h.cmd<<4)|(h.sp<<2)|h.cbChID, b)
	switch h.cbChID {
	case 0:
		core.WriteUInt8(uint8(channelID), b)
	case 1:
		core.WriteUInt16LE(uint16(channelID), b)
	default:
		core.WriteUInt32LE(channelID, b)
	}
	return b.Bytes()
}

// readChannelID reads the channel id encoded in cbChID bytes.
func readChannelID(r io.Reader, cbChID uint8) (uint32, error) {
	switch cbChID {
	case 0:
		v, err := core.ReadUInt8(r)
		return uint32(v), err
	case 1:
		v, err := core.ReadUint16LE(r)
		return uint32(v), err
	default:
		return core.ReadUInt32LE(r)
	}
}

// readVarLength reads the variable length prefix used by DATA_FIRST, whose
// width is selected by the sp field.
func readVarLength(r io.Reader, sp uint8) (uint32, error) {
	switch sp {
	case 0:
		v, err := core.ReadUInt8(r)
		return uint32(v), err
	case 1:
		v, err := core.ReadUint16LE(r)
		return uint32(v), err
	default:
		return core.ReadUInt32LE(r)
	}
}

// Process handles one reassembled drdynvc channel payload.
func (c *DvcClient) Process(s []byte) {
	glog.Tracef("drdynvc: recv %s", hex.EncodeToString(s))
	r := bytes.NewReader(s)
	for r.Len() > 0 {
		h, err := readHeader(r)
		if err != nil {
			return
		}
		if err := c.handle(h, r); err != nil {
			glog.Debugf("drdynvc: command 0x%x: %v", h.cmd, err)
			return
		}
	}
}

func (c *DvcClient) handle(h *header, r *bytes.Reader) error {
	switch h.cmd {
	case cmdCapabilities:
		return c.processCapabilities(r)
	case cmdCreateRequest:
		return c.processCreateRequest(h, r)
	case cmdDataFirst, cmdDataFirstCompressed:
		return c.processDataFirst(h, r, h.cmd == cmdDataFirstCompressed)
	case cmdData, cmdDataCompressed:
		return c.processData(h, r, h.cmd == cmdDataCompressed)
	case cmdClose:
		id, err := readChannelID(r, h.cbChID)
		if err != nil {
			return err
		}
		c.closeChannel(id)
		return nil
	case cmdSoftSyncRequest:
		// A soft sync request asks for a response echoing the same bytes.
		id, err := readChannelID(r, h.cbChID)
		if err != nil {
			return err
		}
		resp := header{cmd: cmdSoftSyncResponse, sp: h.sp, cbChID: h.cbChID}
		out := resp.serialize(id)
		out = append(out, lastN(r, 4)...)
		_, err = c.Send(out)
		return err
	default:
		glog.Debugf("drdynvc: unsupported command 0x%x", h.cmd)
		return nil
	}
}

// processCapabilities answers the server's capability announcement.
//
// The PDU is: a pad byte, the version, and, for version 2 and later, four
// priority charge thresholds. That is 12 bytes in total, which is worth
// spelling out because getting it wrong leaves trailing bytes that look like
// further (bogus) commands.
func (c *DvcClient) processCapabilities(r *bytes.Reader) error {
	if _, err := core.ReadUInt8(r); err != nil { // pad
		return err
	}
	version, err := core.ReadUint16LE(r)
	if err != nil {
		return err
	}
	if version >= capsVersion2 {
		// PriorityCharge0..3. They are advisory and unused here.
		for i := 0; i < 4; i++ {
			if _, err := core.ReadUint16LE(r); err != nil {
				return err
			}
		}
	}
	if version > capsVersion3 {
		version = capsVersion3
	}
	c.version = version
	glog.Debugf("drdynvc: server version %d", version)

	// The response header is the command byte (0x05 << 4) plus a pad byte,
	// which is what the reference client sends as the literal 0x0050.
	out := []byte{0x50, 0x00}
	out = append(out, byte(version), byte(version>>8))
	_, err = c.Send(out)
	return err
}

// processCreateRequest handles a create request from the server and replies
// with the creation status.
func (c *DvcClient) processCreateRequest(h *header, r *bytes.Reader) error {
	id, err := readChannelID(r, h.cbChID)
	if err != nil {
		return err
	}
	// The name is NUL terminated on the wire.
	name := strings.TrimRight(string(readAll(r)), "\x00")
	glog.Debugf("drdynvc: server requests channel id=%d name=%q", id, name)

	c.mu.Lock()
	handler, ok := c.byName[name]
	if ok {
		c.byID[id] = &dynChannel{id: id, name: name, handler: handler, want: -1}
	}
	c.mu.Unlock()

	// The response uses the create command with the same channel id, and a
	// four byte status instead of the channel name. STATUS_UNSUCCESSFUL is
	// what the reference client sends for an unknown channel.
	var status uint32 = 0
	if !ok {
		status = 0xC0000001
		glog.Debugf("drdynvc: no handler for %q, refusing", name)
	}

	out := append(h.serialize(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[len(out)-4:], status)
	if _, err := c.Send(out); err != nil {
		return err
	}

	if ok && handler != nil {
		handler.OnOpen(id)
	}
	return nil
}

// processDataFirst starts a reassembled payload.
func (c *DvcClient) processDataFirst(h *header, r *bytes.Reader, compressed bool) error {
	id, err := readChannelID(r, h.cbChID)
	if err != nil {
		return err
	}
	total, err := readVarLength(r, h.sp)
	if err != nil {
		return err
	}
	body := readAll(r)

	if compressed {
		// The compressed variants need the MPPC dictionary, which is not
		// implemented; report rather than emit nonsense.
		return fmt.Errorf("compressed dynamic virtual channel data is not supported")
	}

	c.mu.Lock()
	ch := c.byID[id]
	c.mu.Unlock()
	if ch == nil {
		return nil
	}

	ch.buf.Reset()
	ch.buf.Write(body)
	ch.want = int(total)
	if ch.buf.Len() >= ch.want {
		return c.deliver(ch)
	}
	return nil
}

// processData continues or completes a payload.
func (c *DvcClient) processData(h *header, r *bytes.Reader, compressed bool) error {
	id, err := readChannelID(r, h.cbChID)
	if err != nil {
		return err
	}
	body := readAll(r)
	if compressed {
		return fmt.Errorf("compressed dynamic virtual channel data is not supported")
	}

	c.mu.Lock()
	ch := c.byID[id]
	c.mu.Unlock()
	if ch == nil {
		return nil
	}

	// A DATA without a preceding DATA_FIRST is a complete payload.
	if ch.want < 0 {
		ch.buf.Reset()
		ch.buf.Write(body)
		return c.deliver(ch)
	}

	ch.buf.Write(body)
	if ch.buf.Len() >= ch.want {
		return c.deliver(ch)
	}
	return nil
}

// deliver hands a completed payload to the channel handler.
func (c *DvcClient) deliver(ch *dynChannel) error {
	data := make([]byte, len(ch.buf.Bytes()))
	copy(data, ch.buf.Bytes())
	ch.buf.Reset()
	ch.want = -1

	if ch.handler == nil {
		return nil
	}
	ch.handler.OnData(data)
	return nil
}

func (c *DvcClient) closeChannel(id uint32) {
	c.mu.Lock()
	ch := c.byID[id]
	delete(c.byID, id)
	c.mu.Unlock()
	if ch != nil && ch.handler != nil {
		ch.handler.OnClose()
	}
}

// CloseAll closes every open channel, which the connection teardown needs.
func (c *DvcClient) CloseAll() {
	c.mu.Lock()
	channels := make([]*dynChannel, 0, len(c.byID))
	for _, ch := range c.byID {
		channels = append(channels, ch)
	}
	c.byID = make(map[uint32]*dynChannel)
	c.mu.Unlock()

	for _, ch := range channels {
		if ch.handler != nil {
			ch.handler.OnClose()
		}
	}
}

// readAll drains the reader.
func readAll(r *bytes.Reader) []byte {
	b, _ := core.ReadBytes(r.Len(), r)
	return b
}

// lastN returns the final n bytes remaining in r, consuming them.
func lastN(r *bytes.Reader, n int) []byte {
	if r.Len() < n {
		n = r.Len()
	}
	b, _ := core.ReadBytes(n, r)
	return b
}
