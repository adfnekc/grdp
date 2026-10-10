package tpkt

import (
	"bytes"
	"sync"
	"errors"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/nla"
)

// take idea from https://github.com/Madnikulin50/gordp

/**
 * Type of tpkt packet
 * Fastpath is use to shortcut RDP stack
 * @see http://msdn.microsoft.com/en-us/library/cc240621.aspx
 * @see http://msdn.microsoft.com/en-us/library/cc240589.aspx
 */
const (
	FASTPATH_ACTION_FASTPATH = 0x0
	FASTPATH_ACTION_X224     = 0x3
)

/**
 * TPKT layer of rdp stack
 */
type TPKT struct {
	emission.Emitter
	Conn             *core.SocketLayer
	ntlm             *nla.NTLMv2
	secFlag          byte
	lastShortLength  int
	fastPathListener core.FastPathListener
	ntlmSec          *nla.NTLMv2Security
	publicKey        []byte
	strictPubKeyAuth bool
	// credsspVersion is the version both sides agreed on, and clientNonce is
	// the value the version 5 and later binding hashes are computed over.
	credsspVersion int
	clientNonce    []byte

	// readMu guards readPaused, which one goroutine sets and the read loop
	// reads.
	readMu sync.Mutex
	// readPaused stops the read loop from arming its next read. It is set
	// before the first read is armed, and cleared by whoever has finished
	// putting the transport into its final form.
	readPaused bool
	// readArmed is whether a read has been started and not yet completed.
	// It is what makes arming idempotent. Without it, resuming the loop while a
	// delivery is in progress arms a second read as well as the one the
	// delivery arms when it returns, and two readers split the stream between
	// them: the session desynchronises and every later PDU is read from the
	// wrong offset, which looks like a malformed server rather than two readers.
	readArmed bool

	// awaitingVerdict is set once the credentials have been presented and the
	// server has not yet shown that it accepted them. A failure in that window
	// is about the credentials; a failure before it is about TLS, and one after
	// it is about the session. The window closes on the first RDP PDU, which is
	// the server's evidence that the logon succeeded.
	awaitingVerdict bool
}

func New(s *core.SocketLayer, ntlm *nla.NTLMv2) *TPKT {
	t := &TPKT{
		Emitter:    *emission.NewEmitter(),
		Conn:       s,
		secFlag:    0,
		ntlm:       ntlm,
		readPaused: true,
	}
	t.armReadForced(2, t.recvHeader)
	return t
}

// PauseRead stops the read loop from arming its next read.
//
// It exists for the handshake. The X.224 reply has to be read before TLS exists,
// so the loop must be running to reach the security exchange; after that reply
// the transport is about to be wrapped in TLS or replaced by CredSSP, and while
// that happens the loop must not be holding the socket. It used to be, which
// meant a failing handshake was reported by the read loop as a plain transport
// error while StartTLS and StartNLA were still inside their own reads, and the
// typed errors those two produce never reached a caller.
//
// The loop is paused from construction, so it is already stopped when the reply
// is delivered: Emit is synchronous, so the handler that decides the security
// protocol runs before the loop would arm again.
func (t *TPKT) PauseRead() {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	t.readPaused = true
}

// ResumeRead arms the next read, on whichever transport the connection now has.
// Every path out of the security exchange must reach it, or the connection stops
// making progress. It is safe to call while a delivery is in progress: that
// delivery will find the read already armed and leave it alone.
func (t *TPKT) ResumeRead() {
	t.readMu.Lock()
	wasPaused := t.readPaused
	t.readPaused = false
	t.readMu.Unlock()
	if wasPaused {
		glog.Debug("tpkt: resuming reads")
	}
	t.armRead(2, t.recvHeader)
}

// armRead starts the next read unless one is already outstanding or the loop is
// held back.
func (t *TPKT) armRead(n int, f core.ReadBytesComplete) {
	t.arm(n, f, false)
}

// armReadForced starts a read that is part of getting to the end of the message
// already in progress, or the first read of all. Neither can wait for the loop:
// the X.224 reply is what decides whether there will be a handshake, and a TPKT
// header arrives over two or three reads which must all happen before anything
// is delivered to decide anything.
//
// Only the read that starts a NEW message is held back, which is the one in
// recvData and its fast path equivalent. Getting this the wrong way round is
// silent: the connection stops after the first two bytes, which is what it did
// when the pause covered every read.
func (t *TPKT) armReadForced(n int, f core.ReadBytesComplete) {
	t.arm(n, f, true)
}

// arm starts a read, unless one is outstanding or, unless forced, the loop is
// held back. It clears the outstanding mark when the read completes, and every
// read in this package goes through it, which is what makes that mark mean
// something. It is what stops a resume during a delivery from arming a second
// reader: two readers split the stream between them and the session desynchronises,
// which looks like a malformed server rather than a client with two readers.
func (t *TPKT) arm(n int, f core.ReadBytesComplete, force bool) {
	t.readMu.Lock()
	if t.readArmed || (!force && t.readPaused) {
		t.readMu.Unlock()
		return
	}
	t.readArmed = true
	t.readMu.Unlock()

	core.StartReadBytes(n, t.Conn, func(b []byte, err error) {
		t.readMu.Lock()
		t.readArmed = false
		t.readMu.Unlock()
		f(b, err)
	})
}

// StartTLS upgrades the connection. A failure here is typed, because this is the
// only place that knows nothing has been sent yet: the two paths that call it,
// plain TLS and the TLS half of NLA, both need a caller to be able to tell a
// certificate problem from an account problem.
func (t *TPKT) StartTLS() error {
	if err := t.Conn.StartTLS(); err != nil {
		return &TLSError{Err: err}
	}
	return nil
}

// SetStrictPubKeyAuth controls whether a mismatch in the CredSSP server public
// key confirmation aborts the handshake. It is non-strict by default because
// this check has not yet been validated against production Windows servers.
func (t *TPKT) SetStrictPubKeyAuth(strict bool) {
	t.strictPubKeyAuth = strict
}

// TLSError is returned when the TLS handshake fails, before anything has been
// authenticated.
//
// It is separate from the two below because it is about the transport and the
// certificate: a wrong password cannot cause it, and a caller that sees it should
// be looking at the server's certificate or at the ciphers, not at credentials.
type TLSError struct {
	Err error
}

func (e *TLSError) Error() string { return "nla: the TLS handshake failed: " + e.Err.Error() }

// Unwrap exposes the underlying error.
func (e *TLSError) Unwrap() error { return e.Err }

// CredSSPError is returned when the credentials have not been sent yet and the
// exchange itself has failed: a DER message that will not parse, a version that
// does not match, or the server closing the connection while it is being asked
// to negotiate.
//
// Nothing here has been proven wrong about the credentials, because at this
// point they have not been offered. That is what separates it from
// CredentialsError below, and it is the reason the two are different types: one
// means fix the credentials, the other means look at the CredSSP exchange.
type CredSSPError struct {
	Err error
}

func (e *CredSSPError) Error() string {
	return "nla: the CredSSP exchange failed before the credentials were sent: " + e.Err.Error()
}

// Unwrap exposes the underlying error.
func (e *CredSSPError) Unwrap() error { return e.Err }

// CredentialsError is returned when the credentials have been sent and the server
// has not answered.
//
// This is the shape a refused logon takes: Windows does not reply with an NTLM
// status, it drops the TLS connection, so the alert arrives a moment later on the
// ordinary read path and on its own reads like a negotiation fault. Whether the
// password was actually wrong cannot be known from here, because a server that
// vanished mid-exchange is indistinguishable from one that refused, which is why
// the name is about the exchange rather than about the password.
type CredentialsError struct {
	Err error
}

func (e *CredentialsError) Error() string {
	return "nla: the server did not answer the credentials: " + e.Err.Error()
}

// Unwrap exposes the underlying error, which for a refused logon is usually the
// TLS alert the server sent as it tore the connection down.
func (e *CredentialsError) Unwrap() error { return e.Err }

func (t *TPKT) StartNLA() error {
	if err := t.StartTLS(); err != nil {
		glog.Info("start tls failed", err)
		// Already a TLSError: StartTLS types it, and nothing has been sent yet.
		return err
	}

	req := nla.EncodeDERTRequest([]nla.Message{t.ntlm.GetNegotiateMessage()}, nil, nil)
	if _, err := t.Conn.Write(req); err != nil {
		return &CredSSPError{Err: fmt.Errorf("send NegotiateMessage: %w", err)}
	}

	data, err := t.readCredSSP()
	if err != nil {
		return &CredSSPError{Err: fmt.Errorf("read challenge: %w", err)}
	}
	if err := t.recvChallenge(data); err != nil {
		return &CredSSPError{Err: err}
	}
	// The challenge is answered and the credentials are on their way. Windows
	// does not answer a bad logon with an NTLM status: it drops the TLS
	// connection, and the alert arrives on the ordinary read path a moment
	// later, outside this function. The window is therefore left open, and
	// classifyAuthError closes it.
	t.awaitingVerdict = true
	return nil
}

// classifyAuthError marks a failure that happened while the server was deciding
// whether to accept the credentials.
//
// This is the distinction a caller cannot make from the message: a refused logon
// against an NLA server reaches them as a TLS alert, which on its own reads like
// a certificate problem and sends an operator to the wrong place. The stage is
// what says otherwise, and the stage is known here and not in the caller.
func (t *TPKT) classifyAuthError(err error, what string) error {
	if err == nil || !t.awaitingVerdict {
		return err
	}
	t.awaitingVerdict = false
	return &CredentialsError{Err: fmt.Errorf("%s: %w", what, err)}
}

// credSSPMaxMessage bounds a single CredSSP TSRequest to avoid unbounded memory
// growth on malformed input.
const credSSPMaxMessage = 1 << 20

// readCredSSP reads one complete DER-encoded TSRequest from the TLS stream.
// CredSSP messages are not TPKT-framed, so the ASN.1 SEQUENCE length is used to
// delimit them. A single TLS Read may deliver a partial or a coalesced record.
func (t *TPKT) readCredSSP() ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 8192)
	for {
		n, err := t.Conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > credSSPMaxMessage {
				return nil, errors.New("nla: CredSSP message too large")
			}
			if total, ok := derSequenceLen(buf); ok && len(buf) >= total {
				return buf[:total], nil
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

// derSequenceLen returns the total encoded length of a DER SEQUENCE (tag +
// length + value) if the buffer already contains the complete length field.
func derSequenceLen(b []byte) (int, bool) {
	if len(b) < 2 || b[0] != 0x30 {
		return 0, false
	}
	l := b[1]
	if l < 0x80 {
		return int(l) + 2, true
	}
	n := int(l & 0x7f)
	if n == 0 || n > 4 || len(b) < 2+n {
		return 0, false
	}
	total := 0
	for i := 0; i < n; i++ {
		total = total<<8 | int(b[2+i])
	}
	return total + 2 + n, true
}

func (t *TPKT) recvChallenge(data []byte) error {
	glog.Trace("recvChallenge", glog.Hex(data))
	tsreq, err := nla.DecodeDERTRequest(data)
	if err != nil {
		return fmt.Errorf("nla: decode challenge: %w", err)
	}
	glog.Debugf("tsreq:%+v", tsreq)
	if len(tsreq.NegoTokens) == 0 {
		return errors.New("nla: server challenge contains no NTLM token")
	}

	// MS-CSSP 3.1.5 says the binding hash covers the certificate's
	// SubjectPublicKey, which is the DER RSAPublicKey inside the
	// SubjectPublicKeyInfo, not the SubjectPublicKeyInfo wrapper itself.
	spki, err := t.Conn.TlsPubKey()
	if err != nil {
		return fmt.Errorf("nla: get server public key: %w", err)
	}
	t.publicKey = spki

	// Effective version is the lower of what we support and what the server
	// offered. From version 5 the raw key is replaced by a direction specific
	// SHA-256 binding hash; before that the raw key is sealed.
	version := tsreq.Version
	if version > nla.VersionNonce {
		version = nla.VersionNonce
	}
	t.credsspVersion = version

	authMsg, ntlmSec := t.ntlm.GetAuthenticateMessage(tsreq.NegoTokens[0].Data)
	if authMsg == nil || ntlmSec == nil {
		return errors.New("nla: failed to build NTLM authenticate message")
	}
	t.ntlmSec = ntlmSec

	binding := t.publicKey
	if version >= nla.VersionSha256 {
		t.clientNonce = core.Random(nla.NonceLen)
		binding = nla.ClientToServerHash(t.clientNonce, t.publicKey)
	} else {
		t.clientNonce = nil
	}

	encryptPubkey := ntlmSec.GssEncrypt(binding)
	req := nla.EncodeDERTRequestVersion(version, []nla.Message{authMsg}, nil, encryptPubkey, t.clientNonce)
	if _, err := t.Conn.Write(req); err != nil {
		return fmt.Errorf("nla: send AuthenticateMessage: %w", err)
	}

	data, err = t.readCredSSP()
	if err != nil {
		return fmt.Errorf("nla: read public key confirmation: %w", err)
	}
	return t.recvPubKeyInc(data)
}

func (t *TPKT) recvPubKeyInc(data []byte) error {
	glog.Trace("recvPubKeyInc", glog.Hex(data))
	tsreq, err := nla.DecodeDERTRequest(data)
	if err != nil {
		return fmt.Errorf("nla: decode public key confirmation: %w", err)
	}

	// Verify the server's public key confirmation (MS-CSSP 3.1.5). From
	// version 5 the server returns the server-to-client binding hash rather
	// than the client's own bytes, and a version 5 server proves it holds the
	// TLS channel by producing it.
	if len(tsreq.PubKeyAuth) > 0 && t.ntlmSec != nil {
		// Both versions work off the same SubjectPublicKeyInfo. A pre-version-5
		// server replies with that key with its first byte incremented, not
		// with the key unchanged.
		want := t.publicKey
		if t.credsspVersion >= nla.VersionSha256 {
			want = nla.ServerToClientHash(t.clientNonce, t.publicKey)
		} else {
			want = nla.LegacyExpectedServerResponse(t.publicKey)
		}
		got := t.ntlmSec.GssDecrypt(tsreq.PubKeyAuth)
		if !bytes.Equal(got, want) {
			if t.strictPubKeyAuth {
				return errors.New("nla: server public key confirmation mismatch")
			}
			glog.Warn("nla: server public key confirmation mismatch (continuing; enable strict mode to enforce)")
		}
	}

	domain, username, password := t.ntlm.GetEncodedCredentials()
	credentials := nla.EncodeDERTCredentials(domain, username, password)
	authInfo := t.ntlmSec.GssEncrypt(credentials)
	req := nla.EncodeDERTRequestVersion(t.credsspVersion, nil, authInfo, nil, nil)
	if _, err := t.Conn.Write(req); err != nil {
		return fmt.Errorf("nla: send credentials: %w", err)
	}

	return nil
}

func (t *TPKT) Read(b []byte) (n int, err error) {
	return t.Conn.Read(b)
}

func (t *TPKT) Write(data []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	core.WriteUInt8(FASTPATH_ACTION_X224, buff)
	core.WriteUInt8(0, buff)
	core.WriteUInt16BE(uint16(len(data)+4), buff)
	buff.Write(data)
	glog.Trace("tpkt Write", glog.Hex(buff.Bytes()))
	return t.Conn.Write(buff.Bytes())
}

func (t *TPKT) Close() error {
	return t.Conn.Close()
}

func (t *TPKT) SetFastPathListener(f core.FastPathListener) {
	t.fastPathListener = f
}

func (t *TPKT) SendFastPath(secFlag byte, data []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	core.WriteUInt8(FASTPATH_ACTION_FASTPATH|((secFlag&0x3)<<6), buff)
	core.WriteUInt16BE(uint16(len(data)+3)|0x8000, buff)
	buff.Write(data)
	glog.Trace("TPTK SendFastPath", glog.Hex(buff.Bytes()))
	return t.Conn.Write(buff.Bytes())
}

// SendFastPathInput writes a client-to-server fast-path input PDU. Unlike
// output PDUs, the first header byte carries the event count in bits 2-5:
//
//	byte0 = action (2 bits) | numEvents << 2 | flags << 6
//	byte1..2 = length (includes this 3 byte header)
//	events
//
// See MS-RDPBCGR 2.2.8.1.2.1.
func (t *TPKT) SendFastPathInput(numEvents byte, data []byte) (n int, err error) {
	buff := &bytes.Buffer{}
	core.WriteUInt8(FASTPATH_ACTION_FASTPATH|((numEvents&0x0F)<<2), buff)
	core.WriteUInt16BE(uint16(len(data)+3)|0x8000, buff)
	buff.Write(data)
	glog.Trace("TPTK SendFastPathInput", glog.Hex(buff.Bytes()))
	return t.Conn.Write(buff.Bytes())
}

func (t *TPKT) recvHeader(s []byte, err error) {
	glog.Trace("tpkt recvHeader", glog.Hex(s), err)
	if err != nil {
		t.emitReadError(t.classifyAuthError(err, "read header"))
		return
	}
	// A valid header is the server carrying on with the session, which is what
	// says the credentials were accepted.
	if t.awaitingVerdict {
		glog.Debug("tpkt: the server carried on, so the logon was accepted")
		t.awaitingVerdict = false
	}

	r := bytes.NewReader(s)
	version, _ := core.ReadUInt8(r)
	if version == FASTPATH_ACTION_X224 {
		glog.Debug("tptk recvHeader FASTPATH_ACTION_X224, wait for recvExtendedHeader")
		t.armReadForced(2, t.recvExtendedHeader)
		return
	}
	t.secFlag = (version >> 6) & 0x3
	length, _ := core.ReadUInt8(r)
	t.lastShortLength = int(length)
	if t.lastShortLength&0x80 != 0 {
		t.armReadForced(1, t.recvExtendedFastPathHeader)
		return
	}
	if t.lastShortLength < 2 {
		t.emitReadError(fmt.Errorf("tpkt: invalid short length %d", t.lastShortLength))
		return
	}
	t.armReadForced(t.lastShortLength-2, t.recvFastPath)
}

// emitReadError surfaces a terminal read error to the layer above. io.EOF is
// reported as "close" (the peer hung up); anything else as "error".
func (t *TPKT) emitReadError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, io.EOF) {
		glog.Debug("tpkt: connection closed by peer")
		t.Emit("close")
		return
	}
	glog.Debug("tpkt: read error:", err)
	t.Emit("error", err)
}

func (t *TPKT) recvExtendedHeader(s []byte, err error) {
	glog.Trace("tpkt recvExtendedHeader", glog.Hex(s), err)
	if err != nil {
		t.emitReadError(err)
		return
	}
	r := bytes.NewReader(s)
	size, _ := core.ReadUint16BE(r)
	if size < 4 {
		t.emitReadError(fmt.Errorf("tpkt: invalid x224 packet size %d", size))
		return
	}
	glog.Debug("tpkt wait recvData:", size)
	t.armReadForced(int(size-4), t.recvData)
}

func (t *TPKT) recvData(s []byte, err error) {
	glog.Trace("tpkt recvData", glog.Hex(s), err)
	if err != nil {
		t.emitReadError(err)
		return
	}
	t.Emit("data", s)
	// The transport may be about to change: the security exchange runs inside
	// this delivery, and it holds the loop back until it has decided what the
	// loop should be reading. armRead does the right thing either way.
	t.armRead(2, t.recvHeader)
}

func (t *TPKT) recvExtendedFastPathHeader(s []byte, err error) {
	glog.Trace("tpkt recvExtendedFastPathHeader", glog.Hex(s))
	if err != nil {
		t.emitReadError(err)
		return
	}
	r := bytes.NewReader(s)
	rightPart, err := core.ReadUInt8(r)
	if err != nil {
		t.emitReadError(err)
		return
	}

	leftPart := t.lastShortLength & ^0x80
	packetSize := (leftPart << 8) + int(rightPart)
	if packetSize < 3 {
		t.emitReadError(fmt.Errorf("tpkt: invalid fastpath packet size %d", packetSize))
		return
	}
	t.armReadForced(packetSize-3, t.recvFastPath)
}

func (t *TPKT) recvFastPath(s []byte, err error) {
	glog.Trace("tpkt recvFastPath")
	if err != nil {
		t.emitReadError(err)
		return
	}

	if t.fastPathListener != nil {
		t.fastPathListener.RecvFastPath(t.secFlag, s)
	}
	t.armRead(2, t.recvHeader)
}
