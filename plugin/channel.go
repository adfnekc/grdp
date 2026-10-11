package plugin

import (
	"bytes"
	"fmt"
	"unsafe"

	"github.com/adfnekc/grdp/glog"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
)

// The return codes of the FreeRDP virtual channel API. They are defined here to
// match that API's values rather than for use in this library: nothing in the
// build returns one. See the note on ChannelEntryPointsEx.
const (
	CHANNEL_RC_OK                         = 0
	CHANNEL_RC_ALREADY_INITIALIZED        = 1
	CHANNEL_RC_NOT_INITIALIZED            = 2
	CHANNEL_RC_ALREADY_CONNECTED          = 3
	CHANNEL_RC_NOT_CONNECTED              = 4
	CHANNEL_RC_TOO_MANY_CHANNELS          = 5
	CHANNEL_RC_BAD_CHANNEL                = 6
	CHANNEL_RC_BAD_CHANNEL_HANDLE         = 7
	CHANNEL_RC_NO_BUFFER                  = 8
	CHANNEL_RC_BAD_INIT_HANDLE            = 9
	CHANNEL_RC_NOT_OPEN                   = 10
	CHANNEL_RC_BAD_PROC                   = 11
	CHANNEL_RC_NO_MEMORY                  = 12
	CHANNEL_RC_UNKNOWN_CHANNEL_NAME       = 13
	CHANNEL_RC_ALREADY_OPEN               = 14
	CHANNEL_RC_NOT_IN_VIRTUALCHANNELENTRY = 15
	CHANNEL_RC_NULL_DATA                  = 16
	CHANNEL_RC_ZERO_LENGTH                = 17
	CHANNEL_RC_INVALID_INSTANCE           = 18
	CHANNEL_RC_UNSUPPORTED_VERSION        = 19
	CHANNEL_RC_INITIALIZATION_ERROR       = 20
)

// VIRTUAL_CHANNEL_VERSION_WIN2000 is the version to request in the FreeRDP channel
// API. It is the only version defined, so asking for anything else is the same as
// asking for this.
const (
	VIRTUAL_CHANNEL_VERSION_WIN2000 = 1
)

// The events a virtual channel is told about. DATA_RECEIVED and WRITE_COMPLETE are
// what a channel implementation acts on; the others describe the life of the
// session, including the two remote control events and the attached and detached
// pair, which this library never sends.
const (
	CHANNEL_EVENT_INITIALIZED          = 0
	CHANNEL_EVENT_CONNECTED            = 1
	CHANNEL_EVENT_V1_CONNECTED         = 2
	CHANNEL_EVENT_DISCONNECTED         = 3
	CHANNEL_EVENT_TERMINATED           = 4
	CHANNEL_EVENT_REMOTE_CONTROL_START = 5
	CHANNEL_EVENT_REMOTE_CONTROL_STOP  = 6
	CHANNEL_EVENT_ATTACHED             = 7
	CHANNEL_EVENT_DETACHED             = 8
	CHANNEL_EVENT_DATA_RECEIVED        = 10
	CHANNEL_EVENT_WRITE_COMPLETE       = 11
	CHANNEL_EVENT_WRITE_CANCELLED      = 12
)

// The channel options, which are the same values as gcc.ChannelOptions: a channel
// is asked for with these flags in the Connect-Initial and they are what the server
// reads. The duplication is deliberate, because this package does not depend on the
// protocol packages, and the two must be kept in step by hand.
const (
	CHANNEL_OPTION_INITIALIZED               = 0x80000000
	CHANNEL_OPTION_ENCRYPT_RDP               = 0x40000000
	CHANNEL_OPTION_ENCRYPT_SC                = 0x20000000
	CHANNEL_OPTION_ENCRYPT_CS                = 0x10000000
	CHANNEL_OPTION_PRI_HIGH                  = 0x08000000
	CHANNEL_OPTION_PRI_MED                   = 0x04000000
	CHANNEL_OPTION_PRI_LOW                   = 0x02000000
	CHANNEL_OPTION_COMPRESS_RDP              = 0x00800000
	CHANNEL_OPTION_COMPRESS                  = 0x00400000
	CHANNEL_OPTION_SHOW_PROTOCOL             = 0x00200000
	CHANNEL_OPTION_REMOTE_CONTROL_PERSISTENT = 0x00100000
)

// ChannelDef is a channel as it is asked for: the name and the options.
type ChannelDef struct {
	Name    string
	Options uint32
}

// CHANNEL_INIT_EVENT_EX_FN is the callback a channel registers to be told about the
// life of the session: initialised, connected, disconnected and terminated.
type CHANNEL_INIT_EVENT_EX_FN func(lpUserParam interface{},
	pInitHandle interface{}, event uint, pData uintptr, dataLength uint)

// VIRTUALCHANNELINITEX registers the channels a plugin wants, and the callback it
// wants called for session events.
type VIRTUALCHANNELINITEX func(lpUserParam interface{}, clientContext interface{},
	pInitHandle interface{}, pChannel []ChannelDef,
	channelCount int, versionRequested uint32,
	pChannelInitEventProcEx CHANNEL_INIT_EVENT_EX_FN) uint

// CHANNEL_OPEN_EVENT_EX_FN is the callback a channel registers when it is opened. It
// is what delivers data to it, as a pointer and a length rather than a slice, which
// is the shape the Windows API uses.
type CHANNEL_OPEN_EVENT_EX_FN func(lpUserParam uintptr,
	openHandle uint32, event uint,
	pData uintptr, dataLength uint32, totalLength uint32, dataFlags uint32)

// VIRTUALCHANNELOPENEX opens one of the registered channels and returns a handle for
// it, which the other calls take.
type VIRTUALCHANNELOPENEX func(pInitHandle interface{}, pOpenHandle *uint32,
	pChannelName string,
	pChannelOpenEventProcEx *CHANNEL_OPEN_EVENT_EX_FN) uint

// VIRTUALCHANNELCLOSEEX closes an open channel.
type VIRTUALCHANNELCLOSEEX func(pInitHandle interface{}, openHandle uint32) uint

// VIRTUALCHANNELWRITEEX writes to an open channel.
type VIRTUALCHANNELWRITEEX func(pInitHandle interface{}, openHandle uint32, pData interface{},
	dataLength uint32, pUserData interface{}) uint

// ChannelEntryPointsEx is the FreeRDP virtual channel entry points, which is how a
// Windows channel plugin is handed the functions it calls back into.
//
// Nothing in the build uses this: its only consumer is the addins port in
// plugin/addins.go, which is switched off with //go:build ignore because it needs
// Windows and defines the same functions with LazyDLL. It is kept because the port
// is unfinished rather than abandoned, and the whole API in this file is here for
// the same reason.
type ChannelEntryPointsEx struct {
	CbSize                 uint32
	ProtocolVersion        uint32
	PVirtualChannelInitEx  VIRTUALCHANNELINITEX
	PVirtualChannelOpenEx  VIRTUALCHANNELOPENEX
	PVirtualChannelCloseEx VIRTUALCHANNELCLOSEEX
	PVirtualChannelWriteEx VIRTUALCHANNELWRITEEX
}

// NewChannelEntryPointsEx returns entry points with the size and protocol version
// filled in, which is what a caller has to set before handing them to a plugin.
//
// The size is computed rather than written, so that adding a function pointer to the
// structure does not leave the field disagreeing with it.
func NewChannelEntryPointsEx() *ChannelEntryPointsEx {
	e := &ChannelEntryPointsEx{}
	e.CbSize = uint32(unsafe.Sizeof(e))
	e.ProtocolVersion = VIRTUAL_CHANNEL_VERSION_WIN2000
	return e
}

// VIRTUALCHANNELENTRYEX is the function a plugin exports, which the client calls
// once to hand over the entry points. Its name is fixed by the API rather than
// chosen, which is why it is exported here and why it cannot be renamed.
type VIRTUALCHANNELENTRYEX func(pEntryPointsEx *ChannelEntryPointsEx,
	pInitHandle interface{}) error

/*
type ChannelEntryPoints struct {
	CbSize               uint32
	ProtocolVersion      uint32
	PVirtualChannelInit  PVIRTUALCHANNELINIT
	PVirtualChannelOpen  PVIRTUALCHANNELOPEN
	PVirtualChannelClose PVIRTUALCHANNELCLOSE
	PVirtualChannelWrite PVIRTUALCHANNELWRITE
}
typedef VOID VCAPITYPE CHANNEL_INIT_EVENT_FN(LPVOID pInitHandle,
        UINT event, LPVOID pData, UINT dataLength);

typedef CHANNEL_INIT_EVENT_FN* PCHANNEL_INIT_EVENT_FN;
typedef VOID VCAPITYPE CHANNEL_OPEN_EVENT_FN(DWORD openHandle, UINT event,
        LPVOID pData, UINT32 dataLength, UINT32 totalLength, UINT32 dataFlags);

typedef CHANNEL_OPEN_EVENT_FN* PCHANNEL_OPEN_EVENT_FN;
typedef UINT VCAPITYPE VIRTUALCHANNELINIT(LPVOID* ppInitHandle, PCHANNEL_DEF pChannel,
                                          INT channelCount, ULONG versionRequested,
                                          PCHANNEL_INIT_EVENT_FN pChannelInitEventProc);
typedef VIRTUALCHANNELINIT* PVIRTUALCHANNELINIT;

typedef UINT VCAPITYPE VIRTUALCHANNELOPEN(LPVOID pInitHandle, LPDWORD pOpenHandle,
                                          PCHAR pChannelName,
                                          PCHANNEL_OPEN_EVENT_FN pChannelOpenEventProc);

typedef VIRTUALCHANNELOPEN* PVIRTUALCHANNELOPEN;

typedef UINT VCAPITYPE VIRTUALCHANNELCLOSE(DWORD openHandle);
typedef VIRTUALCHANNELCLOSE* PVIRTUALCHANNELCLOSE;

typedef UINT VCAPITYPE VIRTUALCHANNELWRITE(DWORD openHandle, LPVOID pData, ULONG dataLength,
                                           LPVOID pUserData);
typedef VIRTUALCHANNELWRITE* PVIRTUALCHANNELWRITE;

typedef UINT VCAPITYPE VIRTUALCHANNELINITEX(LPVOID lpUserParam, LPVOID clientContext,
                                            LPVOID pInitHandle, PCHANNEL_DEF pChannel,
                                            INT channelCount, ULONG versionRequested,
                                            PCHANNEL_INIT_EVENT_EX_FN pChannelInitEventProcEx);
typedef VIRTUALCHANNELINITEX* PVIRTUALCHANNELINITEX;

typedef UINT VCAPITYPE VIRTUALCHANNELOPENEX(LPVOID pInitHandle, LPDWORD pOpenHandle,
                                            PCHAR pChannelName,
                                            PCHANNEL_OPEN_EVENT_EX_FN pChannelOpenEventProcEx);
typedef VIRTUALCHANNELOPENEX* PVIRTUALCHANNELOPENEX;


typedef UINT VCAPITYPE VIRTUALCHANNELCLOSEEX(LPVOID pInitHandle, DWORD openHandle);
typedef VIRTUALCHANNELCLOSEEX* PVIRTUALCHANNELCLOSEEX;

typedef UINT VCAPITYPE VIRTUALCHANNELWRITEEX(LPVOID pInitHandle, DWORD openHandle, LPVOID pData,
                                             ULONG dataLength, LPVOID pUserData);
typedef VIRTUALCHANNELWRITEEX* PVIRTUALCHANNELWRITEEX;
*/

// Static channel names. Only the ones this library actually speaks are here:
// a name with nothing behind it invites advertising a channel and then refusing
// to answer it, which is worse than not asking for it.
const (
	CLIPRDR_SVC_CHANNEL_NAME = "cliprdr" // clipboard
	DRDYNVC_SVC_CHANNEL_NAME = "drdynvc" // dynamic virtual channels
	DISP_SVC_CHANNEL_NAME    = "disp"    // display control
)

// RDPGFX_DVC_CHANNEL_NAME is the dynamic virtual channel the graphics protocol runs
// on. It is the name the server knows it by, so it is not a local choice.
const (
	RDPGFX_DVC_CHANNEL_NAME = "Microsoft::Windows::RDS::Graphics" // graphics
)

// The chunking of a channel message. A channel write is cut into chunks of at most
// CHANNEL_CHUNK_LENGTH, each with a header saying the total length of the whole
// message and which chunks it is, and the receiver puts them back together.
//
// The flags are the position of the chunk: FIRST begins a message and LAST ends one,
// and a message that fits in a single chunk carries both. SHOW_PROTOCOL asks for the
// header to be visible to the channel rather than being removed before delivery.
const (
	CHANNEL_CHUNK_LENGTH       = 1600
	CHANNEL_FLAG_FIRST         = 0x01
	CHANNEL_FLAG_LAST          = 0x02
	CHANNEL_FLAG_SHOW_PROTOCOL = 0x10
)

// ChannelTransport is what a plugin implements to be a channel: its name and options,
// where to send, and what to do with what arrives.
//
// It is the interface this library actually uses, and it is small on purpose. The
// FreeRDP API above describes the same idea in the shape a Windows plugin needs;
// this is the shape a Go one does.
type ChannelTransport interface {
	GetType() (string, uint32)
	Sender(core.ChannelSender)
	Process(s []byte)
}

// ChannelClient is a registered channel: its definition and the plugin behind it.
type ChannelClient struct {
	ChannelDef
	t ChannelTransport
}

// Channels is the set of static virtual channels in a session. It reads the channel
// messages out of the data path, puts chunked messages back together, and hands each
// one to the plugin that registered the channel it was for.
//
// The reassembly buffers are per channel and not shared, because a chunked message on
// one channel must not be corrupted by a message arriving on another.
type Channels struct {
	emission.Emitter
	channels      map[string]ChannelClient
	transport     core.Transport
	buffs         map[string]*bytes.Buffer
	channelSender core.ChannelSender
}

// NewChannels returns an empty set of channels, which listens for the transport's
// "channel" event. A plugin is added with Register, and nothing is sent until one is.
func NewChannels(t core.Transport) *Channels {
	c := &Channels{
		Emitter:   *emission.NewEmitter(),
		channels:  make(map[string]ChannelClient, 20),
		transport: t,
		buffs:     make(map[string]*bytes.Buffer),
	}
	t.On("channel", c.process)
	return c
}

// SetChannelSender sets what outgoing channel traffic is written through. It is set
// by the security layer once the connection is up, and a send before that has nowhere
// to go.
func (c *Channels) SetChannelSender(f core.ChannelSender) {
	c.channelSender = f
}

// Register adds a plugin as a channel. The name and options come from the plugin, and
// the plugin is given this set as its sender so that it can write back.
//
// Registering the same name twice is refused with a warning rather than replacing the
// first registration: a channel that was silently taken over by a second one would be
// very hard to see. This is also where the channel is asked for, since the options
// that go in the Connect-Initial are read from here.
func (c *Channels) Register(t ChannelTransport) {
	name, option := t.GetType()
	_, ok := c.channels[name]
	if ok {
		glog.Warn("Already register channel:", name)
		return
	}
	t.Sender(c)
	c.channels[name] = ChannelClient{ChannelDef{name, option}, t}
}

// SendToChannel cuts a message into chunks and sends it on a channel.
//
// Every chunk carries the total length of the message and its own position, so a
// message longer than CHANNEL_CHUNK_LENGTH arrives in pieces the receiver joins back
// together. The flag for showing the header is copied from the channel's options,
// because a channel that asked for it expects the header to be part of the data.
//
// Two things about what this returns are worth knowing: the count is always zero,
// because the variable it returns is the one the loop counts down, and the error from
// the sender is not returned at all. A caller cannot use either to tell whether the
// message arrived.
func (c *Channels) SendToChannel(channel string, s []byte) (int, error) {
	cli, ok := c.channels[channel]
	if !ok {
		glog.Warn("No register channel:", channel)
		return 0, fmt.Errorf("No register channel: %s", channel)
	}
	idx := 0
	ln := len(s)
	b := &bytes.Buffer{}
	for ln > 0 {
		var flag uint32 = 0
		if cli.Options&CHANNEL_OPTION_SHOW_PROTOCOL != 0 {
			flag |= CHANNEL_FLAG_SHOW_PROTOCOL
		}
		if idx == 0 {
			flag |= CHANNEL_FLAG_FIRST
		}

		var ss []byte
		if ln > CHANNEL_CHUNK_LENGTH {
			ss = s[idx : idx+CHANNEL_CHUNK_LENGTH]
			idx += CHANNEL_CHUNK_LENGTH
		} else {
			flag |= CHANNEL_FLAG_LAST
			ss = s[idx : idx+ln]
		}
		glog.Debug("len:", len(ss), "flag:", flag)
		ln -= len(ss)
		b.Reset()
		core.WriteUInt32LE(uint32(len(s)), b)
		core.WriteUInt32LE(flag, b)
		b.Write(ss)
		c.channelSender.SendToChannel(channel, b.Bytes())
	}
	return ln, nil
}

func (c *Channels) process(channel string, s []byte) {
	cli, ok := c.channels[channel]
	if !ok {
		glog.Warn("No found channel:", channel)
		return
	}
	r := bytes.NewReader(s)
	ln, _ := core.ReadUInt32LE(r)
	flags, _ := core.ReadUInt32LE(r)
	glog.Debugf("channel:%s length: %d, flags: %d", channel, ln, flags)

	// Each channel needs its own reassembly buffer: a chunked message on one
	// channel must not be corrupted by a message arriving on another.
	buff, ok := c.buffs[channel]
	if !ok {
		buff = &bytes.Buffer{}
		c.buffs[channel] = buff
	}

	if flags&CHANNEL_FLAG_FIRST == 0 || flags&CHANNEL_FLAG_LAST == 0 {
		if flags&CHANNEL_FLAG_FIRST != 0 {
			buff.Reset()
		}
		b, _ := core.ReadBytes(r.Len(), r)
		buff.Write(b)
		if flags&CHANNEL_FLAG_LAST == 0 {
			return
		}
		s = buff.Bytes()
	} else {
		s, _ = core.ReadBytes(r.Len(), r)
	}

	cli.t.Process(s)
}
