package t125

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/adfnekc/grdp/plugin/cliprdr"
	"github.com/adfnekc/grdp/plugin/disp"
	"github.com/adfnekc/grdp/plugin/drdynvc"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/t125/ber"
	"github.com/adfnekc/grdp/protocol/t125/gcc"
	"github.com/adfnekc/grdp/protocol/t125/per"
)

// take idea from https://github.com/Madnikulin50/gordp

// Multiple Channel Service layer

// MCSMessage is the octet that names a T.125 connect PDU, which is the first
// field of the message inside the GCC user data.
type MCSMessage uint8

// The two connect messages. A client sends the initial and reads the response;
// everything after the connection is a domain PDU instead.
const (
	MCS_TYPE_CONNECT_INITIAL  MCSMessage = 0x65
	MCS_TYPE_CONNECT_RESPONSE            = 0x66
)

// MCSDomainPDU is the opcode of a T.125 domain PDU: what the request or
// indication is. It shares one byte with the option bits that follow it, which
// is why it is shifted into place rather than written on its own.
type MCSDomainPDU uint16

// The domain PDU opcodes. A client sends the three requests and reads their
// confirmations, then reads and sends data: sendOpCode is the data opcode for the
// direction, which is why MCS is told one for receiving and one for sending.
const (
	ERECT_DOMAIN_REQUEST          MCSDomainPDU = 1
	DISCONNECT_PROVIDER_ULTIMATUM              = 8
	ATTACH_USER_REQUEST                        = 10
	ATTACH_USER_CONFIRM                        = 11
	CHANNEL_JOIN_REQUEST                       = 14
	CHANNEL_JOIN_CONFIRM                       = 15
	SEND_DATA_REQUEST                          = 25
	SEND_DATA_INDICATION                       = 26
)

// Reason names from T.125's DisconnectProviderUltimatum, for documentation. The
// byte a real server sends does not decode as a value of this list; see
// DisconnectError.
const (
	RN_DOMAIN_DISCONNECTED = 0
	RN_PROVIDER_INITIATED  = 1
	RN_TOKEN_PURGED        = 2
	RN_USER_REQUESTED      = 3
	RN_CHANNEL_PURGED      = 4
)

// DisconnectError is a Disconnect Provider Ultimatum: the server ending the
// session, or announcing that it is ending.
//
// What it is good for is the distinction that matters to a caller: the server
// ended this session, so the session still exists on the server, detached, and
// connecting again gets it back. A dropped network sends no ultimatum at all,
// so the presence of one is what tells the two apart, and that is a fact rather
// than a reading of the bytes.
//
// The reason is carried but NOT interpreted, and that is deliberate. What the
// bytes should mean is not settled: T.125 declares Reason a CHOICE of NULLs
// while other renderings of the same ASN.1 declare an ENUMERATED, and the byte a
// real server sends does not decode as either. Against a local xrdp a takeover
// arrives as
//
//	tpkt recvData 02 f0 80 21 80
//
// where 21 is the option byte naming disconnectProviderUltimatum and 80 is the
// reason, and 0x80 is not a value a five alternative Reason can hold. Guessing
// what it stands for would be exactly the mistake this repository keeps
// catching, so the reason is exposed as it arrived and the classification uses
// only what is certain.
type DisconnectError struct {
	// Reason is the byte after the option, passed through unread. Its meaning
	// is not established; see the note above.
	Reason uint8
}

// Error reports the ultimatum and passes the reason byte through unread, because
// what it means is not established; see the type's comment.
// Error reports that the server ended the session.
func (e *DisconnectError) Error() string {
	return fmt.Sprintf("mcs: the server ended the session (disconnect ultimatum, reason byte 0x%02x)", e.Reason)
}

// Channel numbering. The global channel is 1003 and a client's own channel is
// the base plus its user id, so the first user id a client can have is 1002.
// Static virtual channels are allocated upwards from the user channel, and the
// ids a server confirms must be used rather than the ones requested.
const (
	MCS_GLOBAL_CHANNEL_ID uint16 = 1003
	MCS_USERCHANNEL_BASE         = 1001
)

// GLOBAL_CHANNEL_NAME is the name of channel 1003, which carries the share data
// header, input and the core PDUs. It is the channel everything the static
// virtual channels do not carry goes through.
const (
	GLOBAL_CHANNEL_NAME = "global"
)

// writeMCSPDUHeader writes the one byte that opens every domain PDU: the opcode
// in the high six bits and the option bits below it.
func writeMCSPDUHeader(mcsPdu MCSDomainPDU, options uint8, w io.Writer) {
	core.WriteUInt8((uint8(mcsPdu)<<2)|options, w)
}

// readMCSPDUHeader reports whether an option byte names the given opcode.
func readMCSPDUHeader(options uint8, mcsPdu MCSDomainPDU) bool {
	return (options >> 2) == uint8(mcsPdu)
}

// DomainParameters is the T.125 DomainParameters structure, which appears three
// times in a Connect-Initial: as the target the client wants, and as the minimum
// and maximum it will accept. The server answers with one set.
//
// MinThoughput is misspelt, and exported, so it is left alone rather than renamed
// under the caller. It is the minimum throughput the sender requires.
type DomainParameters struct {
	MaxChannelIds   int
	MaxUserIds      int
	MaxTokenIds     int
	NumPriorities   int
	MinThoughput    int
	MaxHeight       int
	MaxMCSPDUsize   int
	ProtocolVersion int
}

/**
 * @see http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25
 * @returns {asn1.univ.Sequence}
 */
// NewDomainParameters returns a set of domain parameters.
//
// The values a client sends are fixed by what other clients send rather than by
// anything in the specification, so the three sets are built by NewConnectInitial
// and this is not usually called directly.
//
// See http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25.
func NewDomainParameters(
	maxChannelIds int,
	maxUserIds int,
	maxTokenIds int,
	numPriorities int,
	minThoughput int,
	maxHeight int,
	maxMCSPDUsize int,
	protocolVersion int) *DomainParameters {
	return &DomainParameters{maxChannelIds, maxUserIds, maxTokenIds,
		numPriorities, minThoughput, maxHeight, maxMCSPDUsize, protocolVersion}
}

// BER returns the structure as eight BER integers.
//
// Four of the eight fields are written as fixed values and not from the struct:
// the number of priorities is always 1, the minimum throughput always 0, the
// maximum height always 1 and the protocol version always 2. The values a real
// client sends are the same for every session, so the fields exist to be parsed
// rather than to be set, and a caller that changes them will not see the change on
// the wire.
func (d *DomainParameters) BER() []byte {
	buff := &bytes.Buffer{}
	ber.WriteInteger(d.MaxChannelIds, buff)
	ber.WriteInteger(d.MaxUserIds, buff)
	ber.WriteInteger(d.MaxTokenIds, buff)
	ber.WriteInteger(1, buff)
	ber.WriteInteger(0, buff)
	ber.WriteInteger(1, buff)
	ber.WriteInteger(d.MaxMCSPDUsize, buff)
	ber.WriteInteger(2, buff)
	return buff.Bytes()
}

// ReadDomainParameters reads a DomainParameters structure, skipping the four
// fields whose values are fixed rather than learned: they are read so the stream
// advances, and then discarded.
func ReadDomainParameters(r io.Reader) (*DomainParameters, error) {
	if !ber.ReadUniversalTag(ber.TAG_SEQUENCE, true, r) {
		return nil, errors.New("bad BER tags")
	}
	d := &DomainParameters{}
	ber.ReadLength(r)

	d.MaxChannelIds, _ = ber.ReadInteger(r)
	d.MaxUserIds, _ = ber.ReadInteger(r)
	d.MaxTokenIds, _ = ber.ReadInteger(r)
	ber.ReadInteger(r)
	ber.ReadInteger(r)
	ber.ReadInteger(r)
	d.MaxMCSPDUsize, _ = ber.ReadInteger(r)
	ber.ReadInteger(r)
	return d, nil
}

// ConnectInitial is the T.125 Connect-Initial, which is the first thing sent on a
// connection and carries the GCC user data with the client's core, network and
// security capabilities in it.
//
// It holds three sets of domain parameters rather than one because T.125
// negotiates by range: what the client wants, and the least and most it will
// accept.
//
// See http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25.
type ConnectInitial struct {
	CallingDomainSelector []byte
	CalledDomainSelector  []byte
	UpwardFlag            bool
	TargetParameters      DomainParameters
	MinimumParameters     DomainParameters
	MaximumParameters     DomainParameters
	UserData              []byte
}

// NewConnectInitial returns a Connect-Initial carrying the given GCC user data,
// with the three sets of domain parameters filled in with the values this client
// asks for.
func NewConnectInitial(userData []byte) ConnectInitial {
	return ConnectInitial{[]byte{0x1},
		[]byte{0x1},
		true,
		*NewDomainParameters(34, 2, 0, 1, 0, 1, 0xffff, 2),
		*NewDomainParameters(1, 1, 1, 1, 0, 1, 0x420, 2),
		*NewDomainParameters(0xffff, 0xfc17, 0xffff, 1, 0, 1, 0xffff, 2),
		userData}
}

// BER returns the Connect-Initial as the sequence of fields T.125 defines: the two
// domain selectors, the upward flag, the three parameter sets, and the GCC user
// data as an octet string.
func (c *ConnectInitial) BER() []byte {
	buff := &bytes.Buffer{}
	ber.WriteOctetstring(string(c.CallingDomainSelector), buff)
	ber.WriteOctetstring(string(c.CalledDomainSelector), buff)
	ber.WriteBoolean(c.UpwardFlag, buff)
	ber.WriteEncodedDomainParams(c.TargetParameters.BER(), buff)
	ber.WriteEncodedDomainParams(c.MinimumParameters.BER(), buff)
	ber.WriteEncodedDomainParams(c.MaximumParameters.BER(), buff)
	ber.WriteOctetstring(string(c.UserData), buff)
	return buff.Bytes()
}

// ConnectResponse is the server's answer to a Connect-Initial, which is what says
// the connection was accepted and carries the GCC data the server chose.
//
// Every field is unexported, so a caller can read the result of the exchange but
// not its contents: what the client needs from it is read during the connect
// sequence, and nothing outside needs the rest. The GCC data is what the
// capability exchange reads, and it goes to that layer as bytes.
//
// See http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25.
type ConnectResponse struct {
	result           uint8
	calledConnectId  int
	domainParameters *DomainParameters
	userData         []byte
}

// NewConnectResponse returns a Connect-Response with the given GCC user data.
//
// A client never sends one: this exists for tests that need the server's side of
// the exchange, and the parameters it holds are the ones a server would choose.
func NewConnectResponse(userData []byte) *ConnectResponse {
	return &ConnectResponse{0,
		0,
		NewDomainParameters(22, 3, 0, 1, 0, 1, 0xfff8, 2),
		userData}
}

// ReadConnectResponse reads a Connect-Response, starting at its application tag.
// The result code, the called connect id, the server's domain parameters and the
// GCC user data are all read; the result is what says whether the connection was
// accepted.
func ReadConnectResponse(r io.Reader) (*ConnectResponse, error) {
	c := &ConnectResponse{}
	var err error
	_, err = ber.ReadApplicationTag(MCS_TYPE_CONNECT_RESPONSE, r)
	if err != nil {
		return nil, err
	}
	c.result, err = ber.ReadEnumerated(r)
	if err != nil {
		return nil, err
	}

	c.calledConnectId, err = ber.ReadInteger(r)
	c.domainParameters, err = ReadDomainParameters(r)
	if err != nil {
		return nil, err
	}
	if !ber.ReadUniversalTag(ber.TAG_OCTET_STRING, false, r) {
		return nil, errors.New("invalid expected BER tag")
	}
	dataLen, _ := ber.ReadLength(r)
	c.userData, err = core.ReadBytes(dataLen, r)
	return c, err
}

// MCSChannelInfo is a channel as the client knows it: the id the server assigned
// and the name it was asked for under. The list of these is what SendToChannel
// searches, so a channel that was never connected is not in it.
type MCSChannelInfo struct {
	ID   uint16
	Name string
}

// MCS is the T.125 layer below the RDP protocol: it delivers the payload of each
// data PDU and passes writes down to the transport.
//
// It is told one opcode for receiving and one for sending because the data opcodes
// are directional: a server sends indications and a client sends requests, and the
// same layer on the other side is looking for the other one. Both ends therefore
// check the opcode they expect rather than accepting either.
type MCS struct {
	emission.Emitter
	transport  core.Transport
	recvOpCode MCSDomainPDU
	sendOpCode MCSDomainPDU
	channels   []MCSChannelInfo
}

// NewMCS returns an MCS layer over a transport, with the global channel already in
// its channel list. The global channel is not joined and is not confirmed by the
// server: it exists from the start, which is why it is in the list before any
// exchange has happened.
//
// The transport's close and error events are forwarded.
func NewMCS(t core.Transport, recvOpCode MCSDomainPDU, sendOpCode MCSDomainPDU) *MCS {
	m := &MCS{
		*emission.NewEmitter(),
		t,
		recvOpCode,
		sendOpCode,
		[]MCSChannelInfo{{MCS_GLOBAL_CHANNEL_ID, GLOBAL_CHANNEL_NAME}},
	}

	m.transport.On("close", func() {
		m.Emit("close")
	}).On("error", func(err error) {
		m.Emit("error", err)
	})
	return m
}

// Read reads from the transport with the T.125 framing left in place. It is not
// how a PDU arrives: the receive path strips the header and emits the payload, and
// a channel's data is delivered on the channel rather than here.
func (x *MCS) Read(b []byte) (n int, err error) {
	return x.transport.Read(b)
}

// Write writes to the transport with no T.125 framing. Use MCSClient.Write or
// SendToChannel to send something that a server will understand.
func (x *MCS) Write(b []byte) (n int, err error) {
	return x.transport.Write(b)
}

// Close closes the transport. It does not stop the receive path, which notices and
// emits "close".
func (m *MCS) Close() error {
	return m.transport.Close()
}

// MCSClient is the client end of the T.125 layer, and it owns the connect
// sequence: erect domain, attach user, then join each static channel in turn,
// each one waiting for the server's confirmation before the next is requested.
//
// It also builds the GCC data the Connect-Initial carries, which is why the
// capability setters below live here rather than on a layer of their own: they add
// to the client core, network and security structures that go out with the
// connect.
type MCSClient struct {
	*MCS
	clientCoreData     *gcc.ClientCoreData
	clientNetworkData  *gcc.ClientNetworkData
	clientSecurityData *gcc.ClientSecurityData

	serverCoreData     *gcc.ServerCoreData
	serverNetworkData  *gcc.ServerNetworkData
	serverSecurityData *gcc.ServerSecurityData

	channelsConnected  int
	userId             uint16
	nbChannelRequested int
}

// NewMCSClient returns a client over a transport, with its GCC data ready and its
// user id chosen.
//
// The user id is one above the base, which is the first id a client can have, and
// the channel it belongs to is the one a server expects to attach: attaching a
// different id is what a takeover looks like.
//
// The connect sequence starts when the transport emits "connect", not here, so a
// client can be built and configured before anything is sent.
func NewMCSClient(t core.Transport) *MCSClient {
	c := &MCSClient{
		MCS:                NewMCS(t, SEND_DATA_INDICATION, SEND_DATA_REQUEST),
		clientCoreData:     gcc.NewClientCoreData(),
		clientNetworkData:  gcc.NewClientNetworkData(),
		clientSecurityData: gcc.NewClientSecurityData(),
		userId:             1 + MCS_USERCHANNEL_BASE,
	}
	c.transport.On("connect", c.connect)
	return c
}

// SetClientDesktop sets the desktop size the client advertises in its core data.
// It is what the server uses until Display Control changes the size later, so a
// client that never resizes still has to set it here.
func (c *MCSClient) SetClientDesktop(width, height uint16) {
	c.clientCoreData.DesktopWidth = width
	c.clientCoreData.DesktopHeight = height
}

// SetClientDynvcProtocol asks for the dynamic channel protocol, which is what the
// graphics channel and Display Control travel inside, and sets the capability flag
// that tells the server so.
//
// Both halves are needed: the flag without the channel gets no dynamic channel, and
// the channel without the flag is not created.
func (c *MCSClient) SetClientDynvcProtocol() {
	c.clientCoreData.EarlyCapabilityFlags = gcc.RNS_UD_CS_SUPPORT_DYNVC_GFX_PROTOCOL
	c.clientNetworkData.AddVirtualChannel(drdynvc.ChannelName, drdynvc.ChannelOption)
}

// SetClientDisplayControl asks for Display Control, both ways round.
//
// The messages travel on the dynamic channel, but the static channel of the same
// name is advertised beside drdynvc as well, because that is what FreeRDP does
// and because Windows will not create the dynamic one without it: asked for
// drdynvc alone, the server never mentions
// Microsoft::Windows::RDS::DisplayControl at all.
func (c *MCSClient) SetClientDisplayControl() {
	c.clientNetworkData.AddVirtualChannel(disp.ChannelName, disp.ChannelOption)
}

// SetClientCliprdr asks for the clipboard channel.
func (c *MCSClient) SetClientCliprdr() {
	c.clientNetworkData.AddVirtualChannel(cliprdr.ChannelName, cliprdr.ChannelOption)
}

func (c *MCSClient) connect(selectedProtocol uint32) {
	glog.Debug("mcs client on connect", selectedProtocol)
	c.clientCoreData.ServerSelectedProtocol = selectedProtocol

	glog.Debugf("clientCoreData:%+v", c.clientCoreData)
	glog.Debugf("clientNetworkData:%+v", c.clientNetworkData)
	glog.Debugf("clientSecurityData:%+v", c.clientSecurityData)
	// sendConnectclientCoreDataInitial
	userDataBuff := bytes.Buffer{}
	userDataBuff.Write(c.clientCoreData.Pack())
	userDataBuff.Write(c.clientNetworkData.Pack())
	userDataBuff.Write(c.clientSecurityData.Pack())

	ccReq := gcc.MakeConferenceCreateRequest(userDataBuff.Bytes())
	connectInitial := NewConnectInitial(ccReq)
	connectInitialBerEncoded := connectInitial.BER()

	dataBuff := &bytes.Buffer{}
	ber.WriteApplicationTag(uint8(MCS_TYPE_CONNECT_INITIAL), len(connectInitialBerEncoded), dataBuff)
	dataBuff.Write(connectInitialBerEncoded)

	_, err := c.transport.Write(dataBuff.Bytes())
	if err != nil {
		c.Emit("error", errors.New(fmt.Sprintf("mcs sendConnectInitial write error %v", err)))
		return
	}
	glog.Debug("mcs wait for data event")
	c.transport.Once("data", c.recvConnectResponse)
}

func (c *MCSClient) recvConnectResponse(s []byte) {
	glog.Trace("mcs recvConnectResponse", glog.Hex(s))
	cResp, err := ReadConnectResponse(bytes.NewReader(s))
	if err != nil {
		c.Emit("error", errors.New(fmt.Sprintf("ReadConnectResponse %v", err)))
		return
	}
	// record server gcc block
	serverSettings := gcc.ReadConferenceCreateResponse(cResp.userData)
	for _, v := range serverSettings {
		switch v.(type) {
		case *gcc.ServerSecurityData:
			c.serverSecurityData = v.(*gcc.ServerSecurityData)

		case *gcc.ServerCoreData:
			c.serverCoreData = v.(*gcc.ServerCoreData)

		case *gcc.ServerNetworkData:
			c.serverNetworkData = v.(*gcc.ServerNetworkData)

		default:
			err := errors.New(fmt.Sprintf("unhandle server gcc block %v", reflect.TypeOf(v)))
			glog.Error(err)
			c.Emit("error", err)
			return
		}
	}
	glog.Debugf("serverSecurityData: %+v", c.serverSecurityData)
	glog.Debugf("serverCoreData: %+v", c.serverCoreData)
	glog.Debugf("serverNetworkData: %+v", c.serverNetworkData)
	glog.Debug("mcs sendErectDomainRequest")
	c.sendErectDomainRequest()

	glog.Debug("mcs sendAttachUserRequest")
	c.sendAttachUserRequest()

	c.transport.Once("data", c.recvAttachUserConfirm)
}

func (c *MCSClient) sendErectDomainRequest() {
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(ERECT_DOMAIN_REQUEST, 0, buff)
	per.WriteInteger(0, buff)
	per.WriteInteger(0, buff)
	c.transport.Write(buff.Bytes())
}

func (c *MCSClient) sendAttachUserRequest() {
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(ATTACH_USER_REQUEST, 0, buff)
	c.transport.Write(buff.Bytes())
}

func (c *MCSClient) recvAttachUserConfirm(s []byte) {
	glog.Debug("mcs recvAttachUserConfirm", glog.Hex(s))
	r := bytes.NewReader(s)

	option, err := core.ReadUInt8(r)
	if err != nil {
		c.Emit("error", err)
		return
	}

	if !readMCSPDUHeader(option, ATTACH_USER_CONFIRM) {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_BAD_HEADER"))
		return
	}

	e, err := per.ReadEnumerates(r)
	if err != nil {
		c.Emit("error", err)
		return
	}
	if e != 0 {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_SERVER_REJECT_USER'"))
		return
	}

	userId, _ := per.ReadInteger16(r)
	userId += MCS_USERCHANNEL_BASE
	c.userId = userId

	c.channels = append(c.channels, MCSChannelInfo{userId, "user"})
	c.connectChannels()
}

func (c *MCSClient) connectChannels() {
	glog.Debug("mcs connectChannels:", c.channelsConnected, ":", len(c.channels))
	if c.channelsConnected == len(c.channels) {
		if c.nbChannelRequested < int(c.serverNetworkData.ChannelCount) {
			//static virtual channel
			chanId := c.serverNetworkData.ChannelIdArray[c.nbChannelRequested]
			c.nbChannelRequested++
			c.sendChannelJoinRequest(chanId)
			c.transport.Once("data", c.recvChannelJoinConfirm)
			return
		}
		c.transport.On("data", c.recvData)
		// send client and sever gcc informations callback to sec
		clientData := make([]interface{}, 0)
		clientData = append(clientData, c.clientCoreData)
		clientData = append(clientData, c.clientSecurityData)
		clientData = append(clientData, c.clientNetworkData)

		serverData := make([]interface{}, 0)
		serverData = append(serverData, c.serverCoreData)
		serverData = append(serverData, c.serverSecurityData)
		glog.Debug("msc connectChannels callback to sec")
		c.Emit("connect", clientData, serverData, c.userId, c.channels)
		return
	}

	// sendChannelJoinRequest
	glog.Debug("sendChannelJoinRequest:", c.channels[c.channelsConnected].Name)
	c.sendChannelJoinRequest(c.channels[c.channelsConnected].ID)

	c.transport.Once("data", c.recvChannelJoinConfirm)
}

func (c *MCSClient) sendChannelJoinRequest(channelId uint16) {
	glog.Debug("mcs sendChannelJoinRequest", channelId)
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(CHANNEL_JOIN_REQUEST, 0, buff)
	per.WriteInteger16(c.userId-MCS_USERCHANNEL_BASE, buff)
	per.WriteInteger16(channelId, buff)
	c.transport.Write(buff.Bytes())
}

func (c *MCSClient) recvData(s []byte) {
	glog.Trace("msc on data recvData:", glog.Hex(s))

	r := bytes.NewReader(s)
	option, err := core.ReadUInt8(r)
	if err != nil {
		c.Emit("error", err)
		return
	}

	if readMCSPDUHeader(option, DISCONNECT_PROVIDER_ULTIMATUM) {
		// The reason follows the option byte as a one octet PER enumerated
		// value. It used to be dropped, and the whole thing reported as the
		// string "MCS DISCONNECT_PROVIDER_ULTIMATUM", which left a caller no
		// way to tell being taken over from losing the network except by
		// matching on that text.
		reason, err := core.ReadUInt8(r)
		if err != nil {
			// A truncated ultimatum still means the session is over, so it is
			// reported with the reason unknown rather than dropped.
			glog.Warnf("mcs: disconnect ultimatum without a reason: %v", err)
			reason = 0xff
		}
		c.Emit("error", &DisconnectError{Reason: reason})
		c.transport.Close()
		return
	} else if !readMCSPDUHeader(option, c.recvOpCode) {
		c.Emit("error", errors.New("Invalid expected MCS opcode receive data"))
		return
	}

	userId, _ := per.ReadInteger16(r)
	userId += MCS_USERCHANNEL_BASE

	channelId, _ := per.ReadInteger16(r)
	per.ReadEnumerates(r)
	size, _ := per.ReadLength(r)
	// channel ID doesn't match a requested layer
	found := false
	channelName := ""
	for _, channel := range c.channels {
		if channel.ID == channelId {
			found = true
			channelName = channel.Name
			break
		}
	}
	if !found {
		glog.Error("mcs receive data for an unconnected layer")
		return
	}
	left, err := core.ReadBytes(int(size), r)
	if err != nil {
		c.Emit("error", errors.New(fmt.Sprintf("mcs recvData get data error %v", err)))
		return
	}
	glog.Debugf("mcs emit channel<%s>", channelName)
	c.Emit("sec", channelName, left)
}

func (c *MCSClient) recvChannelJoinConfirm(s []byte) {
	glog.Debug("mcs recvChannelJoinConfirm", glog.Hex(s))
	r := bytes.NewReader(s)
	option, err := core.ReadUInt8(r)
	if err != nil {
		c.Emit("error", err)
		return
	}

	if !readMCSPDUHeader(option, CHANNEL_JOIN_CONFIRM) {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_WAIT_CHANNEL_JOIN_CONFIRM"))
		return
	}

	confirm, _ := per.ReadEnumerates(r)
	userId, _ := per.ReadInteger16(r)
	userId += MCS_USERCHANNEL_BASE

	if c.userId != userId {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_INVALID_USER_ID"))
		return
	}

	channelId, _ := per.ReadInteger16(r)
	if (confirm != 0) && (channelId == uint16(MCS_GLOBAL_CHANNEL_ID) || channelId == c.userId) {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_SERVER_MUST_CONFIRM_STATIC_CHANNEL"))
		return
	}
	glog.Debug("Confirm channelId:", channelId)
	if confirm == 0 {
		for i := 0; i < int(c.serverNetworkData.ChannelCount); i++ {
			if channelId == c.serverNetworkData.ChannelIdArray[i] {
				var t MCSChannelInfo
				t.ID = channelId
				t.Name = string(c.clientNetworkData.ChannelDefArray[i].Name[:])
				c.channels = append(c.channels, t)
			}
		}
	}
	c.channelsConnected++
	c.connectChannels()
}

// Pack builds a data PDU: the opcode header, the user id relative to the base, the
// channel, the fixed 0x70 data header, and a PER length in front of the data.
//
// The user id goes out relative to the base because that is what the field counts.
// The length is a PER length and not the BER length used at the connect, which is
// the sort of difference that makes a data PDU unreadable if it is got wrong.
func (c *MCSClient) Pack(data []byte, channelId uint16) []byte {
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(c.sendOpCode, 0, buff)
	per.WriteInteger16(c.userId-MCS_USERCHANNEL_BASE, buff)
	per.WriteInteger16(channelId, buff)
	core.WriteUInt8(0x70, buff)
	per.WriteLength(len(data), buff)
	core.WriteBytes(data, buff)
	glog.Trace("MCSClient write", channelId, ":", glog.Hex(buff.Bytes()))
	return buff.Bytes()
}

// Write sends data on the global channel, which is first in the channel list.
func (c *MCSClient) Write(data []byte) (n int, err error) {
	data = c.Pack(data, c.channels[0].ID)
	return c.transport.Write(data)
}

// SendToChannel sends data on the named static channel, looking the name up in the
// list of channels the server confirmed.
//
// A name that is not in the list is not an error: the data goes to the global
// channel instead. That is what makes a send to a channel that was never connected
// look like it worked, so a caller that needs to know should check the name it is
// using rather than the error this returns.
func (c *MCSClient) SendToChannel(channel string, data []byte) (n int, err error) {
	channelId := c.channels[0].ID
	for _, ch := range c.channels {
		if channel == ch.Name {
			channelId = ch.ID
			break
		}
	}

	data = c.Pack(data, channelId)
	return c.transport.Write(data)
}
