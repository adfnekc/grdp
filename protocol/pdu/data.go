package pdu

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/glog"
	"github.com/lunixbochs/struc"
)

// The PDU types in a share control header, which is what says what the rest of the
// PDU is. These five are the ones on the slow path: the connection sequence, and the
// data PDUs that carry everything else.
const (
	PDUTYPE_DEMANDACTIVEPDU  = 0x11
	PDUTYPE_CONFIRMACTIVEPDU = 0x13
	PDUTYPE_DEACTIVATEALLPDU = 0x16
	PDUTYPE_DATAPDU          = 0x17
	PDUTYPE_SERVER_REDIR_PKT = 0x1A
)

// PduType2 is the second level of PDU type, which only exists inside a data PDU: the
// share control header says the PDU is a data PDU and this says what it carries.
//
// The values are sparse and the two directions have separate ranges, so both a
// server's updates and a client's input are in this one list.
type PduType2 uint8

// The data PDU types.
const (
	PDUTYPE2_UPDATE                      = 0x02
	PDUTYPE2_CONTROL                     = 0x14
	PDUTYPE2_POINTER                     = 0x1B
	PDUTYPE2_INPUT                       = 0x1C
	PDUTYPE2_SYNCHRONIZE                 = 0x1F
	PDUTYPE2_REFRESH_RECT                = 0x21
	PDUTYPE2_PLAY_SOUND                  = 0x22
	PDUTYPE2_SUPPRESS_OUTPUT             = 0x23
	PDUTYPE2_SHUTDOWN_REQUEST            = 0x24
	PDUTYPE2_SHUTDOWN_DENIED             = 0x25
	PDUTYPE2_SAVE_SESSION_INFO           = 0x26
	PDUTYPE2_FONTLIST                    = 0x27
	PDUTYPE2_FONTMAP                     = 0x28
	PDUTYPE2_SET_KEYBOARD_INDICATORS     = 0x29
	PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST = 0x2B
	PDUTYPE2_BITMAPCACHE_ERROR_PDU       = 0x2C
	PDUTYPE2_SET_KEYBOARD_IME_STATUS     = 0x2D
	PDUTYPE2_OFFSCRCACHE_ERROR_PDU       = 0x2E
	PDUTYPE2_SET_ERROR_INFO_PDU          = 0x2F
	PDUTYPE2_DRAWNINEGRID_ERROR_PDU      = 0x30
	PDUTYPE2_DRAWGDIPLUS_ERROR_PDU       = 0x31
	PDUTYPE2_ARC_STATUS_PDU              = 0x32
	PDUTYPE2_STATUS_INFO_PDU             = 0x36
	PDUTYPE2_MONITOR_LAYOUT_PDU          = 0x37
)

// String names the data message type, for logs and errors. An unrecognised value is
// reported as Unknown rather than as a number.
func (p PduType2) String() string {
	switch p {
	case PDUTYPE2_UPDATE:
		return "PDUTYPE2_UPDATE"
	case PDUTYPE2_CONTROL:
		return "PDUTYPE2_CONTROL"
	case PDUTYPE2_POINTER:
		return "PDUTYPE2_POINTER"
	case PDUTYPE2_INPUT:
		return "PDUTYPE2_INPUT"
	case PDUTYPE2_SYNCHRONIZE:
		return "PDUTYPE2_SYNCHRONIZE"
	case PDUTYPE2_REFRESH_RECT:
		return "PDUTYPE2_REFRESH_RECT"
	case PDUTYPE2_PLAY_SOUND:
		return "PDUTYPE2_PLAY_SOUND"
	case PDUTYPE2_SUPPRESS_OUTPUT:
		return "PDUTYPE2_SUPPRESS_OUTPUT"
	case PDUTYPE2_SHUTDOWN_REQUEST:
		return "PDUTYPE2_SHUTDOWN_REQUEST"
	case PDUTYPE2_SHUTDOWN_DENIED:
		return "PDUTYPE2_SHUTDOWN_DENIED"
	case PDUTYPE2_SAVE_SESSION_INFO:
		return "PDUTYPE2_SAVE_SESSION_INFO"
	case PDUTYPE2_FONTLIST:
		return "PDUTYPE2_FONTLIST"
	case PDUTYPE2_FONTMAP:
		return "PDUTYPE2_FONTMAP"
	case PDUTYPE2_SET_KEYBOARD_INDICATORS:
		return "PDUTYPE2_SET_KEYBOARD_INDICATORS"
	case PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST:
		return "PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST"
	case PDUTYPE2_BITMAPCACHE_ERROR_PDU:
		return "PDUTYPE2_BITMAPCACHE_ERROR_PDU"
	case PDUTYPE2_SET_KEYBOARD_IME_STATUS:
		return "PDUTYPE2_SET_KEYBOARD_IME_STATUS"
	case PDUTYPE2_OFFSCRCACHE_ERROR_PDU:
		return "PDUTYPE2_OFFSCRCACHE_ERROR_PDU"
	case PDUTYPE2_SET_ERROR_INFO_PDU:
		return "PDUTYPE2_SET_ERROR_INFO_PDU"
	case PDUTYPE2_DRAWNINEGRID_ERROR_PDU:
		return "PDUTYPE2_DRAWNINEGRID_ERROR_PDU"
	case PDUTYPE2_DRAWGDIPLUS_ERROR_PDU:
		return "PDUTYPE2_DRAWGDIPLUS_ERROR_PDU"
	case PDUTYPE2_ARC_STATUS_PDU:
		return "PDUTYPE2_ARC_STATUS_PDU"
	case PDUTYPE2_STATUS_INFO_PDU:
		return "PDUTYPE2_STATUS_INFO_PDU"
	case PDUTYPE2_MONITOR_LAYOUT_PDU:
		return "PDUTYPE2_MONITOR_LAYOUT_PDU"
	}

	return "Unknown"
}

// The control actions in a control PDU, which is how either side asks for the
// session's control or gives it up. Only one side can hold it, so this is what a
// takeover is: another client requesting control, which the server then asks this
// one about.
const (
	CTRLACTION_REQUEST_CONTROL = 0x0001
	CTRLACTION_GRANTED_CONTROL = 0x0002
	CTRLACTION_DETACH          = 0x0003
	CTRLACTION_COOPERATE       = 0x0004
)

// The stream identifiers in a share data header, which name the channel within a
// share that a data PDU belongs to. STREAM_LOW is what this client sends; the others
// exist so that a server's value can be read rather than treated as unknown.
const (
	STREAM_UNDEFINED = 0x00
	STREAM_LOW       = 0x01
	STREAM_MED       = 0x02
	STREAM_HI        = 0x04
)

// FastPathUpdateType is the update type in a fast-path PDU header. It is this
// level's equivalent of PduType2, and the two cover the same ground in different
// framings: a bitmap update can arrive either way.
type FastPathUpdateType uint8

// The fast-path update types. The pointer ones are separate types rather than one
// with a sub-field, which is why a shape and a position arrive as different PDUs.
//
// The list is not contiguous: 0x7 and 0xD are absent, and treating it as an index
// into a table would be wrong.
const (
	FASTPATH_UPDATETYPE_ORDERS        = 0x0
	FASTPATH_UPDATETYPE_BITMAP        = 0x1
	FASTPATH_UPDATETYPE_PALETTE       = 0x2
	FASTPATH_UPDATETYPE_SYNCHRONIZE   = 0x3
	FASTPATH_UPDATETYPE_SURFCMDS      = 0x4
	FASTPATH_UPDATETYPE_PTR_NULL      = 0x5
	FASTPATH_UPDATETYPE_PTR_DEFAULT   = 0x6
	FASTPATH_UPDATETYPE_PTR_POSITION  = 0x8
	FASTPATH_UPDATETYPE_COLOR         = 0x9
	FASTPATH_UPDATETYPE_CACHED        = 0xA
	FASTPATH_UPDATETYPE_POINTER       = 0xB
	FASTPATH_UPDATETYPE_LARGE_POINTER = 0xC
)

// String names the fast-path update type, for logs and errors. An unrecognised value
// is reported as Unknown rather than as a number.
func (t FastPathUpdateType) String() string {
	switch t {
	case FASTPATH_UPDATETYPE_ORDERS:
		return "FASTPATH_UPDATETYPE_ORDERS"
	case FASTPATH_UPDATETYPE_BITMAP:
		return "FASTPATH_UPDATETYPE_BITMAP"
	case FASTPATH_UPDATETYPE_PALETTE:
		return "FASTPATH_UPDATETYPE_PALETTE"
	case FASTPATH_UPDATETYPE_SYNCHRONIZE:
		return "FASTPATH_UPDATETYPE_SYNCHRONIZE"
	case FASTPATH_UPDATETYPE_SURFCMDS:
		return "FASTPATH_UPDATETYPE_SURFCMDS"
	case FASTPATH_UPDATETYPE_PTR_NULL:
		return "FASTPATH_UPDATETYPE_PTR_NULL"
	case FASTPATH_UPDATETYPE_PTR_DEFAULT:
		return "FASTPATH_UPDATETYPE_PTR_DEFAULT"
	case FASTPATH_UPDATETYPE_PTR_POSITION:
		return "FASTPATH_UPDATETYPE_PTR_POSITION"
	case FASTPATH_UPDATETYPE_COLOR:
		return "FASTPATH_UPDATETYPE_COLOR"
	case FASTPATH_UPDATETYPE_CACHED:
		return "FASTPATH_UPDATETYPE_CACHED"
	case FASTPATH_UPDATETYPE_POINTER:
		return "FASTPATH_UPDATETYPE_POINTER"
	case FASTPATH_UPDATETYPE_LARGE_POINTER:
		return "FASTPATH_UPDATETYPE_LARGE_POINTER"
	}

	return "Unknown"
}

// BITMAP_COMPRESSION is the flag in an update's bitmap flags that says the bitmap
// data is compressed, which is what decides whether the compressed data header is
// present.
//
// NO_BITMAP_COMPRESSION_HDR, which says the header is absent and the data starts
// immediately, is defined beside the other bitmap flags in caps.go and is handled
// where the update's rectangles are read.
const (
	BITMAP_COMPRESSION = 0x0001
)

// The compression types for the bulk compressor used on the slow path, which is the
// MPPC family.
//
// The three flags describe what a compressed block is: RDP_MPPC_COMPRESSED says it is
// compressed at all, and RESET and FLUSH are instructions to the decompressor's
// history rather than data, which is why they have to be acted on rather than skipped.
const (
	RDP_MPPC_BIG        = 0x01
	RDP_MPPC_COMPRESSED = 0x20
	RDP_MPPC_RESET      = 0x40
	RDP_MPPC_FLUSH      = 0x80
	RDP_MPPC_DICT_SIZE  = 65536
)

// ShareDataHeader is the header of every slow-path data PDU. It names the share, the
// stream and the PDU type, and it carries the uncompressed length, which is what a
// decompressor needs when the compressed length is the only other thing it is told.
type ShareDataHeader struct {
	SharedId           uint32 `struc:"little"`
	Padding1           uint8  `struc:"little"`
	StreamId           uint8  `struc:"little"`
	UncompressedLength uint16 `struc:"little"`
	PDUType2           uint8  `struc:"little"`
	CompressedType     uint8  `struc:"little"`
	CompressedLength   uint16 `struc:"little"`
}

// NewShareDataHeader returns a header for a data PDU of the given size, on the
// stream this client uses.
//
// The uncompressed length is the size plus four, which counts the header of the
// message inside the PDU: the length field is about the message rather than about the
// data alone, and getting it wrong is a decompression failure rather than a length
// check.
func NewShareDataHeader(size int, type2 uint8, shareId uint32) *ShareDataHeader {
	return &ShareDataHeader{
		SharedId:           shareId,
		PDUType2:           type2,
		StreamId:           STREAM_LOW,
		UncompressedLength: uint16(size + 4),
	}
}

// PDUMessage is a slow-path PDU that can be sent: it knows its type for the share
// control header and how to serialize itself.
type PDUMessage interface {
	Type() uint16
	Serialize() []byte
}

// DemandActivePDU is the server's first protocol PDU, which offers the session and
// lists the server's capabilities. The client answers with a ConfirmActivePDU.
//
// The capability sets are the important part: what the server advertises here decides
// what the client may ask for in its own list, so a client that has not read this
// cannot know what it is allowed to use.
type DemandActivePDU struct {
	SharedId                   uint32       `struc:"little"`
	LengthSourceDescriptor     uint16       `struc:"little,sizeof=SourceDescriptor"`
	LengthCombinedCapabilities uint16       `struc:"little"`
	SourceDescriptor           []byte       `struc:"sizefrom=LengthSourceDescriptor"`
	NumberCapabilities         uint16       `struc:"little,sizeof=CapabilitySets"`
	Pad2Octets                 uint16       `struc:"little"`
	CapabilitySets             []Capability `struc:"sizefrom=NumberCapabilities"`
	SessionId                  uint32       `struc:"little"`
}

// Type reports that this is a demand active PDU.
func (d *DemandActivePDU) Type() uint16 {
	return PDUTYPE_DEMANDACTIVEPDU
}

// Serialize returns the PDU. It is used by the tests and by the server side of an
// exchange, not by a client, which reads this PDU rather than sending it.
//
// Each capability set goes out with its type and a length that counts the four byte
// header of the set as well as the set, which is the length convention every
// capability uses.
func (d *DemandActivePDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt32LE(d.SharedId, buff)
	core.WriteUInt16LE(d.LengthSourceDescriptor, buff)
	core.WriteUInt16LE(d.LengthCombinedCapabilities, buff)
	core.WriteBytes([]byte(d.SourceDescriptor), buff)
	core.WriteUInt16LE(uint16(len(d.CapabilitySets)), buff)
	core.WriteUInt16LE(d.Pad2Octets, buff)
	for _, cap := range d.CapabilitySets {
		core.WriteUInt16LE(uint16(cap.Type()), buff)
		capBuff := &bytes.Buffer{}
		struc.Pack(capBuff, cap)
		capBytes := capBuff.Bytes()
		core.WriteUInt16LE(uint16(len(capBytes)+4), buff)
		core.WriteBytes(capBytes, buff)
	}
	core.WriteUInt32LE(d.SessionId, buff)
	return buff.Bytes()
}

func readDemandActivePDU(r io.Reader) (*DemandActivePDU, error) {
	d := &DemandActivePDU{}
	var err error
	d.SharedId, err = core.ReadUInt32LE(r)
	if err != nil {
		return nil, err
	}
	d.LengthSourceDescriptor, err = core.ReadUint16LE(r)
	d.LengthCombinedCapabilities, err = core.ReadUint16LE(r)
	sourceDescriptorBytes, err := core.ReadBytes(int(d.LengthSourceDescriptor), r)
	if err != nil {
		return nil, err
	}
	d.SourceDescriptor = sourceDescriptorBytes
	d.NumberCapabilities, err = core.ReadUint16LE(r)
	d.Pad2Octets, err = core.ReadUint16LE(r)
	d.CapabilitySets = make([]Capability, 0, d.NumberCapabilities)
	glog.Debug("NumberCapabilities is", d.NumberCapabilities)
	for i := 0; i < int(d.NumberCapabilities); i++ {
		c, err := readCapability(r)
		if err != nil {
			glog.Warn("read capability set:", err)
			continue
		}
		if c == nil {
			continue
		}
		d.CapabilitySets = append(d.CapabilitySets, c)
	}
	d.NumberCapabilities = uint16(len(d.CapabilitySets))
	d.SessionId, err = core.ReadUInt32LE(r)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// ConfirmActivePDU is the client's answer to the server's demand: the same shape with
// the client's capabilities in it, and this is where a client says what it can do.
//
// OriginatorId is what tells a server that this is the same client reconnecting
// rather than a new one.
type ConfirmActivePDU struct {
	SharedId                   uint32       `struc:"little"`
	OriginatorId               uint16       `struc:"little"`
	LengthSourceDescriptor     uint16       `struc:"little,sizeof=SourceDescriptor"`
	LengthCombinedCapabilities uint16       `struc:"little"`
	SourceDescriptor           []byte       `struc:"sizefrom=LengthSourceDescriptor"`
	NumberCapabilities         uint16       `struc:"little,sizeof=CapabilitySets"`
	Pad2Octets                 uint16       `struc:"little"`
	CapabilitySets             []Capability `struc:"sizefrom=NumberCapabilities"`
}

// Type reports that this is a confirm active PDU.
func (*ConfirmActivePDU) Type() uint16 {
	return PDUTYPE_CONFIRMACTIVEPDU
}

// Serialize returns the PDU, which is the message a client sends to accept the
// session and state its capabilities.
//
// The combined capabilities length is computed from the sets and written before them,
// so the sets have to be finalized before this is called.
func (c *ConfirmActivePDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt32LE(c.SharedId, buff)
	core.WriteUInt16LE(c.OriginatorId, buff)
	core.WriteUInt16LE(uint16(len(c.SourceDescriptor)), buff)

	capsBuff := &bytes.Buffer{}
	for _, capa := range c.CapabilitySets {
		core.WriteUInt16LE(uint16(capa.Type()), capsBuff)
		capBuff := &bytes.Buffer{}
		struc.Pack(capBuff, capa)
		capBytes := capBuff.Bytes()
		core.WriteUInt16LE(uint16(len(capBytes)+4), capsBuff)
		core.WriteBytes(capBytes, capsBuff)
	}
	capsBytes := capsBuff.Bytes()

	core.WriteUInt16LE(uint16(2+2+len(capsBytes)), buff)
	core.WriteBytes(c.SourceDescriptor, buff)
	core.WriteUInt16LE(c.NumberCapabilities, buff)
	core.WriteUInt16LE(c.Pad2Octets, buff)
	core.WriteBytes(capsBytes, buff)
	return buff.Bytes()
}

// 9401 => share control header
// 1300 => share control header
// ec03 => share control header
// ea030100  => shareId 66538
// ea03 => OriginatorId
// 0400
// 8001 => LengthCombinedCapabilities
// 72647079
// 0c00 => NumberCapabilities 12
// 0000
// caps below
// 010018000100030000020000000015040000000000000000
// 02001c00180001000100010000052003000000000100000001000000
// 030058000000000000000000000000000000000000000000010014000000010000000a0000000000000000000000000000000000000000000000000000000000000000000000000000000000008403000000000000000000
// 04002800000000000000000000000000000000000000000000000000000000000000000000000000
// 0800080000001400
// 0c00080000000000
// 0d005c001500000009040000040000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000c000000
// 0f00080000000000
// 10003400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000
// 11000c000000000000000000
// 14000c000000000000000000
// 1a00080000000000

// NewConfirmActivePDU returns an empty PDU for a client to fill in, with the source
// descriptor a client normally sends and no capability sets yet.
func NewConfirmActivePDU() *ConfirmActivePDU {
	return &ConfirmActivePDU{
		OriginatorId:     0x03EA,
		CapabilitySets:   make([]Capability, 0),
		SourceDescriptor: []byte("rdpy"),
	}
}

func readConfirmActivePDU(r io.Reader) (*ConfirmActivePDU, error) {
	p := &ConfirmActivePDU{}
	var err error
	p.SharedId, err = core.ReadUInt32LE(r)
	if err != nil {
		return nil, err
	}
	p.OriginatorId, err = core.ReadUint16LE(r)
	p.LengthSourceDescriptor, err = core.ReadUint16LE(r)
	p.LengthCombinedCapabilities, err = core.ReadUint16LE(r)

	sourceDescriptorBytes, err := core.ReadBytes(int(p.LengthSourceDescriptor), r)
	if err != nil {
		return nil, err
	}
	p.SourceDescriptor = sourceDescriptorBytes
	p.NumberCapabilities, err = core.ReadUint16LE(r)
	p.Pad2Octets, err = core.ReadUint16LE(r)

	p.CapabilitySets = make([]Capability, 0)
	for i := 0; i < int(p.NumberCapabilities); i++ {
		c, err := readCapability(r)
		if err != nil {
			return nil, err
		}
		p.CapabilitySets = append(p.CapabilitySets, c)
	}
	s, _ := core.ReadUInt32LE(r)
	glog.Info("sessionid:", s)
	return p, nil
}

// DeactiveAllPDU ends the session. It is sent by whichever side is stopping, and a
// client that receives one should treat the session as over rather than as dropped.
type DeactiveAllPDU struct {
	ShareId                uint32 `struc:"little"`
	LengthSourceDescriptor uint16 `struc:"little,sizeof=SourceDescriptor"`
	SourceDescriptor       []byte
}

// Type reports that this is a deactivate all PDU.
func (*DeactiveAllPDU) Type() uint16 {
	return PDUTYPE_DEACTIVATEALLPDU
}

// Serialize returns the PDU, which is its header and nothing else: the PDU carries no
// data beyond the share control header that names it.
func (d *DeactiveAllPDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, d)
	return buff.Bytes()
}

func readDeactiveAllPDU(r io.Reader) (*DeactiveAllPDU, error) {
	p := &DeactiveAllPDU{}
	err := struc.Unpack(r, p)
	return p, err
}

// DataPDU is the slow-path PDU that carries everything after the connection
// sequence: updates, pointer shapes, input and session messages.
//
// The header is the share data header and the data is whatever DataPDUData says it
// is, which is decided by the PduType2 in that header rather than by anything in the
// data itself.
type DataPDU struct {
	Header *ShareDataHeader
	Data   DataPDUData
}

// Type reports that this is a data PDU.
func (*DataPDU) Type() uint16 {
	return PDUTYPE_DATAPDU
}

// Serialize returns the PDU: the share data header, then the data's own serialized
// form.
//
// The length in the header is computed from the serialized data, so the data has to
// be serialized before the header is written, which is why this builds the data first
// and the header second even though they go out the other way round.
func (d *DataPDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, d.Header)
	struc.Pack(buff, d.Data)
	return buff.Bytes()
}

// NewDataPDU returns a data PDU carrying the given data on the given share, with the
// header filled in from the data's own type.
func NewDataPDU(data DataPDUData, shareId uint32) *DataPDU {
	dataBuff := &bytes.Buffer{}
	struc.Pack(dataBuff, data)
	return &DataPDU{
		Header: NewShareDataHeader(len(dataBuff.Bytes()), data.Type2(), shareId),
		Data:   data,
	}
}

func readDataPDU(r io.Reader) (*DataPDU, error) {
	header := &ShareDataHeader{}
	err := struc.Unpack(r, header)
	if err != nil {
		glog.Error("read data pdu header error", err)
		return nil, err
	}
	var d DataPDUData
	glog.Debugf("PDUType2 0x%02x", header.PDUType2)
	switch header.PDUType2 {
	case PDUTYPE2_UPDATE:
		d = &UpdateDataPDU{}

	case PDUTYPE2_SYNCHRONIZE:
		d = &SynchronizeDataPDU{}

	case PDUTYPE2_CONTROL:
		d = &ControlDataPDU{}

	case PDUTYPE2_POINTER:
		d = &PointerDataPDU{}

	case PDUTYPE2_FONTLIST:
		d = &FontListDataPDU{}

	case PDUTYPE2_SET_ERROR_INFO_PDU:
		d = &ErrorInfoDataPDU{}

	case PDUTYPE2_FONTMAP:
		d = &FontMapDataPDU{}

	case PDUTYPE2_SAVE_SESSION_INFO:
		d = &SaveSessionInfo{}

	default:
		err = errors.New(fmt.Sprintf("Unknown data pdu type2 0x%02x", header.PDUType2))
		glog.Error(err)
		return nil, err
	}

	err = d.Unpack(r)
	if err != nil {
		glog.Error("Read data pdu:", err)
		return nil, err
	}

	p := &DataPDU{
		Header: header,
		Data:   d,
	}
	return p, nil
}

// DataPDUData is the payload of a data PDU. Type2 is the second level PDU type that
// goes in the share data header, and Unpack reads the payload once that header has
// said what it is.
type DataPDUData interface {
	Type2() uint8
	Unpack(io.Reader) error
}

// UpdateDataPDU is the slow-path update, which carries a list of update structures:
// bitmap updates, pointer updates, palette updates and synchronize marks.
type UpdateDataPDU struct {
	UpdateType uint16
	Udata      UpdateData
}

// Type2 reports that this is an update.
func (*UpdateDataPDU) Type2() uint8 {
	return PDUTYPE2_UPDATE
}

// Unpack reads the update list.
//
// The header's update type decides how many of the header's bytes are present: the
// count is one or two bytes depending on whether the update type has its high bit
// set, and reading the wrong width takes a byte from the first update and misaligns
// everything after it.
func (d *UpdateDataPDU) Unpack(r io.Reader) (err error) {
	//slow path update
	d.UpdateType, err = core.ReadUint16LE(r)
	glog.Debugf("UpdateType 0x%02x", d.UpdateType)
	var p UpdateData
	switch d.UpdateType {
	case FASTPATH_UPDATETYPE_ORDERS:
	case FASTPATH_UPDATETYPE_BITMAP:
		p = &BitmapUpdateDataPDU{}
	case FASTPATH_UPDATETYPE_PALETTE:
	case FASTPATH_UPDATETYPE_SYNCHRONIZE:
	}
	if p != nil {
		err = p.Unpack(r)
		if err != nil {
			//glog.Error("Unpack:", err)
			return err
		}
	} else {
		return errors.New(fmt.Sprintf("Unsupport slow update type 0x%x", d.UpdateType))
	}

	d.Udata = p

	return nil
}

// BitmapUpdateDataPDU is the first update structure in an update: the bitmap update,
// which carries one or more bitmaps and is what draws the screen on the slow path.
type BitmapUpdateDataPDU struct {
	NumberRectangles uint16 `struc:"little,sizeof=Rectangles"`
	Rectangles       []BitmapData
}

// FastPathUpdateType reports that this is a bitmap update, in the fast path's own
// numbering rather than the slow path's.
func (*BitmapUpdateDataPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_BITMAP
}

// Unpack reads a bitmap update: the header, then each rectangle's bounds and its
// bitmap data.
//
// The bitmap lengths are bounded against what is left of the update before anything is
// read from them, and a length that does not fit ends the update rather than being
// trusted, which is what stops a corrupt length becoming an allocation.
func (f *BitmapUpdateDataPDU) Unpack(r io.Reader) error {
	var err error
	f.NumberRectangles, err = core.ReadUint16LE(r)
	f.Rectangles = make([]BitmapData, 0, f.NumberRectangles)
	for i := 0; i < int(f.NumberRectangles); i++ {
		rect := BitmapData{}
		rect.DestLeft, err = core.ReadUint16LE(r)
		rect.DestTop, err = core.ReadUint16LE(r)
		rect.DestRight, err = core.ReadUint16LE(r)
		rect.DestBottom, err = core.ReadUint16LE(r)
		rect.Width, err = core.ReadUint16LE(r)
		rect.Height, err = core.ReadUint16LE(r)
		rect.BitsPerPixel, err = core.ReadUint16LE(r)
		rect.Flags, err = core.ReadUint16LE(r)
		rect.BitmapLength, err = core.ReadUint16LE(r)
		ln := rect.BitmapLength
		if rect.Flags&BITMAP_COMPRESSION != 0 && (rect.Flags&NO_BITMAP_COMPRESSION_HDR == 0) {
			rect.BitmapComprHdr = new(BitmapCompressedDataHeader)
			rect.BitmapComprHdr.CbCompFirstRowSize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbCompMainBodySize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbScanWidth, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbUncompressedSize, err = core.ReadUint16LE(r)
			ln = rect.BitmapComprHdr.CbCompMainBodySize
		}

		rect.BitmapDataStream, err = core.ReadBytes(int(ln), r)
		f.Rectangles = append(f.Rectangles, rect)
	}
	return err
}

// SynchronizeDataPDU is the server's synchronize mark, sent once after the
// capabilities are exchanged and expected to be answered by the client.
//
// It carries the target user id, which the server uses to tell one session's input
// from another's.
type SynchronizeDataPDU struct {
	MessageType uint16 `struc:"little"`
	TargetUser  uint16 `struc:"little"`
}

// Type2 reports that this is a synchronize message.
func (*SynchronizeDataPDU) Type2() uint8 {
	return PDUTYPE2_SYNCHRONIZE
}

// NewSynchronizeDataPDU returns a synchronize message for the given target user,
// which is what the client sends in answer to the server's own.
func NewSynchronizeDataPDU(targetUser uint16) *SynchronizeDataPDU {
	return &SynchronizeDataPDU{
		MessageType: 1,
		TargetUser:  targetUser,
	}
}

// Unpack reads the target user id.
func (d *SynchronizeDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// ControlDataPDU is the control message, which carries a control action: a request
// for the session's control, a grant of it, a detach, or a note that the sender is
// cooperating.
type ControlDataPDU struct {
	Action    uint16 `struc:"little"`
	GrantId   uint16 `struc:"little"`
	ControlId uint32 `struc:"little"`
}

// Type2 reports that this is a control message.
func (*ControlDataPDU) Type2() uint8 {
	return PDUTYPE2_CONTROL
}

// Unpack reads the action and its fields.
func (d *ControlDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// FontListDataPDU is the server's list of the fonts it will use in drawing orders,
// which the client answers by choosing the ones it has.
type FontListDataPDU struct {
	NumberFonts   uint16 `struc:"little"`
	TotalNumFonts uint16 `struc:"little"`
	ListFlags     uint16 `struc:"little"`
	EntrySize     uint16 `struc:"little"`
}

// Type2 reports that this is a font list.
func (*FontListDataPDU) Type2() uint8 {
	return PDUTYPE2_FONTLIST
}

// Unpack reads the font entries, each of which is a count of characters and the index
// of the font in the cache.
func (d *FontListDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// ErrorInfoDataPDU is the server's report of why it disconnected, as a code. It
// arrives before the session ends and is the most useful thing in a failed session
// after the security layer's errors.
type ErrorInfoDataPDU struct {
	ErrorInfo uint32 `struc:"little"`
}

// Type2 reports that this is an error info message.
func (*ErrorInfoDataPDU) Type2() uint8 {
	return PDUTYPE2_SET_ERROR_INFO_PDU
}

// Unpack reads the error code.
func (d *ErrorInfoDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// FontMapDataPDU is the client's answer to a font list: the mapping from the server's
// font indexes to the indexes of the client's own fonts.
type FontMapDataPDU struct {
	NumberEntries   uint16 `struc:"little"`
	TotalNumEntries uint16 `struc:"little"`
	MapFlags        uint16 `struc:"little"`
	EntrySize       uint16 `struc:"little"`
}

// Type2 reports that this is a font map.
func (*FontMapDataPDU) Type2() uint8 {
	return PDUTYPE2_FONTMAP
}

// Unpack reads the mapping: a count, then one entry per server font.
func (d *FontMapDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// InfoType is the kind of logon information in a save session info message. The four
// kinds carry different fields, so the value is what decides how the body is read.
//
// See MS-RDPBCGR 2.2.10.1.1.4.
type InfoType uint32

// The logon notification types in a save session info message.
//
// LOGON and LOGON_LONG carry the credentials fields in their short and long forms, and
// the extended one carries the reconnect cookie: a client that does not read the
// fourth can never use auto reconnect, because that is the only message the cookie
// arrives in.
const (
	INFOTYPE_LOGON               = 0x00000000
	INFOTYPE_LOGON_LONG          = 0x00000001
	INFOTYPE_LOGON_PLAINNOTIFY   = 0x00000002
	INFOTYPE_LOGON_EXTENDED_INFO = 0x00000003
)

// The extended logon flags, which say what an extended logon notification carries.
// AUTORECONNECTCOOKIE is the one that matters: its presence is what says a cookie
// follows, and the cookie's length is what says how much of the body is unread.
const (
	LOGON_EX_AUTORECONNECTCOOKIE = 0x00000001
	LOGON_EX_LOGONERRORS         = 0x00000002
)

// LogonFields are the long form of the logon information: the domain, user name and
// shell, each as a length and a string that may be encoded as Unicode.
//
// The fields are named after the Windows LogonInfo structure rather than being
// rearranged, because the order they appear in is what the wire says and not a choice
// this client makes.
type LogonFields struct {
	CbFileData uint32   `struc:"little"`
	Len        uint32   //28 `struc:"little"`
	Version    uint32   // 1 `struc:"little"`
	LogonId    uint32   `struc:"little"`
	random     [16]byte //16 `struc:"little"`
}

// SaveSessionInfo is the server's logon notification, which is how a session reports
// that a logon succeeded, that it failed, or that a reconnect cookie has been
// issued.
//
// What it carries depends entirely on its InfoType, which is why the four forms are
// read by four separate methods rather than by one that switches: the fields have
// nothing in common beyond the header.
type SaveSessionInfo struct {
	InfoType      uint32
	Length        uint16
	FieldsPresent uint32
	LogonId       uint32
	Random        []byte
}

func (s *SaveSessionInfo) logonInfoV1(r io.Reader) (err error) {
	core.ReadUInt32LE(r) // cbDomain
	b, _ := core.ReadBytes(52, r)
	domain := core.UnicodeDecode(b)

	core.ReadUInt32LE(r) // cbUserName
	b, _ = core.ReadBytes(512, r)
	userName := core.UnicodeDecode(b)

	sessionId, _ := core.ReadUInt32LE(r)
	s.LogonId = sessionId
	glog.Infof("SessionId:[%d] UserName:[%s] Domain:[%s]", s.LogonId, userName, domain)
	return err
}
func (s *SaveSessionInfo) logonInfoV2(r io.Reader) (err error) {
	core.ReadUint16LE(r)
	core.ReadUInt32LE(r)
	sessionId, _ := core.ReadUInt32LE(r)
	s.LogonId = sessionId
	cbDomain, _ := core.ReadUInt32LE(r)
	cbUserName, _ := core.ReadUInt32LE(r)
	core.ReadBytes(558, r)

	b, _ := core.ReadBytes(int(cbDomain), r)
	domain := core.UnicodeDecode(b)
	b, _ = core.ReadBytes(int(cbUserName), r)
	userName := core.UnicodeDecode(b)
	glog.Infof("SessionId:[%d] UserName:[ %s] Domain:[ %s]", s.LogonId, userName, domain)

	return err
}
func (s *SaveSessionInfo) logonPlainNotify(r io.Reader) (err error) {
	core.ReadBytes(576, r) /* pad (576 bytes) */
	return err
}
func (s *SaveSessionInfo) logonInfoExtended(r io.Reader) (err error) {
	s.Length, err = core.ReadUint16LE(r)
	s.FieldsPresent, err = core.ReadUInt32LE(r)
	//glog.Info("FieldsPresent:", s.FieldsPresent)
	// auto reconnect cookie
	if s.FieldsPresent&LOGON_EX_AUTORECONNECTCOOKIE != 0 {
		core.ReadUInt32LE(r)
		b, _ := core.ReadUInt32LE(r)
		if b != 28 {
			return errors.New(fmt.Sprintf("invalid length in Auto-Reconnect packet"))
		}
		b, _ = core.ReadUInt32LE(r)
		if b != 1 {
			return errors.New(fmt.Sprintf("unsupported version of Auto-Reconnect packet"))
		}
		b, _ = core.ReadUInt32LE(r)
		s.LogonId = b
		s.Random, _ = core.ReadBytes(16, r)
	} else { // logon error info
		core.ReadUInt32LE(r)
		b, _ := core.ReadUInt32LE(r)
		b, _ = core.ReadUInt32LE(r)
		s.LogonId = b
	}
	core.ReadBytes(570, r)
	return err
}

// Unpack reads the logon notification, dispatching on the info type.
//
// An info type that is not one of the four is read as having no body rather than
// being reported as an error, so a caller cannot tell an unknown notification from an
// empty one here.
func (s *SaveSessionInfo) Unpack(r io.Reader) (err error) {
	s.InfoType, err = core.ReadUInt32LE(r)
	switch s.InfoType {
	case INFOTYPE_LOGON:
		err = s.logonInfoV1(r)
	case INFOTYPE_LOGON_LONG:
		err = s.logonInfoV2(r)
	case INFOTYPE_LOGON_PLAINNOTIFY:
		err = s.logonPlainNotify(r)
	case INFOTYPE_LOGON_EXTENDED_INFO:
		err = s.logonInfoExtended(r)
	default:
		glog.Errorf("Unhandled saveSessionInfo type 0x%x", s.InfoType)
		return fmt.Errorf("Unhandled saveSessionInfo type 0x%x", s.InfoType)
	}

	return err
}

// Type2 reports that this is a logon notification.
func (*SaveSessionInfo) Type2() uint8 {
	return PDUTYPE2_SAVE_SESSION_INFO
}

// PersistKeyPDU is the persistent bitmap cache key list, which the server sends when
// it wants the client to keep cached bitmaps across sessions.
type PersistKeyPDU struct {
	NumEntriesCache0   uint16 `struc:"little"`
	NumEntriesCache1   uint16 `struc:"little"`
	NumEntriesCache2   uint16 `struc:"little"`
	NumEntriesCache3   uint16 `struc:"little"`
	NumEntriesCache4   uint16 `struc:"little"`
	TotalEntriesCache0 uint16 `struc:"little"`
	TotalEntriesCache1 uint16 `struc:"little"`
	TotalEntriesCache2 uint16 `struc:"little"`
	TotalEntriesCache3 uint16 `struc:"little"`
	TotalEntriesCache4 uint16 `struc:"little"`
	BBitMask           uint8  `struc:"little"`
	Pad1               uint8  `struc:"little"`
	Ppad3              uint16 `struc:"little"`
}

// Type2 reports that this is a persistent bitmap cache key list.
func (*PersistKeyPDU) Type2() uint8 {
	return PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST
}

// UpdateData is the body of an update: the interface every update structure
// implements, so that a list of them can be walked without knowing in advance what
// kinds are in it.
type UpdateData interface {
	FastPathUpdateType() uint8
	Unpack(io.Reader) error
}

// BitmapCompressedDataHeader is the header in front of compressed bitmap data, which
// is absent when the update's flags say so.
//
// The header is what makes the compressed data self-describing: it gives the length
// and the uncompressed size, so a decompressor does not have to guess where the
// bitmap ends.
type BitmapCompressedDataHeader struct {
	CbCompFirstRowSize uint16 `struc:"little"`
	CbCompMainBodySize uint16 `struc:"little"`
	CbScanWidth        uint16 `struc:"little"`
	CbUncompressedSize uint16 `struc:"little"`
}

// BitmapData is one bitmap in a bitmap update: where it goes, and its pixels in
// whatever form the flags say.
//
// The data can be raw, RLE compressed or codec compressed, and the flags are what
// decide which: the same field holds all three, and reading it as the wrong one
// produces garbage rather than a failure.
type BitmapData struct {
	DestLeft         uint16 `struc:"little"`
	DestTop          uint16 `struc:"little"`
	DestRight        uint16 `struc:"little"`
	DestBottom       uint16 `struc:"little"`
	Width            uint16 `struc:"little"`
	Height           uint16 `struc:"little"`
	BitsPerPixel     uint16 `struc:"little"`
	Flags            uint16 `struc:"little"`
	BitmapLength     uint16 `struc:"little,sizeof=BitmapDataStream"`
	BitmapComprHdr   *BitmapCompressedDataHeader
	BitmapDataStream []byte
}

// IsCompress reports whether the bitmap's data is compressed, which decides whether
// there is a compressed data header in front of it and whether the data goes through
// the bulk decompressor or is read as pixels.
func (b *BitmapData) IsCompress() bool {
	return b.Flags&BITMAP_COMPRESSION != 0
}

// FastPathBitmapUpdateDataPDU is the fast-path bitmap update, which is the same
// update as the slow path carries in a different framing.
//
// The screen is cut into rectangles rather than sent whole, and each rectangle is
// decoded on its own, which is why the region count and the per rectangle bounds are
// read before any pixels are.
type FastPathBitmapUpdateDataPDU struct {
	Header           uint16 `struc:"little"`
	NumberRectangles uint16 `struc:"little,sizeof=Rectangles"`
	Rectangles       []BitmapData
}

// Unpack reads the update: the region count, then each rectangle's bounds and its
// bitmap data, until the update is used up.
//
// Each rectangle is bounded before anything is allocated from it, so a length that
// does not fit in what is left of the update is an error rather than an allocation of
// whatever it claims.
func (f *FastPathBitmapUpdateDataPDU) Unpack(r io.Reader) error {
	var err error
	f.Header, err = core.ReadUint16LE(r)
	f.NumberRectangles, err = core.ReadUint16LE(r)
	f.Rectangles = make([]BitmapData, 0, f.NumberRectangles)
	for i := 0; i < int(f.NumberRectangles); i++ {
		rect := BitmapData{}
		rect.DestLeft, err = core.ReadUint16LE(r)
		rect.DestTop, err = core.ReadUint16LE(r)
		rect.DestRight, err = core.ReadUint16LE(r)
		rect.DestBottom, err = core.ReadUint16LE(r)
		rect.Width, err = core.ReadUint16LE(r)
		rect.Height, err = core.ReadUint16LE(r)
		rect.BitsPerPixel, err = core.ReadUint16LE(r)
		rect.Flags, err = core.ReadUint16LE(r)
		rect.BitmapLength, err = core.ReadUint16LE(r)
		ln := rect.BitmapLength
		if rect.Flags&BITMAP_COMPRESSION != 0 && (rect.Flags&NO_BITMAP_COMPRESSION_HDR == 0) {
			rect.BitmapComprHdr = new(BitmapCompressedDataHeader)
			rect.BitmapComprHdr.CbCompFirstRowSize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbCompMainBodySize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbScanWidth, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbUncompressedSize, err = core.ReadUint16LE(r)
			ln = rect.BitmapComprHdr.CbCompMainBodySize
		}

		rect.BitmapDataStream, err = core.ReadBytes(int(ln), r)
		f.Rectangles = append(f.Rectangles, rect)
	}
	return err
}

// FastPathUpdateType reports that this is a bitmap update.
func (*FastPathBitmapUpdateDataPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_BITMAP
}

// FastPathColorPdu is a palette update on the fast path: the entries that changed and
// the index they start at.
type FastPathColorPdu struct {
	CacheIdx uint16
	X        uint16
	Y        uint16
	Width    uint16
	Height   uint16
	MaskLen  uint16 `struc:"little,sizeof=Mask"`
	DataLen  uint16 `struc:"little,sizeof=Data"`
	Mask     []byte
	Data     []byte
}

// FastPathUpdateType reports that this is a palette update.
func (*FastPathColorPdu) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_COLOR
}

// Unpack reads the palette entries.
func (f *FastPathColorPdu) Unpack(r io.Reader) error {
	return struc.Unpack(r, f)
}

// FastPathSurfaceCmds is a group of surface commands delivered on the fast path
// instead of the dynamic channel, which is what a server may do once the client has
// advertised surface command support.
type FastPathSurfaceCmds struct {
}

// FastPathUpdateType reports that this is a group of surface commands.
func (*FastPathSurfaceCmds) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_SURFCMDS
}

// Unpack reads the surface commands and hands them to the same handler the dynamic
// channel uses, so a surface arriving either way is drawn the same.
func (f *FastPathSurfaceCmds) Unpack(r io.Reader) error {
	cmdType, _ := core.ReadUint16LE(r)
	switch cmdType {

	}

	return nil
}

// FastPathUpdatePDU is a fast-path PDU that carries update data rather than input or
// pointer traffic.
//
// Its header holds a fragmentation flag as well as the update type, because a large
// update can be split across several PDUs. A fragment other than the last has to be
// joined with the ones around it before it can be parsed, and parsing one on its own
// gives a truncated update rather than an error.
type FastPathUpdatePDU struct {
	UpdateHeader     uint8
	Fragmentation    uint8
	CompressionFlags uint8
	Size             uint16
	Data             UpdateData
}

// FASTPATH_OUTPUT_COMPRESSION_USED is the other flag in a fast-path header's option
// bits, which says the payload is compressed with the bulk compressor. It is not the
// same as the surface commands' own compression, and a reader that confuses the two
// feeds the wrong data to the wrong decoder.
const (
	FASTPATH_OUTPUT_COMPRESSION_USED = 0x2
)

// The fragmentation flags in a fast-path header, which say whether the PDU carries a
// whole update or one part of one.
//
// A large update can be split across several PDUs, and only LAST ends one, so a reader
// that treats every PDU as complete parses a fragment and produces a truncated update
// rather than an error.
const (
	FASTPATH_FRAGMENT_SINGLE = (0x0 << 4)
	FASTPATH_FRAGMENT_LAST   = (0x1 << 4)
	FASTPATH_FRAGMENT_FIRST  = (0x2 << 4)
	FASTPATH_FRAGMENT_NEXT   = (0x3 << 4)
)

func readFastPathUpdatePDU(r io.Reader, code uint8, state *OrderState) (*FastPathUpdatePDU, error) {
	f := &FastPathUpdatePDU{}
	var err error
	var d UpdateData
	//glog.Debugf("FastPathPDU type %s(0x%x)", FastPathUpdateType(code), code)
	switch code {
	case FASTPATH_UPDATETYPE_ORDERS:
		d = &FastPathOrdersPDU{State: state}
	case FASTPATH_UPDATETYPE_BITMAP:
		d = &FastPathBitmapUpdateDataPDU{}
	case FASTPATH_UPDATETYPE_SURFCMDS:
		d = &SurfaceCommandsPDU{}
	case FASTPATH_UPDATETYPE_PALETTE:
		d = &FastPathNoopPDU{Type: FASTPATH_UPDATETYPE_PALETTE}
	case FASTPATH_UPDATETYPE_SYNCHRONIZE:
		d = &FastPathNoopPDU{Type: FASTPATH_UPDATETYPE_SYNCHRONIZE}
	case FASTPATH_UPDATETYPE_PTR_NULL:
		d = &FastPathNoopPDU{Type: FASTPATH_UPDATETYPE_PTR_NULL}
	case FASTPATH_UPDATETYPE_PTR_DEFAULT:
		d = &FastPathNoopPDU{Type: FASTPATH_UPDATETYPE_PTR_DEFAULT}
	case FASTPATH_UPDATETYPE_PTR_POSITION:
		d = &FastPathPointerPositionPDU{}
	case FASTPATH_UPDATETYPE_COLOR:
		d = &FastPathPointerPDU{UpdateType: FASTPATH_UPDATETYPE_COLOR}
	case FASTPATH_UPDATETYPE_CACHED:
		d = &FastPathPointerPDU{UpdateType: FASTPATH_UPDATETYPE_CACHED}
	case FASTPATH_UPDATETYPE_POINTER:
		d = &FastPathPointerPDU{UpdateType: FASTPATH_UPDATETYPE_POINTER}
	case FASTPATH_UPDATETYPE_LARGE_POINTER:
		d = &FastPathPointerPDU{UpdateType: FASTPATH_UPDATETYPE_LARGE_POINTER}
	default:
		glog.Debugf("Unknown FastPathPDU type 0x%x", code)
		return f, errors.New(fmt.Sprintf("Unknown FastPathPDU type 0x%x", code))
	}
	if d != nil {
		err = d.Unpack(r)
		if err != nil {
			//glog.Error("Unpack:", err)
			return nil, err
		}
	} else {
		return nil, errors.New(fmt.Sprintf("Unsupport FastPathPDU type 0x%x", code))
	}

	f.Data = d
	return f, nil
}

// ShareControlHeader is the header in front of every slow-path PDU: its total length,
// the PDU type, and the source or originator that sent it.
//
// The length counts the header, so it is what tells a reader where the PDU ends
// without having to understand the PDU itself.
type ShareControlHeader struct {
	TotalLength uint16 `struc:"little"`
	PDUType     uint16 `struc:"little"`
	PDUSource   uint16 `struc:"little"`
}

// PDU is a slow-path PDU: the share control header, and the message it carries.
type PDU struct {
	ShareCtrlHeader *ShareControlHeader
	Message         PDUMessage
}

// NewPDU wraps a message in a share control header, taking the type from the message
// itself so that the two cannot disagree.
func NewPDU(userId uint16, message PDUMessage) *PDU {
	pdu := &PDU{}
	pdu.ShareCtrlHeader = &ShareControlHeader{
		TotalLength: uint16(len(message.Serialize()) + 6),
		PDUType:     message.Type(),
		PDUSource:   userId,
	}
	pdu.Message = message
	return pdu
}

func readPDU(r io.Reader) (*PDU, error) {
	pdu := &PDU{}
	var err error
	header := &ShareControlHeader{}
	err = struc.Unpack(r, header)
	if err != nil {
		return nil, err
	}

	pdu.ShareCtrlHeader = header

	var d PDUMessage
	switch pdu.ShareCtrlHeader.PDUType {
	case PDUTYPE_DEMANDACTIVEPDU:
		glog.Debug("PDUTYPE_DEMANDACTIVEPDU")
		d, err = readDemandActivePDU(r)
	case PDUTYPE_DATAPDU:
		glog.Debug("PDUTYPE_DATAPDU")
		d, err = readDataPDU(r)
	case PDUTYPE_CONFIRMACTIVEPDU:
		glog.Debug("PDUTYPE_CONFIRMACTIVEPDU")
		d, err = readConfirmActivePDU(r)
	case PDUTYPE_DEACTIVATEALLPDU:
		glog.Debug("PDUTYPE_DEACTIVATEALLPDU")
		d, err = readDeactiveAllPDU(r)
	default:
		glog.Errorf("PDU invalid pdu type: 0x%02x", pdu.ShareCtrlHeader.PDUType)
	}
	if err != nil {
		return nil, err
	}
	pdu.Message = d
	return pdu, err
}

func (p *PDU) serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, p.ShareCtrlHeader)
	core.WriteBytes(p.Message.Serialize(), buff)
	return buff.Bytes()
}

// SlowPathInputEvent is one input event on the slow path, written straight to the
// wire rather than through a serialize method: the fields are in the order they are
// sent.
//
// The slow path carries a full event time and a message type per event, which the fast
// path has no room for, and that is the whole difference between the two framings of
// input.
type SlowPathInputEvent struct {
	EventTime         uint32 `struc:"little"`
	MessageType       uint16 `struc:"little"`
	Size              int    `struc:"skip"`
	SlowPathInputData []byte `struc:"sizefrom=Size"`
}

// PointerEvent is a pointer event: the flags, which say whether this is a movement, a
// button or a wheel, and the position.
//
// The flags are where the meaning is. A movement carries PTRFLAGS_MOVE and a button
// carries PTRFLAGS_DOWN and the button, and mixing them makes a server drop the event
// rather than interpret it.
type PointerEvent struct {
	PointerFlags uint16 `struc:"little"`
	XPos         uint16 `struc:"little"`
	YPos         uint16 `struc:"little"`
}

// Serialize returns the event in its fast-path form: pointer flags, then x and y
// little endian. The event code is not written here, because a fast-path PDU carries
// it in the event header rather than in the event.
func (p *PointerEvent) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, p)
	return buff.Bytes()
}

// SynchronizeEvent carries the state of the toggle keys: caps lock, num lock, scroll
// lock and the kana lock.
//
// It has no fields of its own because the state travels in the event header's own low
// bits on the fast path, which is why the caller passes a flag word to the send call
// rather than filling a structure in.
type SynchronizeEvent struct {
	Pad2Octets  uint16 `struc:"little"`
	ToggleFlags uint32 `struc:"little"`
}

// Serialize returns nothing, which is correct: the toggle state is not in the body of
// this event but in the low bits of the fast-path event header, so the send path
// writes the header from the flags and the body is empty.
func (p *SynchronizeEvent) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, p)
	return buff.Bytes()
}

// ScancodeKeyEvent is a key event carrying a PC scancode, which is what makes
// shortcuts work: a modifier is a scancode and a key, and no character expresses
// that.
type ScancodeKeyEvent struct {
	KeyboardFlags uint16 `struc:"little"`
	KeyCode       uint16 `struc:"little"`
	Pad2Octets    uint16 `struc:"little"`
}

// Serialize returns the scan code, then the keyboard flags.
//
// The flags are written in the low byte and the release flag in the high one, and the
// extended flag belongs to the scan code's prefix rather than to this field, which is
// the arrangement the fast path uses and not the one the slow path does.
func (p *ScancodeKeyEvent) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, p)
	return buff.Bytes()
}

// UnicodeKeyEvent is a key event carrying a character, which is the only way to send
// what an input method produces: a composed character has no scan code.
type UnicodeKeyEvent struct {
	KeyboardFlags uint16 `struc:"little"`
	Unicode       uint16 `struc:"little"`
	Pad2Octets    uint16 `struc:"little"`
}

// Serialize returns the release flag, then the character little endian.
//
// The flag is a full two bytes here and a single byte in a scan code event, because
// the event codes differ and the flags layout follows the event type.
func (p *UnicodeKeyEvent) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, p)
	return buff.Bytes()
}

// ClientInputEventPDU is a client's input on the slow path: a count and the events
// themselves, each with its own event time, message type and flags.
//
// A single PDU can carry events of different types, which is what the input preamble
// needs, so the count is the number of events and not the number of types.
type ClientInputEventPDU struct {
	NumEvents           uint16               `struc:"little,sizeof=SlowPathInputEvents"`
	Pad2Octets          uint16               `struc:"little"`
	SlowPathInputEvents []SlowPathInputEvent `struc:"little"`
}

// Type2 reports that this is an input message.
func (*ClientInputEventPDU) Type2() uint8 {
	return PDUTYPE2_INPUT
}

// Unpack reports that a client does not read this message.
//
// An input PDU only ever travels from a client to a server, so there is nothing to
// parse on this side; the method exists because the interface requires it.
func (*ClientInputEventPDU) Unpack(io.Reader) error {
	return nil
}

// ---------------------------------------------------------------------------
// Pointer updates.
//
// Slow path: Pointer Update PDU (PDUTYPE2_POINTER, MS-RDPBCGR 2.2.9.1.1.4).
// Fast path: FASTPATH_UPDATETYPE_PTR_POSITION / COLOR / CACHED / POINTER /
// LARGE_POINTER (MS-RDPBCGR 2.2.9.1.2.1).

// TS_PTRMSGTYPE_* values carried by the slow-path Pointer Update PDU.
const (
	TS_PTRMSGTYPE_SYSTEM   = 0x0001
	TS_PTRMSGTYPE_POSITION = 0x0003
	TS_PTRMSGTYPE_COLOR    = 0x0006
	TS_PTRMSGTYPE_CACHED   = 0x0007
	TS_PTRMSGTYPE_POINTER  = 0x0008
)

// PointerDataPDU is the slow-path Pointer Update PDU. It carries either a new
// pointer position or a pointer shape.
type PointerDataPDU struct {
	MessageType uint16
	XPos        uint16
	YPos        uint16
	CacheIndex  uint16
	HotSpotX    uint16
	HotSpotY    uint16
	Width       uint16
	Height      uint16
	Data        []byte // XOR mask, bottom-up BGRA
	Mask        []byte // AND mask, 1bpp

	// SystemType is the systemPointerType of a TS_PTRMSGTYPE_SYSTEM update: a
	// built in cursor such as the arrow or the hourglass. It is zero for the
	// other message types, and SYSPTR_NULL is itself a meaningful zero, so the
	// message type is what says whether it applies.
	SystemType uint32

	// XorBpp is the bytes per pixel of Data, which is how a caller knows
	// whether this is a 24bpp or a 32bpp colour pointer. It is one for a
	// monochrome pointer, whose Data is then one bit per pixel rather than one
	// byte.
	XorBpp int

	// LengthAndMask and LengthXorMask are the sizes the server declared for the
	// two masks. They are checked against the sizes the width and height imply
	// rather than used to read, since they come after the data they describe.
	LengthAndMask uint16
	LengthXorMask uint16
}

// Type2 reports that this is a pointer update.
func (*PointerDataPDU) Type2() uint8 { return PDUTYPE2_POINTER }

// Unpack reads a pointer update: the shape, the position, the cached shape reference
// or the system pointer type, depending on the update type in the message.
func (d *PointerDataPDU) Unpack(r io.Reader) error {
	var err error
	if d.MessageType, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	switch d.MessageType {
	case TS_PTRMSGTYPE_POSITION:
		if d.XPos, err = core.ReadUint16LE(r); err != nil {
			return err
		}
		d.YPos, err = core.ReadUint16LE(r)
		return err
	case TS_PTRMSGTYPE_SYSTEM:
		// systemPointerType is four bytes. Reading two here, as a pad, is
		// how this was written before: it took the low half of the value as
		// padding and never saw the type, so no caller could tell an arrow
		// from an hourglass, and the two bytes that were left belonged to
		// whoever read the stream next.
		d.SystemType, err = core.ReadUInt32LE(r)
		return err
	case TS_PTRMSGTYPE_CACHED:
		d.CacheIndex, err = core.ReadUint16LE(r)
		return err
	case TS_PTRMSGTYPE_COLOR:
		return d.unpackShape(r, 3)
	case TS_PTRMSGTYPE_POINTER:
		// TS_POINTERATTRIBUTE is the two colour shape: a bit depth and then a
		// bitmap with a header of its own. Reading it as a colour shape,
		// which is what this did before, took the bitmap header for pixels
		// and left the reader in the middle of the next field.
		return d.unpackMonochromePointer(r)
	}
	return nil
}

// unpackMonochromePointer reads a TS_POINTERATTRIBUTE, the classic two colour
// shape: a bit depth, a one bit per pixel bitmap of the colour, and an AND mask.
//
// The bitmap is stepped over using its own declared length, which is what keeps
// the stream framed, but the rows are indexed at the size a pointer's mask uses
// rather than at the bitmap's, because those are not always the same: a DIB is
// aligned to four bytes and a pointer mask to two. The declared length is what
// decides how far to advance either way.
func (d *PointerDataPDU) unpackMonochromePointer(r io.Reader) error {
	// xorBpp is on the wire but is always one for this shape, and the masks
	// are sized from the width instead, so it is read and discarded only to
	// stay in step.
	if _, err := core.ReadUint16LE(r); err != nil {
		return err
	}
	var head [18]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return err
	}
	bitmapLength := int(head[16]) | int(head[17])<<8
	if bitmapLength < 0 || bitmapLength > maxPointerMaskBytes {
		return fmt.Errorf("pdu: monochrome pointer bitmap of %d bytes is out of range", bitmapLength)
	}
	xor, err := core.ReadBytes(bitmapLength, r)
	if err != nil {
		return err
	}
	w, h := int(d.Width), int(d.Height)
	andRow := AndMaskRowBytes(w)
	if andRow <= 0 || andRow*h > maxPointerMaskBytes {
		return fmt.Errorf("pdu: monochrome pointer %dx%d is out of range", w, h)
	}
	if d.Mask, err = core.ReadBytes(andRow*h, r); err != nil {
		return err
	}
	if d.LengthAndMask, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.LengthXorMask, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	// The colour mask is one bit per pixel here, so its rows are the same size
	// as the AND mask's, which is not what a bytes-per-pixel row size would be.
	xorRow := PointerXorRowBytes(w, 1)
	if xorRow <= 0 || xorRow*h > len(xor) {
		return fmt.Errorf("pdu: monochrome pointer %dx%d holds %d colour bytes, needs %d", w, h, len(xor), xorRow*h)
	}
	d.XorBpp = 1
	d.Data = xor[:xorRow*h]
	return nil
}

// unpackShape reads a TS_COLORPOINTERATTRIBUTE-style shape with the given
// bytes-per-pixel for the XOR mask.
func (d *PointerDataPDU) unpackShape(r io.Reader, bpp int) error {
	var err error
	if d.CacheIndex, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.HotSpotX, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.HotSpotY, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.Width, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.Height, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.Width == 0 || d.Height == 0 {
		return nil
	}
	w, h := int(d.Width), int(d.Height)
	// The scan lines are padded, so the mask is not width*height*bpp. See
	// XorMaskRowBytes for why that matters.
	xorRow, andRow := XorMaskRowBytes(w, bpp), AndMaskRowBytes(w)
	if xorRow <= 0 || andRow <= 0 || xorRow*h > maxPointerMaskBytes || andRow*h > maxPointerMaskBytes {
		return fmt.Errorf("pdu: pointer %dx%d at %d bytes per pixel is too large to keep", w, h, bpp)
	}
	d.XorBpp = bpp
	if d.Data, err = core.ReadBytes(xorRow*h, r); err != nil {
		return err
	}
	if d.Mask, err = core.ReadBytes(andRow*h, r); err != nil {
		return err
	}
	// The two lengths follow the masks, and the masks are sized from the
	// width and height rather than from them, so they can only be used as a
	// check. They are checked anyway, because a disagreement means one of the
	// two is wrong and reading on regardless would carry the mistake into
	// whatever follows.
	if d.LengthAndMask, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if d.LengthXorMask, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if int(d.LengthAndMask) != andRow*h || int(d.LengthXorMask) != xorRow*h {
		return fmt.Errorf("pdu: pointer %dx%d at %d bytes per pixel declares %d/%d mask bytes, expected %d/%d",
			w, h, bpp, d.LengthAndMask, d.LengthXorMask, andRow*h, xorRow*h)
	}
	return nil
}

// FastPathPointerPositionPDU is FASTPATH_UPDATETYPE_PTR_POSITION
// (TS_FP_POINTERPOSATTRIBUTE). It is how the server reports pointer motion.
type FastPathPointerPositionPDU struct {
	XPos uint16
	YPos uint16
}

// FastPathUpdateType reports that this is a pointer position update.
func (*FastPathPointerPositionPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_PTR_POSITION
}

// Unpack reads the pointer position, which is the whole update: the fast path has a
// separate update type for a position, so nothing else follows it.
func (f *FastPathPointerPositionPDU) Unpack(r io.Reader) error {
	var err error
	if f.XPos, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	f.YPos, err = core.ReadUint16LE(r)
	return err
}

// FastPathPointerPDU covers the fast-path pointer shape updates
// (COLOR, CACHED, POINTER, LARGE_POINTER).
type FastPathPointerPDU struct {
	UpdateType uint8
	Bpp        uint16
	CacheIndex uint16
	HotSpotX   uint16
	HotSpotY   uint16
	Width      uint16
	Height     uint16
	Data       []byte
	Mask       []byte
}

// FastPathUpdateType reports the update type this PDU was built with, since the
// pointer updates share one structure and are told apart by it.
func (f *FastPathPointerPDU) FastPathUpdateType() uint8 { return f.UpdateType }

// Unpack reads a pointer update, dispatching on the update type: a null or default
// pointer has no body, a colour pointer is an update structure, and a cached one is a
// cache index.
//
// The shape itself is read by unpackShape, which needs the colour depth from the
// update, and that is why the two are separate rather than one method.
func (f *FastPathPointerPDU) Unpack(r io.Reader) error {
	var err error
	switch f.UpdateType {
	case FASTPATH_UPDATETYPE_CACHED:
		f.CacheIndex, err = core.ReadUint16LE(r)
		return err
	case FASTPATH_UPDATETYPE_POINTER:
		if f.Bpp, err = core.ReadUint16LE(r); err != nil {
			return err
		}
		return f.unpackShape(r, int(f.Bpp)/8)
	case FASTPATH_UPDATETYPE_LARGE_POINTER:
		if f.Bpp, err = core.ReadUint16LE(r); err != nil {
			return err
		}
		return f.unpackShape(r, int(f.Bpp)/8)
	default: // FASTPATH_UPDATETYPE_COLOR is always 24bpp
		return f.unpackShape(r, 3)
	}
}

func (f *FastPathPointerPDU) unpackShape(r io.Reader, bpp int) error {
	var err error
	if f.CacheIndex, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if f.HotSpotX, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if f.HotSpotY, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if f.Width, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if f.Height, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if f.Width == 0 || f.Height == 0 || bpp <= 0 {
		return nil
	}
	w, h := int(f.Width), int(f.Height)
	// The scan lines are padded, the same as on the slow path. This sized the
	// mask as width*height*bpp, which reads every row after the first from one
	// byte too early whenever the width is odd.
	xorRow, andRow := XorMaskRowBytes(w, bpp), AndMaskRowBytes(w)
	if xorRow <= 0 || andRow <= 0 || xorRow*h > maxPointerMaskBytes || andRow*h > maxPointerMaskBytes {
		return fmt.Errorf("pdu: fast-path pointer %dx%d at %d bytes per pixel is too large to keep", w, h, bpp)
	}
	// Bpp is the bit depth, and a colour update never states it, so it is
	// filled in here for the caller to read either way.
	f.Bpp = uint16(bpp * 8)
	if f.Data, err = core.ReadBytes(xorRow*h, r); err != nil {
		return err
	}
	f.Mask, err = core.ReadBytes(andRow*h, r)
	return err
}

// FastPathNoopPDU is used for fast-path update types that carry no payload
// (palette, synchronize, null/default pointer) so that parsing can continue
// past them instead of aborting the whole fast-path PDU.
type FastPathNoopPDU struct {
	Type uint8
}

// FastPathUpdateType reports the type this PDU was built with, which for a no-op is
// null or default pointer.
func (f *FastPathNoopPDU) FastPathUpdateType() uint8 { return f.Type }

// Unpack does nothing and reports no error, which is correct: the null and default
// pointer updates are announcements with no body, and there is nothing to read.
func (f *FastPathNoopPDU) Unpack(io.Reader) error { return nil }
