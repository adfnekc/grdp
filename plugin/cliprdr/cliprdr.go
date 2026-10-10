package cliprdr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/lunixbochs/struc"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/plugin"
)

/**
 *                                    Initialization Sequence\n
 *     Client                                                                    Server\n
 *        |                                                                         |\n
 *        |<----------------------Server Clipboard Capabilities PDU-----------------|\n
 *        |<-----------------------------Monitor Ready PDU--------------------------|\n
 *        |-----------------------Client Clipboard Capabilities PDU---------------->|\n
 *        |---------------------------Temporary Directory PDU---------------------->|\n
 *        |-------------------------------Format List PDU-------------------------->|\n
 *        |<--------------------------Format List Response PDU----------------------|\n
 *
 */

/**
 *                                    Data Transfer Sequences\n
 *     Shared                                                                     Local\n
 *  Clipboard Owner                                                           Clipboard Owner\n
 *        |                                                                         |\n
 *        |-------------------------------------------------------------------------|\n _
 *        |-------------------------------Format List PDU-------------------------->|\n  |
 *        |<--------------------------Format List Response PDU----------------------|\n _| Copy
 * Sequence
 *        |<---------------------Lock Clipboard Data PDU (Optional)-----------------|\n
 *        |-------------------------------------------------------------------------|\n
 *        |-------------------------------------------------------------------------|\n _
 *        |<--------------------------Format Data Request PDU-----------------------|\n  | Paste
 * Sequence Palette,
 *        |---------------------------Format Data Response PDU--------------------->|\n _| Metafile,
 * File List Data
 *        |-------------------------------------------------------------------------|\n
 *        |-------------------------------------------------------------------------|\n _
 *        |<------------------------Format Contents Request PDU---------------------|\n  | Paste
 * Sequence
 *        |-------------------------Format Contents Response PDU------------------->|\n _| File
 * Stream Data
 *        |<---------------------Lock Clipboard Data PDU (Optional)-----------------|\n
 *        |-------------------------------------------------------------------------|\n
 *
 */

// ChannelName and ChannelOption identify the cliprdr static virtual channel.
const (
	ChannelName   = plugin.CLIPRDR_SVC_CHANNEL_NAME
	ChannelOption = plugin.CHANNEL_OPTION_INITIALIZED | plugin.CHANNEL_OPTION_ENCRYPT_RDP |
		plugin.CHANNEL_OPTION_COMPRESS_RDP | plugin.CHANNEL_OPTION_SHOW_PROTOCOL
)

// clipboardRequestDelay is how long to wait before re-asking for announced
// text. A server can announce a format list slightly before it is able to
// serve the data, and it silently drops a request made in that window. It is a
// variable so tests can shorten it.
var clipboardRequestDelay = 600 * time.Millisecond

// MsgType is a cliprdr PDU message type (MS-RDPECLIP 2.2.1).
type MsgType uint16

// Cliprdr message types (MS-RDPECLIP 2.2.1).
const (
	CB_MONITOR_READY         = 0x0001
	CB_FORMAT_LIST           = 0x0002
	CB_FORMAT_LIST_RESPONSE  = 0x0003
	CB_FORMAT_DATA_REQUEST   = 0x0004
	CB_FORMAT_DATA_RESPONSE  = 0x0005
	CB_TEMP_DIRECTORY        = 0x0006
	CB_CLIP_CAPS             = 0x0007
	CB_FILECONTENTS_REQUEST  = 0x0008
	CB_FILECONTENTS_RESPONSE = 0x0009
	CB_LOCK_CLIPDATA         = 0x000A
	CB_UNLOCK_CLIPDATA       = 0x000B
)

// MsgFlags are the MsgFlags field of a cliprdr PDU header.
type MsgFlags uint16

// Cliprdr message flags (MS-RDPECLIP 2.2.1).
const (
	CB_RESPONSE_OK   = 0x0001
	CB_RESPONSE_FAIL = 0x0002
	CB_ASCII_NAMES   = 0x0004
)

// DwFlags is the dwFlags field of a CLIPRDR_FILE_CONTENTS_REQUEST.
type DwFlags uint32

// File contents request flags (MS-RDPECLIP 2.2.5.3).
const (
	FILECONTENTS_SIZE  = 0x00000001
	FILECONTENTS_RANGE = 0x00000002
)

// CliprdrPDUHeader is the common 8 byte header of every cliprdr PDU.
type CliprdrPDUHeader struct {
	MsgType  uint16 `struc:"little"`
	MsgFlags uint16 `struc:"little"`
	DataLen  uint32 `struc:"little"`
}

// NewCliprdrPDUHeader builds a cliprdr PDU header.
func NewCliprdrPDUHeader(mType, flags uint16, ln uint32) *CliprdrPDUHeader {
	return &CliprdrPDUHeader{
		MsgType:  mType,
		MsgFlags: flags,
		DataLen:  ln,
	}
}
func (h *CliprdrPDUHeader) serialize() []byte {
	b := &bytes.Buffer{}
	core.WriteUInt16LE(h.MsgType, b)
	core.WriteUInt16LE(h.MsgFlags, b)
	core.WriteUInt32LE(h.DataLen, b)
	return b.Bytes()
}

// CliprdrGeneralCapabilitySet is one general capability set (MS-RDPECLIP 2.2.2.1).
type CliprdrGeneralCapabilitySet struct {
	CapabilitySetType   uint16 `struc:"little"`
	CapabilitySetLength uint16 `struc:"little"`
	Version             uint32 `struc:"little"`
	GeneralFlags        uint32 `struc:"little"`
}

// CB_CAPSTYPE_GENERAL identifies the general capability set.
const (
	CB_CAPSTYPE_GENERAL = 0x0001
)

// CliprdrCapabilitySets is one capability set of a CLIPRDR_CAPS PDU.
type CliprdrCapabilitySets struct {
	CapabilitySetType uint16 `struc:"little"`
	LengthCapability  uint16 `struc:"little"`
	Version           uint32 `struc:"little"`
	GeneralFlags      uint32 `struc:"little"`
	//CapabilityData    []byte `struc:"little"`
}

// CliprdrCapabilitiesPDU is a Client/Server Clipboard Capabilities PDU
// (MS-RDPECLIP 2.2.2).
type CliprdrCapabilitiesPDU struct {
	CCapabilitiesSets uint16                        `struc:"little,sizeof=CapabilitySets"`
	Pad1              uint16                        `struc:"little"`
	CapabilitySets    []CliprdrGeneralCapabilitySet `struc:"little"`
}

// CliprdrMonitorReady is the Monitor Ready PDU; it carries no payload.
type CliprdrMonitorReady struct {
}

// GeneralFlags is the generalFlags field of a CLIPRDR_GENERAL_CAPABILITY.
type GeneralFlags uint32

// General capability flags (MS-RDPECLIP 2.2.2.1).
const (
	CB_USE_LONG_FORMAT_NAMES     = 0x00000002
	CB_STREAM_FILECLIP_ENABLED   = 0x00000004
	CB_FILECLIP_NO_FILE_PATHS    = 0x00000008
	CB_CAN_LOCK_CLIPDATA         = 0x00000010
	CB_HUGE_FILE_SUPPORT_ENABLED = 0x00000020
)

// General capability protocol versions (MS-RDPECLIP 2.2.2.1).
const (
	CB_CAPS_VERSION_1 = 0x00000001
	CB_CAPS_VERSION_2 = 0x00000002
)

// CB_CAPSTYPE_GENERAL_LEN is the fixed length of a general capability set.
const (
	CB_CAPSTYPE_GENERAL_LEN = 12
)

// FILEDESCRIPTORW.dwFlags bits (MS-RDPECLIP 2.2.5.2.3.1).
const (
	FD_CLSID      = 0x00000001
	FD_SIZEPOINT  = 0x00000002
	FD_ATTRIBUTES = 0x00000004
	FD_CREATETIME = 0x00000008
	FD_ACCESSTIME = 0x00000010
	FD_WRITESTIME = 0x00000020
	FD_FILESIZE   = 0x00000040
	FD_PROGRESSUI = 0x00004000
	FD_LINKUI     = 0x00008000
)

// fileDescriptorNameBytes is the size of a FILEDESCRIPTORW.cFileName field: a
// WCHAR[260] array (MS-RDPECLIP 2.2.5.2.3.1, winpr/include/winpr/shell.h).
const fileDescriptorNameBytes = 260 * 2

// fileDescriptorSize is the wire size of one FILEDESCRIPTORW: flags, clsid,
// sizel, pointl, file attributes, three FILETIMEs, the two size halves and the
// fixed size file name.
const fileDescriptorSize = 4 + 16 + 8 + 8 + 4 + 8 + 8 + 8 + 4 + 4 + fileDescriptorNameBytes

// FileGroupDescriptor is a FILEGROUPDESCRIPTORW: the item count followed by
// that many FILEDESCRIPTORW entries.
type FileGroupDescriptor struct {
	CItems uint32
	Fgd    []FileDescriptor
}

// FileDescriptor is a FILEDESCRIPTORW. FileName holds up to
// fileDescriptorNameBytes of UTF-16LE; it is not NUL terminated in this
// representation, the fixed array on the wire is.
type FileDescriptor struct {
	Flags          uint32
	Clsid          [16]byte
	Sizel          [8]byte
	Pointl         [8]byte
	FileAttributes uint32
	CreationTime   [8]byte
	LastAccessTime [8]byte
	LastWriteTime  []byte
	FileSizeHigh   uint32
	FileSizeLow    uint32
	FileName       []byte
}

// Unpack parses a FILEGROUPDESCRIPTORW. The item count is a wire value, so it
// is validated against the buffer before anything is allocated; a list that
// does not fit returns an error rather than a partially filled manifest.
func (f *FileGroupDescriptor) Unpack(b []byte) error {
	if len(b) < 4 {
		return fmt.Errorf("cliprdr: FILEGROUPDESCRIPTORW is %d bytes, need at least 4 for its item count", len(b))
	}
	count := binary.LittleEndian.Uint32(b[:4])
	if uint64(count)*fileDescriptorSize+4 > uint64(len(b)) {
		return fmt.Errorf("cliprdr: FILEGROUPDESCRIPTORW claims %d descriptors, only %d bytes available", count, len(b))
	}
	r := bytes.NewReader(b[4:])
	f.Fgd = make([]FileDescriptor, count)
	for i := range f.Fgd {
		if err := f.Fgd[i].unpack(r); err != nil {
			return err
		}
	}
	f.CItems = count
	return nil
}

// unpack reads one FILEDESCRIPTORW from r.
func (f *FileDescriptor) unpack(r *bytes.Reader) error {
	if r.Len() < fileDescriptorSize {
		return fmt.Errorf("cliprdr: FILEDESCRIPTORW truncated: %d bytes left, need %d", r.Len(), fileDescriptorSize)
	}
	var buf [fileDescriptorSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return fmt.Errorf("cliprdr: reading FILEDESCRIPTORW: %w", err)
	}
	f.Flags = binary.LittleEndian.Uint32(buf[0:4])
	copy(f.Clsid[:], buf[4:20])
	copy(f.Sizel[:], buf[20:28])
	copy(f.Pointl[:], buf[28:36])
	f.FileAttributes = binary.LittleEndian.Uint32(buf[36:40])
	copy(f.CreationTime[:], buf[40:48])
	copy(f.LastAccessTime[:], buf[48:56])
	f.LastWriteTime = append(f.LastWriteTime[:0], buf[56:64]...)
	f.FileSizeHigh = binary.LittleEndian.Uint32(buf[64:68])
	f.FileSizeLow = binary.LittleEndian.Uint32(buf[68:72])
	f.FileName = append(f.FileName[:0], buf[72:fileDescriptorSize]...)
	return nil
}

// serialize writes one FILEDESCRIPTORW. It always produces fileDescriptorSize
// bytes, including the fixed size NUL padded file name array.
func (f *FileDescriptor) serialize() []byte {
	b := make([]byte, 0, fileDescriptorSize)
	var u32 [4]byte
	put := func(v uint32) {
		binary.LittleEndian.PutUint32(u32[:], v)
		b = append(b, u32[:]...)
	}
	put(f.Flags)
	b = append(b, f.Clsid[:]...)
	b = append(b, f.Sizel[:]...)
	b = append(b, f.Pointl[:]...)
	put(f.FileAttributes)
	b = append(b, f.CreationTime[:]...)
	b = append(b, f.LastAccessTime[:]...)
	var lastWrite [8]byte
	copy(lastWrite[:], f.LastWriteTime)
	b = append(b, lastWrite[:]...)
	put(f.FileSizeHigh)
	put(f.FileSizeLow)
	var name [fileDescriptorNameBytes]byte
	copy(name[:], f.FileName)
	b = append(b, name[:]...)
	return b
}

func (f *FileDescriptor) isDir() bool {
	if f.Flags&FD_ATTRIBUTES != 0 {
		return f.FileAttributes&FILE_ATTRIBUTE_DIRECTORY != 0
	}

	return false
}

func (f *FileDescriptor) hasFileSize() bool {
	return f.Flags&FD_FILESIZE != 0
}

// filetimeFromTime converts a time to the 100-nanosecond FILETIME the wire
// format carries: intervals since 1601-01-01 UTC.
func filetimeFromTime(t time.Time) []byte {
	b := make([]byte, 8)
	if t.Unix() > 0 {
		const filetimeUnixEpoch = 116444736000000000 // 100ns intervals from 1601 to 1970
		binary.LittleEndian.PutUint64(b, uint64(t.UnixNano()/100)+filetimeUnixEpoch)
	}
	return b
}

// decodeFileName decodes a FILEDESCRIPTORW.cFileName, stopping at the NUL
// terminator that pads the fixed size array.
func decodeFileName(b []byte) string {
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			return core.UnicodeDecode(b[:i])
		}
	}
	return core.UnicodeDecode(b)
}

// newFileDescriptor builds the descriptor for one manifest entry. A name that
// does not fit the fixed size wire field is an error, never a silent truncation.
func newFileDescriptor(name string, size int64, modTime time.Time, isDir bool) (FileDescriptor, error) {
	d := FileDescriptor{
		Flags:         FD_ATTRIBUTES | FD_FILESIZE | FD_WRITESTIME | FD_PROGRESSUI,
		LastWriteTime: filetimeFromTime(modTime),
	}
	if isDir {
		d.FileAttributes = FILE_ATTRIBUTE_DIRECTORY
	} else {
		if size < 0 {
			size = 0
		}
		d.FileAttributes = FILE_ATTRIBUTE_NORMAL
		d.FileSizeHigh = uint32(uint64(size) >> 32)
		d.FileSizeLow = uint32(uint64(size) & 0xffffffff)
	}
	enc := core.UnicodeEncode(name)
	if len(enc)+2 > fileDescriptorNameBytes {
		return d, fmt.Errorf("cliprdr: file name %q encodes to %d bytes, the descriptor holds at most %d", name, len(enc), fileDescriptorNameBytes-2)
	}
	d.FileName = enc
	return d, nil
}

// buildFileDescriptors turns a provider manifest into descriptors.
func buildFileDescriptors(p FileProvider) ([]FileDescriptor, error) {
	names := p.Names()
	out := make([]FileDescriptor, 0, len(names))
	for _, name := range names {
		size, modTime, isDir := p.Info(name)
		d, err := newFileDescriptor(name, size, modTime, isDir)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// CliprdrTempDirectory is the Temporary Directory PDU payload: the server's
// temporary directory name as a fixed size UTF-16 buffer.
type CliprdrTempDirectory struct {
	SzTempDir []byte `struc:"[260]byte"`
}

// CliprdrFormat is one entry of a format list: either a predefined format id
// with an empty name, or a registered name with its id.
type CliprdrFormat struct {
	FormatId   uint32
	FormatName string
}

// CliprdrFormatList is a parsed format list.
type CliprdrFormatList struct {
	NumFormats uint32
	Formats    []CliprdrFormat
}

// ClipboardFormats holds the ids of the registered clipboard formats shared by
// the non-Windows platforms.
type ClipboardFormats uint16

// Registered clipboard format ids shared across platforms (MS-RDPECLIP 2.2.4).
const (
	CB_FORMAT_HTML             = 0xD010
	CB_FORMAT_PNG              = 0xD011
	CB_FORMAT_JPEG             = 0xD012
	CB_FORMAT_GIF              = 0xD013
	CB_FORMAT_TEXTURILIST      = 0xD014
	CB_FORMAT_GNOMECOPIEDFILES = 0xD015
	CB_FORMAT_MATECOPIEDFILES  = 0xD016
)

// CliprdrCtrlClipboardData is the payload of a Lock or Unlock Clipboard Data
// PDU.
type CliprdrCtrlClipboardData struct {
	ClipDataId uint32
}

// CliprdrFormatDataRequest is a Format Data Request PDU: the id of the format
// the peer wants.
type CliprdrFormatDataRequest struct {
	RequestedFormatId uint32
}

// CliprdrFormatDataResponse is a Format Data Response PDU: the bytes of the
// requested format.
type CliprdrFormatDataResponse struct {
	RequestedFormatData []byte
}

// CliprdrFileContentsRequest is a File Contents Request PDU
// (MS-RDPECLIP 2.2.5.3). ClipDataId is present only when the peer advertised
// CB_CAN_LOCK_CLIPDATA.
type CliprdrFileContentsRequest struct {
	StreamId      uint32 `struc:"little"`
	Lindex        uint32 `struc:"little"`
	DwFlags       uint32 `struc:"little"`
	NPositionLow  uint32 `struc:"little"`
	NPositionHigh uint32 `struc:"little"`
	CbRequested   uint32 `struc:"little"`
	ClipDataId    uint32 `struc:"little"`
}

// FileContentsSizeRequest builds a FILECONTENTS_SIZE request for the file at
// index i.
func FileContentsSizeRequest(i uint32) *CliprdrFileContentsRequest {
	return &CliprdrFileContentsRequest{
		StreamId:      1,
		Lindex:        i,
		DwFlags:       FILECONTENTS_SIZE,
		NPositionLow:  0,
		NPositionHigh: 0,
		CbRequested:   65535,
		ClipDataId:    0,
	}
}

// CliprdrFileContentsResponse is a File Contents Response PDU: the stream id
// echoed back followed by the requested file data.
type CliprdrFileContentsResponse struct {
	StreamId      uint32
	CbRequested   uint32
	RequestedData []byte
}

// Unpack reads a FILECONTENTS_RESPONSE: the stream id followed by the requested
// data. The data length is implied by the PDU length, so a response shorter
// than its stream id is an error.
func (resp *CliprdrFileContentsResponse) Unpack(b []byte) error {
	if len(b) < 4 {
		return fmt.Errorf("cliprdr: FILECONTENTS_RESPONSE is %d bytes, need at least 4 for its stream id", len(b))
	}
	resp.StreamId = binary.LittleEndian.Uint32(b[:4])
	resp.RequestedData = b[4:]
	resp.CbRequested = uint32(len(resp.RequestedData))
	return nil
}

// CliprdrClient implements the cliprdr virtual channel: it advertises and
// serves the local clipboard to the server, and receives what the server
// offers. Text goes through OnText, SetClipboardText and RequestClipboardText;
// files go through FileProvider in one direction and OnRemoteFiles,
// ReadRemoteFile and ClearRemoteFiles in the other.
type CliprdrClient struct {
	w                            core.ChannelSender
	mu                           sync.Mutex
	ready                        bool
	textHandler                  func(string)
	pendingTextRequest           bool
	fileProvider                 FileProvider
	onRemoteFiles                func([]RemoteFile)
	remoteFiles                  []RemoteFile
	pendingFileDescriptorRequest bool
	textAfterFileDescriptor      bool
	readStreamID                 uint32
	pendingReads                 map[uint32]chan fileContentsResult
	useLongFormatNames           bool
	streamFileClipEnabled        bool
	fileClipNoFilePaths          bool
	canLockClipData              bool
	hasHugeFileSupport           bool
	formatIdMap                  map[uint32]uint32
	Files                        []FileDescriptor
	reply                        chan []byte
	Control
}

// FileProvider is the application's view of the files it shares with the
// server. The protocol layer never opens a path itself: it asks the provider
// for the manifest and for the bytes.
type FileProvider interface {
	// Names returns the names to publish, in the order they should appear in
	// the descriptor list.
	Names() []string
	// Info reports the size, modification time and directory flag of one named
	// entry. The name is one returned by Names. Size is ignored for a directory.
	Info(name string) (size int64, modTime time.Time, isDir bool)
	// Open returns the contents of one named entry. The caller seeks to the
	// requested offset and reads only the requested range, so a large file is
	// never read into memory by the protocol layer.
	Open(name string) (io.ReadCloser, error)
}

// RemoteFile is one entry of the manifest the server offered. Index is the
// value to pass to ReadRemoteFile; the rest is the metadata from the
// descriptor.
type RemoteFile struct {
	// Index is the entry's position in the descriptor list.
	Index uint32
	// Name is the file or directory name.
	Name string
	// Size is the file size in bytes, zero for a directory or when the server
	// did not supply one.
	Size int64
	// IsDir reports the FILE_ATTRIBUTE_DIRECTORY flag of the descriptor.
	IsDir bool
}

// fileContentsResult is the answer to one ReadRemoteFile round trip.
type fileContentsResult struct {
	data []byte
	err  error
}

// maxRemoteRangeBytes caps a single remote file read, so a range request that
// arrives with an oversized count cannot drive an unbounded allocation.
const maxRemoteRangeBytes = 16 << 20

// maxLocalRangeBytes caps how much of a locally provided file is read for one
// FILECONTENTS_RANGE response.
const maxLocalRangeBytes = 16 << 20

// remoteFileTimeout is how long ReadRemoteFile waits for the server to answer
// one range request. It is a variable so tests can shorten it.
var remoteFileTimeout = 30 * time.Second

// NewCliprdrClient returns a cliprdr client and starts the platform clipboard
// watcher goroutine.
func NewCliprdrClient() *CliprdrClient {
	c := &CliprdrClient{
		formatIdMap:  make(map[uint32]uint32, 20),
		Files:        make([]FileDescriptor, 0, 20),
		pendingReads: make(map[uint32]chan fileContentsResult),
		reply:        make(chan []byte, 100),
	}

	go ClipWatcher(c)

	return c
}

// OnText registers the handler for text the server offers. It is called on
// the channel's goroutine, so it should not block.
func (c *CliprdrClient) OnText(f func(string)) {
	c.mu.Lock()
	c.textHandler = f
	c.mu.Unlock()
}

// RequestClipboardText asks the server for the text it last announced. It is
// only needed when the text was announced before a handler was registered, or
// when a request timed out; OnText normally triggers the request itself.
func (c *CliprdrClient) RequestClipboardText() error {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if !ready {
		return fmt.Errorf("cliprdr: the channel is not ready")
	}
	c.mu.Lock()
	c.pendingTextRequest = true
	c.mu.Unlock()
	c.sendFormatDataRequest(CF_UNICODETEXT)
	return nil
}

// SetClipboardText publishes text as the local clipboard contents and tells
// the server about it. The text is also served to any later data request, so
// it stays available after the announcement.
func (c *CliprdrClient) SetClipboardText(s string) error {
	setLocalText(s)

	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if !ready {
		// The channel will advertise the current clipboard when the server
		// sends its monitor ready.
		return nil
	}
	c.sendFormatListPDU()
	return nil
}

// SetFileProvider installs the source of the files this client offers to the
// server. While a provider is set the format list advertises
// CFSTR_FILEDESCRIPTORW and CFSTR_FILECONTENTS; with no provider it does not,
// so the server is never left waiting for file data that cannot be produced.
// Passing nil clears the provider. The call re-announces the format list when
// the channel is already up.
func (c *CliprdrClient) SetFileProvider(p FileProvider) {
	c.mu.Lock()
	c.fileProvider = p
	ready := c.ready
	c.mu.Unlock()
	if ready {
		c.sendFormatListPDU()
	}
}

// localFileSource returns the provider that answers a local -> remote file
// request: the application's provider when one is set, otherwise the
// platform's own clipboard file list (only Windows has one).
func (c *CliprdrClient) localFileSource() FileProvider {
	c.mu.Lock()
	p := c.fileProvider
	c.mu.Unlock()
	if p != nil {
		return p
	}
	return platformClipboardFiles()
}

// OnRemoteFiles registers the handler for the file list the server offers.
// When it is set, the client asks the server for the descriptor list as soon
// as the server announces CFSTR_FILEDESCRIPTORW and calls the handler once
// with the parsed manifest. Nothing is fetched until ReadRemoteFile asks for a
// range. It runs on the channel's goroutine, so it should not block.
func (c *CliprdrClient) OnRemoteFiles(f func(files []RemoteFile)) {
	c.mu.Lock()
	c.onRemoteFiles = f
	c.mu.Unlock()
}

// ClearRemoteFiles drops the manifest received from the server, so a later
// ReadRemoteFile with a stale index fails instead of reading a different file.
func (c *CliprdrClient) ClearRemoteFiles() {
	c.mu.Lock()
	c.remoteFiles = nil
	c.mu.Unlock()
}

// nextReadStreamID returns a stream id for one remote read. c.mu must be held.
func (c *CliprdrClient) nextReadStreamID() uint32 {
	c.readStreamID++
	return c.readStreamID
}

// ReadRemoteFile reads the [off, off+n) range of the remote file at index and
// returns it. n <= 0 means read to the end of the file, but a single response
// is capped at maxRemoteRangeBytes; a larger remainder is an error so that a
// multi-gigabyte file is read in explicit chunks instead of one huge
// allocation. A directory entry, a negative offset, an offset past the end of
// the file, and a failed or timed out response are all errors.
func (c *CliprdrClient) ReadRemoteFile(index uint32, off int64, n int) ([]byte, error) {
	c.mu.Lock()
	if int(index) >= len(c.remoteFiles) {
		c.mu.Unlock()
		return nil, fmt.Errorf("cliprdr: remote file index %d is not in the %d-entry manifest", index, len(c.remoteFiles))
	}
	rf := c.remoteFiles[index]
	if off < 0 {
		c.mu.Unlock()
		return nil, fmt.Errorf("cliprdr: negative offset %d for remote file %q", off, rf.Name)
	}
	if off > rf.Size {
		c.mu.Unlock()
		return nil, fmt.Errorf("cliprdr: offset %d is past the end of remote file %q (%d bytes)", off, rf.Name, rf.Size)
	}
	if rf.IsDir {
		c.mu.Unlock()
		return nil, fmt.Errorf("cliprdr: remote entry %q is a directory", rf.Name)
	}
	want := int64(n)
	if n <= 0 || want > rf.Size-off {
		want = rf.Size - off
	}
	if want > maxRemoteRangeBytes {
		c.mu.Unlock()
		return nil, fmt.Errorf("cliprdr: %d bytes from %q exceeds the %d-byte single read limit; pass an explicit size", want, rf.Name, maxRemoteRangeBytes)
	}

	streamID := c.nextReadStreamID()
	ch := make(chan fileContentsResult, 1)
	c.pendingReads[streamID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pendingReads, streamID)
		c.mu.Unlock()
	}()

	c.sendFormatContentsRequest(CliprdrFileContentsRequest{
		StreamId:      streamID,
		Lindex:        index,
		DwFlags:       FILECONTENTS_RANGE,
		NPositionLow:  uint32(off & 0xffffffff),
		NPositionHigh: uint32(uint64(off) >> 32),
		CbRequested:   uint32(want),
	})

	select {
	case res := <-ch:
		return res.data, res.err
	case <-time.After(remoteFileTimeout):
		return nil, fmt.Errorf("cliprdr: timed out reading %d bytes at offset %d of remote file %q", want, off, rf.Name)
	}
}

// Send(s []byte) writes a cliprdr PDU on the static channel.
func (c *CliprdrClient) Send(s []byte) (int, error) {
	glog.Debug("len:", len(s), "data:", glog.Hex(s))
	name, _ := c.GetType()
	return c.w.SendToChannel(name, s)
}

// Sender installs the channel used to write PDUs.
func (c *CliprdrClient) Sender(f core.ChannelSender) {
	c.w = f
}

// GetType reports the channel name and options this plugin registers with.
func (c *CliprdrClient) GetType() (string, uint32) {
	return ChannelName, ChannelOption
}

// Process consumes one channel payload. It may hold several PDUs, so the
// payload is walked using each PDU's own declared length.
func (c *CliprdrClient) Process(s []byte) {
	glog.Debug("recv:", glog.Hex(s))
	r := bytes.NewReader(s)

	// A channel payload can hold several PDUs, and a server may append
	// bytes beyond the length it declared, so walk the payload using each
	// PDU's own length instead of assuming one PDU per message.
	for r.Len() >= 8 {
		msgType, _ := core.ReadUint16LE(r)
		flag, _ := core.ReadUint16LE(r)
		length, _ := core.ReadUInt32LE(r)
		if !knownMsgType(msgType) {
			// Servers can append bytes beyond the length they declared
			// (xrdp does), and those bytes do not form a PDU. Stop rather
			// than misread them and swallow a following message.
			glog.Debugf("cliprdr: stopping at unknown message type 0x%x (%d bytes left)",
				msgType, r.Len())
			return
		}
		if int(length) > r.Len() {
			glog.Debugf("cliprdr: type=0x%x declares %d bytes, %d left; dropping",
				msgType, length, r.Len())
			return
		}
		glog.Debugf("cliprdr: type=0x%x flag=%d length=%d, all=%d", msgType, flag, length, r.Len())

		b, _ := core.ReadBytes(int(length), r)
		c.dispatch(msgType, flag, b)
	}
}

// knownMsgType reports whether a message type is part of the protocol.
func knownMsgType(msgType uint16) bool {
	return msgType >= CB_MONITOR_READY && msgType <= CB_UNLOCK_CLIPDATA
}

func (c *CliprdrClient) dispatch(msgType, flag uint16, b []byte) {
	switch msgType {
	case CB_CLIP_CAPS:
		glog.Info("CB_CLIP_CAPS")
		c.processClipCaps(b)

	case CB_MONITOR_READY:
		glog.Info("CB_MONITOR_READY")
		c.processMonitorReady(b)

	case CB_FORMAT_LIST:
		glog.Info("CB_FORMAT_LIST")
		c.processFormatList(b)

	case CB_FORMAT_LIST_RESPONSE:
		glog.Info("CB_FORMAT_LIST_RESPONSE")
		c.processFormatListResponse(flag, b)

	case CB_FORMAT_DATA_REQUEST:
		glog.Info("CB_FORMAT_DATA_REQUEST")
		c.processFormatDataRequest(b)

	case CB_FORMAT_DATA_RESPONSE:
		glog.Info("CB_FORMAT_DATA_RESPONSE")
		c.processFormatDataResponse(flag, b)

	case CB_FILECONTENTS_REQUEST:
		glog.Info("CB_FILECONTENTS_REQUEST")
		c.processFileContentsRequest(b)

	case CB_FILECONTENTS_RESPONSE:
		glog.Info("CB_FILECONTENTS_RESPONSE")
		c.processFileContentsResponse(flag, b)

	case CB_LOCK_CLIPDATA:
		glog.Info("CB_LOCK_CLIPDATA")
		c.processLockClipData(b)

	case CB_UNLOCK_CLIPDATA:
		glog.Info("CB_UNLOCK_CLIPDATA")
		c.processUnlockClipData(b)

	default:
		glog.Errorf("type 0x%x not supported", msgType)
	}
}
func (c *CliprdrClient) processClipCaps(b []byte) {
	r := bytes.NewReader(b)
	var cp CliprdrCapabilitiesPDU
	err := struc.Unpack(r, &cp)
	if err != nil {
		glog.Error(err)
		return
	}
	glog.Debugf("Capabilities:%+v", cp)
	c.useLongFormatNames = cp.CapabilitySets[0].GeneralFlags&CB_USE_LONG_FORMAT_NAMES != 0
	c.streamFileClipEnabled = cp.CapabilitySets[0].GeneralFlags&CB_STREAM_FILECLIP_ENABLED != 0
	c.fileClipNoFilePaths = cp.CapabilitySets[0].GeneralFlags&CB_FILECLIP_NO_FILE_PATHS != 0
	c.canLockClipData = cp.CapabilitySets[0].GeneralFlags&CB_CAN_LOCK_CLIPDATA != 0
	c.hasHugeFileSupport = cp.CapabilitySets[0].GeneralFlags&CB_HUGE_FILE_SUPPORT_ENABLED != 0
	glog.Info("UseLongFormatNames:", c.useLongFormatNames)
	glog.Info("StreamFileClipEnabled:", c.streamFileClipEnabled)
	glog.Info("FileClipNoFilePaths:", c.fileClipNoFilePaths)
	glog.Info("CanLockClipData:", c.canLockClipData)
	glog.Info("HasHugeFileSupport:", c.hasHugeFileSupport)
}

func (c *CliprdrClient) processMonitorReady(b []byte) {
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()

	//Client Clipboard Capabilities PDU
	c.sendClientCapabilitiesPDU()

	//Temporary Directory PDU
	//c.sendTemporaryDirectoryPDU()

	//Format List PDU
	c.sendFormatListPDU()

}
func (c *CliprdrClient) processFormatList(b []byte) {
	c.withOpenClipboard(func() {
		if !EmptyClipboard() {
			glog.Error("EmptyClipboard failed")
		}
	})
	fl, hasFile := c.readForamtList(b)
	glog.Info("numFormats:", fl.NumFormats)

	if hasFile {
		c.SendCliprdrMessage()
	} else {
		c.withOpenClipboard(func() {
			if !EmptyClipboard() {
				glog.Error("EmptyClipboard failed")
			}
			for i := range c.formatIdMap {
				glog.Debug("i:", i)
				SetClipboardData(i, 0)
			}
		})

	}

	c.sendFormatListResponse(CB_RESPONSE_OK)

	// A FORMAT_DATA_RESPONSE carries no format id, so only one format data
	// request may be outstanding. When the server offers files and text and
	// the application handles both, ask for the descriptor list first and
	// defer the text request until it has been answered.
	isUnicodeText := false
	for _, f := range fl.Formats {
		if f.FormatId == CF_UNICODETEXT {
			isUnicodeText = true
			break
		}
	}

	c.mu.Lock()
	wantFiles := c.onRemoteFiles != nil
	autoText := isUnicodeText && c.textHandler != nil
	c.mu.Unlock()

	sentDescriptor := false
	if wantFiles {
		if remoteID, ok := c.formatIdMap[RegisterClipboardFormat(CFSTR_FILEDESCRIPTORW)]; ok {
			c.mu.Lock()
			c.pendingFileDescriptorRequest = true
			c.textAfterFileDescriptor = autoText
			c.mu.Unlock()
			c.sendFormatDataRequest(remoteID)
			sentDescriptor = true
		}
	}

	if autoText && !sentDescriptor {
		c.mu.Lock()
		c.pendingTextRequest = true
		c.mu.Unlock()
		go c.requestTextWithRetry()
	}
}

// requestTextWithRetry asks the server for announced text, retrying once. A
// server may announce a format before it has actually acquired the data from
// whatever owns the selection, and such a request is simply dropped; a second
// ask a moment later is what an interactive paste ends up doing too. Callers
// that want to control the timing exactly can use RequestClipboardText.
func (c *CliprdrClient) requestTextWithRetry() {
	// Snapshot the delay so the goroutine never reads a variable that could
	// change under it.
	delay := clipboardRequestDelay
	for i := 0; i < 2; i++ {
		time.Sleep(delay)
		c.mu.Lock()
		pending := c.pendingTextRequest
		c.mu.Unlock()
		if !pending {
			return
		}
		c.sendFormatDataRequest(CF_UNICODETEXT)
	}
}
func (c *CliprdrClient) processFormatListResponse(flag uint16, b []byte) {
	if flag != CB_RESPONSE_OK {
		glog.Error("Format List Response Failed")
		return
	}
	glog.Debug("Format List Response OK")
}
func (c *CliprdrClient) processFormatDataRequest(b []byte) {
	r := bytes.NewReader(b)
	requestId, err := core.ReadUInt32LE(r)
	if err != nil {
		glog.Error("cliprdr: malformed format data request: ", err)
		c.sendFormatDataResponseFlags(CB_RESPONSE_FAIL, nil)
		return
	}

	if requestId == RegisterClipboardFormat(CFSTR_FILEDESCRIPTORW) {
		provider := c.localFileSource()
		if provider == nil {
			glog.Error("cliprdr: file descriptor requested but no FileProvider is set")
			c.sendFormatDataResponseFlags(CB_RESPONSE_FAIL, nil)
			return
		}
		descs, err := buildFileDescriptors(provider)
		if err != nil {
			glog.Error("cliprdr: ", err)
			c.sendFormatDataResponseFlags(CB_RESPONSE_FAIL, nil)
			return
		}
		c.mu.Lock()
		c.Files = descs
		c.mu.Unlock()
		buff := &bytes.Buffer{}
		core.WriteUInt32LE(uint32(len(descs)), buff)
		for i := range descs {
			buff.Write(descs[i].serialize())
		}
		c.sendFormatDataResponse(buff.Bytes())
		return
	}

	buff := &bytes.Buffer{}
	c.withOpenClipboard(func() {
		data := GetClipboardData(requestId)
		glog.Debug("data:", data)
		buff.Write(core.UnicodeEncode(data))
		buff.Write([]byte{0, 0})
	})
	c.sendFormatDataResponse(buff.Bytes())
}

func (c *CliprdrClient) processFormatDataResponse(flag uint16, b []byte) {
	c.mu.Lock()
	wantedText := c.pendingTextRequest
	wantedFiles := c.pendingFileDescriptorRequest
	startText := c.textAfterFileDescriptor
	textHandler := c.textHandler
	fileHandler := c.onRemoteFiles
	c.pendingTextRequest = false
	c.pendingFileDescriptorRequest = false
	c.textAfterFileDescriptor = false
	c.mu.Unlock()

	if flag != CB_RESPONSE_OK {
		glog.Error("Format Data Response Failed")
	} else {
		if wantedText && textHandler != nil {
			// Text arrives as UTF-16LE, usually NUL terminated. Decoding the
			// terminator would put a stray NUL in the string.
			text := core.UnicodeDecode(b)
			text = strings.TrimRight(text, "\x00")
			textHandler(text)
		}

		if wantedFiles && fileHandler != nil {
			var dsc FileGroupDescriptor
			if err := dsc.Unpack(b); err != nil {
				// A manifest that cannot be read fails the batch; the handler is
				// not called with a half parsed list.
				glog.Error("cliprdr: bad FILEGROUPDESCRIPTORW: ", err)
			} else {
				files := remoteFilesFromDescriptor(&dsc)
				c.mu.Lock()
				c.remoteFiles = files
				c.mu.Unlock()
				fileHandler(files)
			}
		}
	}

	// The text request that was held back while the descriptor list was being
	// fetched can go out now.
	if startText {
		c.mu.Lock()
		c.pendingTextRequest = true
		c.mu.Unlock()
		go c.requestTextWithRetry()
	}

	select {
	case c.reply <- b:
	default:
		// The legacy reply channel is buffered; drop rather than block the
		// channel goroutine when nobody is reading it.
	}
}

// remoteFilesFromDescriptor turns a parsed FILEGROUPDESCRIPTORW into the
// manifest handed to OnRemoteFiles.
func remoteFilesFromDescriptor(dsc *FileGroupDescriptor) []RemoteFile {
	files := make([]RemoteFile, 0, len(dsc.Fgd))
	for i := range dsc.Fgd {
		d := &dsc.Fgd[i]
		size := int64(0)
		if !d.isDir() {
			size = int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow)
		}
		files = append(files, RemoteFile{
			Index: uint32(i),
			Name:  decodeFileName(d.FileName),
			Size:  size,
			IsDir: d.isDir(),
		})
	}
	return files
}

// readProviderRange opens one provider entry and returns count bytes from off.
// The count is bounded before it can drive a read.
func readProviderRange(p FileProvider, name string, off int64, count int) ([]byte, error) {
	if off < 0 {
		return nil, fmt.Errorf("cliprdr: negative range offset %d for %q", off, name)
	}
	if count <= 0 {
		return nil, nil
	}
	if count > maxLocalRangeBytes {
		count = maxLocalRangeBytes
	}
	f, err := p.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if off > 0 {
		// The provider interface only promises a reader. Seek past the offset
		// when it supports it and fall back to discarding otherwise.
		if s, ok := f.(io.Seeker); ok {
			if _, err := s.Seek(off, io.SeekStart); err != nil {
				return nil, err
			}
		} else if _, err := io.CopyN(io.Discard, f, off); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(count)))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (c *CliprdrClient) processFileContentsRequest(b []byte) {
	var req CliprdrFileContentsRequest
	if err := struc.Unpack(bytes.NewReader(b), &req); err != nil {
		glog.Error("cliprdr: malformed file contents request: ", err)
		return
	}
	provider := c.localFileSource()
	if provider == nil {
		glog.Error("cliprdr: file contents requested but no FileProvider is set")
		c.sendFormatContentsResponseFlags(req.StreamId, CB_RESPONSE_FAIL, nil)
		return
	}
	c.mu.Lock()
	if int(req.Lindex) >= len(c.Files) {
		c.mu.Unlock()
		glog.Error("cliprdr: file contents request for unknown index ", req.Lindex)
		c.sendFormatContentsResponseFlags(req.StreamId, CB_RESPONSE_FAIL, nil)
		return
	}
	d := c.Files[req.Lindex]
	c.mu.Unlock()
	name := decodeFileName(d.FileName)

	switch req.DwFlags {
	case FILECONTENTS_SIZE:
		size, _, isDir := provider.Info(name)
		if isDir || size < 0 {
			size = 0
		}
		buff := &bytes.Buffer{}
		core.WriteUInt32LE(uint32(uint64(size)&0xffffffff), buff)
		core.WriteUInt32LE(uint32(uint64(size)>>32), buff)
		c.sendFormatContentsResponse(req.StreamId, buff.Bytes())
	case FILECONTENTS_RANGE:
		off := int64(uint64(req.NPositionHigh)<<32 | uint64(req.NPositionLow))
		data, err := readProviderRange(provider, name, off, int(req.CbRequested))
		if err != nil {
			glog.Error("cliprdr: ", err)
			c.sendFormatContentsResponseFlags(req.StreamId, CB_RESPONSE_FAIL, nil)
			return
		}
		c.sendFormatContentsResponse(req.StreamId, data)
	default:
		glog.Errorf("cliprdr: unsupported file contents request flags 0x%x", req.DwFlags)
		c.sendFormatContentsResponseFlags(req.StreamId, CB_RESPONSE_FAIL, nil)
	}
}

func (c *CliprdrClient) processFileContentsResponse(flag uint16, b []byte) {
	var resp CliprdrFileContentsResponse
	if err := resp.Unpack(b); err != nil {
		glog.Error("cliprdr: malformed file contents response: ", err)
		return
	}

	c.mu.Lock()
	ch := c.pendingReads[resp.StreamId]
	c.mu.Unlock()

	// A ReadRemoteFile waiter owns this stream id.
	if ch != nil {
		if flag != CB_RESPONSE_OK {
			ch <- fileContentsResult{err: fmt.Errorf("cliprdr: server refused file contents request for stream 0x%x", resp.StreamId)}
			return
		}
		ch <- fileContentsResult{data: resp.RequestedData}
		return
	}

	// The Windows data object consumes responses from the legacy reply channel.
	if flag != CB_RESPONSE_OK {
		glog.Error("File Contents Response Failed")
		return
	}
	glog.Debug("Get File Contents Response:", resp.StreamId, resp.CbRequested)
	c.reply <- resp.RequestedData
}
func (c *CliprdrClient) processLockClipData(b []byte) {
	r := bytes.NewReader(b)
	var l CliprdrCtrlClipboardData
	l.ClipDataId, _ = core.ReadUInt32LE(r)
}
func (c *CliprdrClient) processUnlockClipData(b []byte) {
	r := bytes.NewReader(b)
	var l CliprdrCtrlClipboardData
	l.ClipDataId, _ = core.ReadUInt32LE(r)

}

func (c *CliprdrClient) sendClientCapabilitiesPDU() {
	glog.Info("Send Client Clipboard Capabilities PDU")
	var cs CliprdrGeneralCapabilitySet
	cs.CapabilitySetLength = 12
	cs.CapabilitySetType = CB_CAPSTYPE_GENERAL
	cs.Version = CB_CAPS_VERSION_2
	cs.GeneralFlags = CB_USE_LONG_FORMAT_NAMES |
		CB_STREAM_FILECLIP_ENABLED |
		CB_FILECLIP_NO_FILE_PATHS
	var cc CliprdrCapabilitiesPDU
	cc.CCapabilitiesSets = 1
	cc.Pad1 = 0
	cc.CapabilitySets = make([]CliprdrGeneralCapabilitySet, 0, 1)
	cc.CapabilitySets = append(cc.CapabilitySets, cs)
	header := NewCliprdrPDUHeader(CB_CLIP_CAPS, 0, 16)

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteUInt16LE(cc.CCapabilitiesSets, buff)
	core.WriteUInt16LE(cc.Pad1, buff)
	for _, v := range cc.CapabilitySets {
		struc.Pack(buff, v)
	}

	c.Send(buff.Bytes())
}

func (c *CliprdrClient) sendTemporaryDirectoryPDU() {
	glog.Info("Send Temporary Directory PDU")
	var t CliprdrTempDirectory
	header := &CliprdrPDUHeader{CB_TEMP_DIRECTORY, 0, 260}
	t.SzTempDir = core.UnicodeEncode(os.TempDir())

	buff := &bytes.Buffer{}
	core.WriteBytes(header.serialize(), buff)
	core.WriteBytes(t.SzTempDir, buff)
	c.Send(buff.Bytes())
}
func (c *CliprdrClient) sendFormatListPDU() {
	glog.Info("Send Format List PDU")
	var f CliprdrFormatList

	f.Formats = c.localFormatList()
	f.NumFormats = uint32(len(f.Formats))

	glog.Info("NumFormats:", f.NumFormats)
	glog.Debug("Formats:", f.Formats)

	b := &bytes.Buffer{}
	for _, v := range f.Formats {
		core.WriteUInt32LE(v.FormatId, b)
		if v.FormatName == "" {
			core.WriteUInt16LE(0, b)
		} else {
			n := core.UnicodeEncode(v.FormatName)
			core.WriteBytes(n, b)
			b.Write([]byte{0, 0})
		}
	}

	header := NewCliprdrPDUHeader(CB_FORMAT_LIST, 0, uint32(b.Len()))

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteBytes(b.Bytes(), buff)

	c.Send(buff.Bytes())
}
func (c *CliprdrClient) readForamtList(b []byte) (*CliprdrFormatList, bool) {
	r := bytes.NewReader(b)
	fs := make([]CliprdrFormat, 0, 20)
	var numFormats uint32 = 0
	hasFile := false
	c.formatIdMap = make(map[uint32]uint32, 0)
	for r.Len() > 0 {
		foramtId, _ := core.ReadUInt32LE(r)
		bs := make([]uint16, 0, 20)
		ln := r.Len()
		for j := 0; j < ln; j++ {
			b, _ := core.ReadUint16LE(r)
			if b == 0 {
				break
			}
			bs = append(bs, b)
		}
		name := string(utf16.Decode(bs))
		if strings.EqualFold(name, CFSTR_FILEDESCRIPTORW) {
			hasFile = true
		}
		glog.Infof("Foramt:%d Name:<%s>", foramtId, name)
		if name != "" {
			localId := RegisterClipboardFormat(name)
			glog.Info("local:", localId, "remote:", foramtId)
			c.formatIdMap[localId] = foramtId
		} else {
			c.formatIdMap[foramtId] = foramtId
		}

		numFormats++
		fs = append(fs, CliprdrFormat{foramtId, name})
	}

	return &CliprdrFormatList{numFormats, fs}, hasFile
}

func (c *CliprdrClient) sendFormatListResponse(flags uint16) {
	glog.Info("Send Format List Response")
	header := NewCliprdrPDUHeader(CB_FORMAT_LIST_RESPONSE, flags, 0)
	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	c.Send(buff.Bytes())
}

func (c *CliprdrClient) sendFormatDataRequest(id uint32) {
	glog.Info("Send Format Data Request")
	var r CliprdrFormatDataRequest
	r.RequestedFormatId = id
	header := NewCliprdrPDUHeader(CB_FORMAT_DATA_REQUEST, 0, 4)

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteUInt32LE(r.RequestedFormatId, buff)

	c.Send(buff.Bytes())
}
func (c *CliprdrClient) sendFormatDataResponse(b []byte) {
	c.sendFormatDataResponseFlags(CB_RESPONSE_OK, b)
}

// sendFormatDataResponseFlags writes a FORMAT_DATA_RESPONSE with the given
// status flags. A failure response carries no data.
func (c *CliprdrClient) sendFormatDataResponseFlags(flags uint16, b []byte) {
	glog.Info("Send Format Data Response")
	var resp CliprdrFormatDataResponse
	resp.RequestedFormatData = b

	header := NewCliprdrPDUHeader(CB_FORMAT_DATA_RESPONSE, flags, uint32(len(resp.RequestedFormatData)))

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	buff.Write(resp.RequestedFormatData)

	c.Send(buff.Bytes())
}

func (c *CliprdrClient) sendFormatContentsRequest(r CliprdrFileContentsRequest) uint32 {
	glog.Info("Send Format Contents Request")
	glog.Debugf("Format Contents Request:%+v", r)
	header := NewCliprdrPDUHeader(CB_FILECONTENTS_REQUEST, 0, 28)

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteUInt32LE(r.StreamId, buff)
	core.WriteUInt32LE(uint32(r.Lindex), buff)
	core.WriteUInt32LE(r.DwFlags, buff)
	core.WriteUInt32LE(r.NPositionLow, buff)
	core.WriteUInt32LE(r.NPositionHigh, buff)
	core.WriteUInt32LE(r.CbRequested, buff)
	core.WriteUInt32LE(r.ClipDataId, buff)

	c.Send(buff.Bytes())

	return uint32(buff.Len())
}
func (c *CliprdrClient) sendFormatContentsResponse(streamId uint32, b []byte) {
	c.sendFormatContentsResponseFlags(streamId, CB_RESPONSE_OK, b)
}

// sendFormatContentsResponseFlags writes a FILECONTENTS_RESPONSE with the given
// status flags. A failure response still carries the stream id so the requester
// can match it, but no file data.
func (c *CliprdrClient) sendFormatContentsResponseFlags(streamId uint32, flags uint16, b []byte) {
	glog.Info("Send Format Contents Response")
	var r CliprdrFileContentsResponse
	r.StreamId = streamId
	r.RequestedData = b
	r.CbRequested = uint32(len(b))
	header := NewCliprdrPDUHeader(CB_FILECONTENTS_RESPONSE, flags, uint32(4+r.CbRequested))

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteUInt32LE(r.StreamId, buff)
	core.WriteBytes(r.RequestedData, buff)

	c.Send(buff.Bytes())
}

func (c *CliprdrClient) sendLockClipData() {
	glog.Info("Send Lock Clip Data")
	var r CliprdrCtrlClipboardData
	header := NewCliprdrPDUHeader(CB_LOCK_CLIPDATA, 0, 4)

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteUInt32LE(r.ClipDataId, buff)

	c.Send(buff.Bytes())
}

func (c *CliprdrClient) sendUnlockClipData() {
	glog.Info("Send Unlock Clip Data")
	var r CliprdrCtrlClipboardData
	header := NewCliprdrPDUHeader(CB_UNLOCK_CLIPDATA, 0, 4)

	buff := &bytes.Buffer{}
	buff.Write(header.serialize())
	core.WriteUInt32LE(r.ClipDataId, buff)

	c.Send(buff.Bytes())
}
