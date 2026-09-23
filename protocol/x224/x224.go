package x224

import (
	"bytes"
	"context"
	"encoding/hex"
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

/**
 * Message type present in X224 packet header
 */
type MessageType byte

const (
	TPDU_CONNECTION_REQUEST MessageType = 0xE0
	TPDU_CONNECTION_CONFIRM             = 0xD0
	TPDU_DISCONNECT_REQUEST             = 0x80
	TPDU_DATA                           = 0xF0
	TPDU_ERROR                          = 0x70
)

/**
 * Type of negotiation present in negotiation packet
 */
type NegotiationType byte

const (
	TYPE_RDP_NEG_REQ     NegotiationType = 0x01
	TYPE_RDP_NEG_RSP                     = 0x02
	TYPE_RDP_NEG_FAILURE                 = 0x03
)

/**
 * Protocols available for x224 layer
 */

const (
	PROTOCOL_RDP       uint32 = 0x00000000
	PROTOCOL_SSL              = 0x00000001
	PROTOCOL_HYBRID           = 0x00000002
	PROTOCOL_RDSTLS           = 0x00000004
	PROTOCOL_HYBRID_EX        = 0x00000008
	PROTOCOL_RDSAAD           = 0x00000010
)

/**
 * Use to negotiate security layer of RDP stack
 * In node-rdpjs only ssl is available
 * @param opt {object} component type options
 * @see request -> http://msdn.microsoft.com/en-us/library/cc240500.aspx
 * @see response -> http://msdn.microsoft.com/en-us/library/cc240506.aspx
 * @see failure ->http://msdn.microsoft.com/en-us/library/cc240507.aspx
 */
type Negotiation struct {
	Type   NegotiationType `struc:"byte"`
	Flag   uint8           `struc:"uint8"`
	Length uint16          `struc:"little"`
	Result uint32          `struc:"little"`
}

func NewNegotiation() *Negotiation {
	return &Negotiation{0, 0, 0x0008 /*constant*/, PROTOCOL_RDP}
}

type failureCode int

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

/**
 * X224 client connection request
 * @param opt {object} component type options
 * @see	http://msdn.microsoft.com/en-us/library/cc240470.aspx
 */
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

/**
 * X224 Server connection confirm
 * @param opt {object} component type options
 * @see	http://msdn.microsoft.com/en-us/library/cc240506.aspx
 */
type ServerConnectionConfirm struct {
	Len         uint8       `struc:"byte"`
	Code        MessageType `struc:"byte"`
	Padding1    uint16      `struc:"little"`
	Padding2    uint16      `struc:"little"`
	Padding3    uint8       `struc:"byte"`
	ProtocolNeg *Negotiation
}

/**
 * Header of each data message from x224 layer
 * @returns {type.Component}
 */
type DataHeader struct {
	Header      uint8       `struc:"little"`
	MessageType MessageType `struc:"uint8"`
	Separator   uint8       `struc:"little"`
}

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

func (e *NegotiationFailure) Error() string {
	msg := map[uint32]string{
		SSL_REQUIRED_BY_SERVER:                "the server requires Enhanced RDP Security (TLS or CredSSP)",
		SSL_NOT_ALLOWED_BY_SERVER:             "the server only supports Standard RDP Security",
		SSL_CERT_NOT_ON_SERVER:                "the server has no valid authentication certificate",
		INCONSISTENT_FLAGS:                    "inconsistent requested security protocols",
		HYBRID_REQUIRED_BY_SERVER:             "the server requires CredSSP (NLA)",
		SSL_WITH_USER_AUTH_REQUIRED_BY_SERVER: "the server requires TLS with client authentication",
	}[
		e.Code]
	if msg == "" {
		msg = "unknown failure code"
	}
	return fmt.Sprintf("x224: RDP security negotiation failed (0x%08x): %s", e.Code, msg)
}

/**
 * Common X224 Automata
 * @param presentation {Layer} presentation layer
 */
type X224 struct {
	emission.Emitter
	transport         core.Transport
	requestedProtocol uint32
	selectedProtocol  uint32
	dataHeader        *DataHeader
	connectTimeout    time.Duration
	connectResult     chan error
}

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

func (x *X224) Read(b []byte) (n int, err error) {
	n, err = x.transport.Read(b)
	return n, err
}

func (x *X224) Write(b []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	err = struc.Pack(buff, x.dataHeader)
	if err != nil {
		return 0, err
	}
	buff.Write(b)

	glog.Trace("x224 write:", hex.EncodeToString(buff.Bytes()))
	return x.transport.Write(buff.Bytes())
}

func (x *X224) Close() error {
	return x.transport.Close()
}

func (x *X224) SetRequestedProtocol(p uint32) {
	x.requestedProtocol = p
}

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

	glog.Debug("x224 sendConnectionRequest", hex.EncodeToString(message.Serialize()))
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
	glog.Debug("x224 recvConnectionConfirm ", hex.EncodeToString(s))
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

	switch x.selectedProtocol {
	case PROTOCOL_RDP:
		glog.Info("*** RDP security selected ***")
	case PROTOCOL_SSL:
		glog.Info("*** SSL security selected ***")
		if err := x.transport.(*tpkt.TPKT).StartTLS(); err != nil {
			glog.Error("start tls failed:", err)
			x.signalConnect(fmt.Errorf("x224: start TLS: %w", err))
			return
		}
	case PROTOCOL_HYBRID:
		glog.Info("*** NLA Security selected ***")
		if err := x.transport.(*tpkt.TPKT).StartNLA(); err != nil {
			glog.Error("start NLA failed:", err)
			x.signalConnect(fmt.Errorf("x224: start NLA: %w", err))
			return
		}
	default:
		x.signalConnect(fmt.Errorf("x224: unknown selected protocol 0x%x", x.selectedProtocol))
		return
	}

	x.Emit("connect", x.selectedProtocol)
	x.signalConnect(nil)
}

func (x *X224) recvData(s []byte) {
	glog.Trace("x224 recvData", hex.EncodeToString(s), "emit data")
	// x224 data header takes 3 bytes
	if len(s) < 3 {
		glog.Warn("x224 recvData: short data PDU, dropping")
		return
	}
	x.Emit("data", s[3:])
}
