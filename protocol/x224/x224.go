package x224

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/adfnekc/grdp/glog"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/protocol/tpkt"
	"github.com/lunixbochs/struc"
)

// take idea from https://github.com/Madnikulin50/gordp

// MessageType is the TPDU code in the first byte of an X.224 header. It says
// what the packet is, and it is the only field the connection sequence reads
// before deciding what to do with the rest.
type MessageType byte

// The TPDU codes. This client sends a connection request and then data, and
// reads a connection confirm or a disconnect request from the server.
const (
	TPDU_CONNECTION_REQUEST MessageType = 0xE0
	TPDU_CONNECTION_CONFIRM             = 0xD0
	TPDU_DISCONNECT_REQUEST             = 0x80
	TPDU_DATA                           = 0xF0
	TPDU_ERROR                          = 0x70
)

// NegotiationType is the Type field of an RDP Negotiation structure, which says
// whether that structure is a request, a response or a failure.
type NegotiationType byte

// The negotiation types. The same eight byte structure carries all three, and
// only this field tells them apart.
const (
	TYPE_RDP_NEG_REQ     NegotiationType = 0x01
	TYPE_RDP_NEG_RSP                     = 0x02
	TYPE_RDP_NEG_FAILURE                 = 0x03
)

// The security protocols a client can ask for, as a bitmask in the requested
// protocols field. They are ordered by strength rather than by value, and the
// server answers with the one it wants.
//
// The set this client asks for by default is TLS, CredSSP and Standard RDP
// Security together, which is what lets one code path reach a server that
// requires any of the three.
const (
	// PROTOCOL_RDP is Standard RDP Security: no external protocol, the legacy
	// encryption negotiated inside the RDP exchange itself.
	PROTOCOL_RDP uint32 = 0x00000000
	// PROTOCOL_SSL is TLS.
	PROTOCOL_SSL = 0x00000001
	// PROTOCOL_HYBRID is CredSSP, which is TLS with NTLM inside it: NLA.
	PROTOCOL_HYBRID = 0x00000002
	// PROTOCOL_RDSTLS is an extension where the server supplies the certificate.
	PROTOCOL_RDSTLS = 0x00000004
	// PROTOCOL_HYBRID_EX is CredSSP with the server's own early user
	// authentication, which also allows a logon to be refused with a reason.
	PROTOCOL_HYBRID_EX = 0x00000008
	// PROTOCOL_RDSAAD is Azure AD authentication.
	PROTOCOL_RDSAAD = 0x00000010
)

// Negotiation is the RDP Negotiation structure that rides inside the X.224
// connection request and confirm. Request, response and failure share the layout
// and are told apart by Type, so one Go type holds all three and only Result
// changes meaning: it is the requested protocol set, the selected protocol, or a
// failure code.
type Negotiation struct {
	Type   NegotiationType `struc:"byte"`
	Flag   uint8           `struc:"uint8"`
	Length uint16          `struc:"little"`
	Result uint32          `struc:"little"`
}

// NewNegotiation returns a negotiation request for Standard RDP Security. The
// requested protocols are replaced when the connection request is built, so a
// caller that wants TLS or CredSSP asks for it with SetRequestedProtocol.
func NewNegotiation() *Negotiation {
	return &Negotiation{0, 0, 0x0008 /*constant*/, PROTOCOL_RDP}
}

// The RDP_NEG_FAILURE codes: what a server says when it will not accept the
// security protocols that were asked for. They are the most useful thing in a
// failed handshake, because each names a different fix, and NegotiationFailure
// turns them into a sentence.
//
// They are also easy to mistake for a TLS fault: every one of them arrives
// before any TLS has happened, and SSL_REQUIRED_BY_SERVER in particular is what
// a connection offering only Standard RDP Security gets from a server that
// requires more.
type failureCode int

// The codes themselves. Each one is worth a sentence rather than a number,
// because each names a different fix.
const (
	//The server requires that the client support Enhanced RDP Security (section 5.4) with either TLS 1.0, 1.1 or 1.2 (section 5.4.5.1) or CredSSP (section 5.4.5.2). If only CredSSP was requested then the server only supports TLS.
	SSL_REQUIRED_BY_SERVER = 0x00000001

	//The server is configured to only use Standard RDP Security mechanisms (section 5.3) and does not support any External Security Protocols (section 5.4.5).
	SSL_NOT_ALLOWED_BY_SERVER = 0x00000002

	//The server does not possess a valid authentication certificate and cannot initialize the External Security Protocol Provider (section 5.4.5).
	SSL_CERT_NOT_ON_SERVER = 0x00000003

	//The list of requested security protocols is not consistent with the current security protocol in effect. This error is only possible when the Direct Approach (sections 5.4.2.2 and 1.3.1.2) is used and an External Security Protocol (section 5.4.5) is already being used.
	INCONSISTENT_FLAGS = 0x00000004

	//The server requires that the client support Enhanced RDP Security (section 5.4) with CredSSP (section 5.4.5.2).
	HYBRID_REQUIRED_BY_SERVER = 0x00000005

	//The server requires that the client support Enhanced RDP Security (section 5.4) with TLS 1.0, 1.1 or 1.2 (section 5.4.5.1) and certificate-based client authentication.<4>
	SSL_WITH_USER_AUTH_REQUIRED_BY_SERVER = 0x00000006
)

// ClientConnectionRequestPDU is the X.224 connection request, which opens the
// session. It carries an optional cookie, which is the client's routing token or
// the requested user name, and an optional RDP Negotiation Request.
type ClientConnectionRequestPDU struct {
	Len               uint8
	Code              MessageType
	Padding1          uint16
	Padding2          uint16
	Padding3          uint8
	Cookie            []byte
	requestedProtocol uint32
	ProtocolNeg       *Negotiation
}

// NewClientConnectionRequestPDU returns a connection request for the given
// cookie and requested protocol set.
//
// The length is computed here rather than by the caller, because it counts
// optional parts that are not present: the cookie contributes its own length plus
// two for the terminator, and the negotiation request contributes eight bytes and
// is left out entirely when nothing beyond Standard RDP Security is wanted.
func NewClientConnectionRequestPDU(cookie []byte, requestedProtocol uint32) *ClientConnectionRequestPDU {
	x := ClientConnectionRequestPDU{0, TPDU_CONNECTION_REQUEST, 0, 0, 0,
		cookie, requestedProtocol, NewNegotiation()}

	x.Len = 6
	if len(cookie) > 0 {
		x.Len += uint8(len(cookie) + 2)
	}
	if x.requestedProtocol > PROTOCOL_RDP {
		x.Len += 8
	}

	return &x
}

// Serialize returns the wire form of the request: the fixed header, the cookie
// terminated by a carriage return and line feed, and the negotiation request when
// one was asked for.
func (x *ClientConnectionRequestPDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt8(x.Len, buff)
	core.WriteUInt8(uint8(x.Code), buff)
	core.WriteUInt16BE(x.Padding1, buff)
	core.WriteUInt16BE(x.Padding2, buff)
	core.WriteUInt8(x.Padding3, buff)

	if len(x.Cookie) > 0 {
		buff.Write(x.Cookie)
		core.WriteUInt8(0x0D, buff)
		core.WriteUInt8(0x0A, buff)
	}

	if x.requestedProtocol > PROTOCOL_RDP {
		struc.Pack(buff, x.ProtocolNeg)
	}

	return buff.Bytes()
}

// ServerConnectionConfirm is the server's reply to the connection request. It is
// parsed rather than built, and its negotiation structure, when present, is what
// says which security protocol will be used.
type ServerConnectionConfirm struct {
	Len         uint8       `struc:"byte"`
	Code        MessageType `struc:"byte"`
	Padding1    uint16      `struc:"little"`
	Padding2    uint16      `struc:"little"`
	Padding3    uint8       `struc:"byte"`
	ProtocolNeg *Negotiation
}

// DataHeader is the three byte header that precedes every T.125 data PDU. The
// separator is a fixed value rather than a length, so a data PDU carries no
// length of its own and the enclosing TPKT is what says where it ends.
type DataHeader struct {
	Header      uint8       `struc:"little"`
	MessageType MessageType `struc:"uint8"`
	Separator   uint8       `struc:"little"`
}

// NewDataHeader returns the fixed header of a data PDU.
func NewDataHeader() *DataHeader {
	return &DataHeader{2, TPDU_DATA /* constant */, 0x80 /*constant*/}
}

// DefaultConnectTimeout bounds how long Connect waits for the server to
// confirm the X.224 connection and complete the security handshake.
const DefaultConnectTimeout = 20 * time.Second

// NegotiationFailure is returned when the server rejects the requested
// security protocol (RDP_NEG_FAILURE). See MS-RDPBCGR 2.2.1.2.1 for codes.
type NegotiationFailure struct {
	Code uint32
}

// Error turns a failure code into a sentence, because the code on its own reads
// like a protocol fault while each one actually names a different fix. An
// unrecognised code is reported as such rather than being forced into the map's
// zero value, which would silently mean Standard RDP Security.
func (e *NegotiationFailure) Error() string {
	msg := map[uint32]string{
		SSL_REQUIRED_BY_SERVER:                "the server requires Enhanced RDP Security (TLS or CredSSP)",
		SSL_NOT_ALLOWED_BY_SERVER:             "the server only supports Standard RDP Security",
		SSL_CERT_NOT_ON_SERVER:                "the server has no valid authentication certificate",
		INCONSISTENT_FLAGS:                    "inconsistent requested security protocols",
		HYBRID_REQUIRED_BY_SERVER:             "the server requires CredSSP (NLA)",
		SSL_WITH_USER_AUTH_REQUIRED_BY_SERVER: "the server requires TLS with client authentication",
	}[e.Code]
	if msg == "" {
		msg = "unknown failure code"
	}
	return fmt.Sprintf("x224: RDP security negotiation failed (0x%08x): %s", e.Code, msg)
}

// X224 is the layer that opens the session and frames what follows. It sends the
// connection request, waits for the confirm, and delivers the payload of each
// data PDU without its three byte header.
//
// The security handshake belongs here as well as the connection request, because
// the confirm is what says which protocol was chosen: nothing below can start
// until that is known, and nothing above knows it happened. A failure during the
// handshake is therefore reported on this layer's connect result rather than on
// the data path.
type X224 struct {
	emission.Emitter
	transport         core.Transport
	requestedProtocol uint32
	selectedProtocol  uint32
	dataHeader        *DataHeader
	connectTimeout    time.Duration
	connectResult     chan error
}

// New returns an X.224 layer over a transport. It asks for TLS, CredSSP and
// Standard RDP Security together and assumes TLS was chosen until the confirm
// says otherwise, so a server that requires any of the three is reachable without
// configuring which one to expect.
//
// The transport's close and error events are forwarded, which is how a
// connection that dies during the handshake reaches the caller.
func New(t core.Transport) *X224 {
	x := &X224{
		Emitter:           *emission.NewEmitter(),
		transport:         t,
		requestedProtocol: PROTOCOL_RDP | PROTOCOL_SSL | PROTOCOL_HYBRID,
		selectedProtocol:  PROTOCOL_SSL,
		dataHeader:        NewDataHeader(),
		connectTimeout:    DefaultConnectTimeout,
	}

	t.On("close", func() {
		x.Emit("close")
	}).On("error", func(err error) {
		x.Emit("error", err)
	})

	return x
}

// SetConnectTimeout overrides the handshake timeout used by Connect.
func (x *X224) SetConnectTimeout(d time.Duration) {
	x.connectTimeout = d
}

// Read reads from the transport with the T.125 framing left in place. It is not
// how a PDU arrives: the receive path strips the three byte data header and emits
// the payload as a "data" event.
func (x *X224) Read(b []byte) (n int, err error) {
	n, err = x.transport.Read(b)
	return n, err
}

// Write writes a T.125 data PDU, which is the three byte data header followed by
// the payload. The caller supplies the payload only.
func (x *X224) Write(b []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	err = struc.Pack(buff, x.dataHeader)
	if err != nil {
		return 0, err
	}
	buff.Write(b)

	glog.Trace("x224 write:", glog.Hex(buff.Bytes()))
	return x.transport.Write(buff.Bytes())
}

// Close closes the transport. It does not stop the receive path, which notices
// and emits "close".
func (x *X224) Close() error {
	return x.transport.Close()
}

// SetRequestedProtocol replaces the security protocols asked for in the
// connection request. It has no effect once Connect has been called, because the
// request has been sent by then.
func (x *X224) SetRequestedProtocol(p uint32) {
	x.requestedProtocol = p
}

// Connect performs the connection sequence with the default timeout. It is
// ConnectContext with no way to cancel it, so a client that needs to abandon a
// handshake should use that instead.
func (x *X224) Connect() error {
	return x.ConnectContext(context.Background())
}

// ConnectContext performs the X.224 connection request / confirm exchange and
// the negotiated security handshake, blocking until it completes, fails or the
// context (or the connect timeout) expires.
func (x *X224) ConnectContext(ctx context.Context) error {
	if x.transport == nil {
		return errors.New("no transport")
	}

	timeout := x.connectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := make(chan error, 1)
	x.connectResult = result

	// Arm the confirm handler BEFORE writing so a fast server response cannot
	// be dropped (previously this raced and could hang forever).
	x.transport.Once("data", x.recvConnectionConfirm)

	errCh := make(chan error, 1)
	x.transport.Once("error", func(err error) {
		if err == nil {
			err = errors.New("transport error")
		}
		errCh <- err
	})
	x.transport.Once("close", func() {
		errCh <- io.EOF
	})

	cookie := "Cookie: mstshash=test"
	message := NewClientConnectionRequestPDU([]byte(cookie), x.requestedProtocol)
	message.ProtocolNeg.Type = TYPE_RDP_NEG_REQ
	message.ProtocolNeg.Result = uint32(x.requestedProtocol)

	glog.Debug("x224 sendConnectionRequest", glog.Hex(message.Serialize()))
	if _, err := x.transport.Write(message.Serialize()); err != nil {
		return fmt.Errorf("x224: send connection request: %w", err)
	}

	select {
	case err := <-result:
		return err
	case err := <-errCh:
		return fmt.Errorf("x224: transport closed during handshake: %w", err)
	case <-ctx.Done():
		_ = x.Close()
		return fmt.Errorf("x224: connect timed out: %w", ctx.Err())
	}
}

func (x *X224) signalConnect(err error) {
	if x.connectResult != nil {
		x.connectResult <- err
	}
}

func (x *X224) recvConnectionConfirm(s []byte) {
	glog.Debug("x224 recvConnectionConfirm ", glog.Hex(s))
	r := bytes.NewReader(s)
	ln, _ := core.ReadUInt8(r)
	if ln > 6 {
		message := &ServerConnectionConfirm{}
		if err := struc.Unpack(bytes.NewReader(s), message); err != nil {
			glog.Error("ReadServerConnectionConfirm err", err)
			x.signalConnect(fmt.Errorf("x224: read connection confirm: %w", err))
			return
		}
		glog.Debugf("message: %+v", *message.ProtocolNeg)
		if message.ProtocolNeg.Type == TYPE_RDP_NEG_FAILURE {
			x.signalConnect(&NegotiationFailure{Code: message.ProtocolNeg.Result})
			_ = x.Close()
			return
		}

		if message.ProtocolNeg.Type == TYPE_RDP_NEG_RSP {
			glog.Info("TYPE_RDP_NEG_RSP")
			x.selectedProtocol = message.ProtocolNeg.Result
		}
	} else {
		x.selectedProtocol = PROTOCOL_RDP
	}

	if x.selectedProtocol == PROTOCOL_HYBRID_EX || x.selectedProtocol == PROTOCOL_RDSTLS || x.selectedProtocol == PROTOCOL_RDSAAD {
		x.signalConnect(fmt.Errorf("x224: unsupported security protocol 0x%x (only Standard RDP, TLS and NLA are supported)", x.selectedProtocol))
		_ = x.Close()
		return
	}

	// From here on, regular data PDUs are handed to recvData.
	x.transport.On("data", x.recvData)

	// The transport holds its read loop back from construction, because what
	// happens next decides what that loop should be reading: TLS, or the
	// CredSSP exchange over it, or the plain connection. This is the one place
	// that knows which, so it is the one place that starts it again. Every path
	// below either resumes or returns an error that closes the connection; a
	// path that does neither would leave the session silent for ever.
	tp, _ := x.transport.(*tpkt.TPKT)

	switch x.selectedProtocol {
	case PROTOCOL_RDP:
		glog.Info("*** RDP security selected ***")
		// No handshake at all for standard RDP security, so the connection the
		// loop was reading is already the final one.
	case PROTOCOL_SSL:
		glog.Info("*** SSL security selected ***")
		if err := tp.StartTLS(); err != nil {
			glog.Error("start tls failed:", err)
			x.signalConnect(fmt.Errorf("x224: start TLS: %w", err))
			_ = x.Close()
			return
		}
	case PROTOCOL_HYBRID:
		glog.Info("*** NLA Security selected ***")
		if err := tp.StartNLA(); err != nil {
			glog.Error("start NLA failed:", err)
			x.signalConnect(fmt.Errorf("x224: start NLA: %w", err))
			_ = x.Close()
			return
		}
	default:
		x.signalConnect(fmt.Errorf("x224: unknown selected protocol 0x%x", x.selectedProtocol))
		_ = x.Close()
		return
	}

	if tp != nil {
		tp.ResumeRead()
	}

	x.Emit("connect", x.selectedProtocol)
	x.signalConnect(nil)
}

func (x *X224) recvData(s []byte) {
	glog.Trace("x224 recvData", glog.Hex(s), "emit data")
	// x224 data header takes 3 bytes
	if len(s) < 3 {
		glog.Warn("x224 recvData: short data PDU, dropping")
		return
	}
	x.Emit("data", s[3:])
}
