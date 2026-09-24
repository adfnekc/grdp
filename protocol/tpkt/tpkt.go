package tpkt

import (
	"bytes"
	"encoding/hex"
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
}

func New(s *core.SocketLayer, ntlm *nla.NTLMv2) *TPKT {
	t := &TPKT{
		Emitter: *emission.NewEmitter(),
		Conn:    s,
		secFlag: 0,
		ntlm:    ntlm}
	core.StartReadBytes(2, s, t.recvHeader)
	return t
}

func (t *TPKT) StartTLS() error {
	return t.Conn.StartTLS()
}

// SetStrictPubKeyAuth controls whether a mismatch in the CredSSP server public
// key confirmation aborts the handshake. It is non-strict by default because
// this check has not yet been validated against production Windows servers.
func (t *TPKT) SetStrictPubKeyAuth(strict bool) {
	t.strictPubKeyAuth = strict
}

func (t *TPKT) StartNLA() error {
	if err := t.StartTLS(); err != nil {
		glog.Info("start tls failed", err)
		return fmt.Errorf("nla: start TLS: %w", err)
	}

	req := nla.EncodeDERTRequest([]nla.Message{t.ntlm.GetNegotiateMessage()}, nil, nil)
	if _, err := t.Conn.Write(req); err != nil {
		return fmt.Errorf("nla: send NegotiateMessage: %w", err)
	}

	data, err := t.readCredSSP()
	if err != nil {
		return fmt.Errorf("nla: read challenge: %w", err)
	}
	return t.recvChallenge(data)
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
	glog.Trace("recvChallenge", hex.EncodeToString(data))
	tsreq, err := nla.DecodeDERTRequest(data)
	if err != nil {
		return fmt.Errorf("nla: decode challenge: %w", err)
	}
	glog.Debugf("tsreq:%+v", tsreq)
	if len(tsreq.NegoTokens) == 0 {
		return errors.New("nla: server challenge contains no NTLM token")
	}

	// The binding hash is computed over the certificate's
	// SubjectPublicKeyInfo, so keep those exact bytes for version 5 and up.
	spki, err := t.Conn.TlsPubKeySPKI()
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
	if version < nla.VersionSha256 {
		legacy, lerr := t.Conn.TlsPubKey()
		if lerr != nil {
			return fmt.Errorf("nla: get server public key: %w", lerr)
		}
		t.publicKey = legacy
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
	glog.Trace("recvPubKeyInc", hex.EncodeToString(data))
	tsreq, err := nla.DecodeDERTRequest(data)
	if err != nil {
		return fmt.Errorf("nla: decode public key confirmation: %w", err)
	}

	// Verify the server's public key confirmation (MS-CSSP 3.1.5). From
	// version 5 the server returns the server-to-client binding hash rather
	// than the client's own bytes, and a version 5 server proves it holds the
	// TLS channel by producing it.
	if len(tsreq.PubKeyAuth) > 0 && t.ntlmSec != nil {
		want := t.publicKey
		if t.credsspVersion >= nla.VersionSha256 {
			want = nla.ServerToClientHash(t.clientNonce, t.publicKey)
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
	glog.Trace("tpkt Write", hex.EncodeToString(buff.Bytes()))
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
	glog.Trace("TPTK SendFastPath", hex.EncodeToString(buff.Bytes()))
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
	glog.Trace("TPTK SendFastPathInput", hex.EncodeToString(buff.Bytes()))
	return t.Conn.Write(buff.Bytes())
}

func (t *TPKT) recvHeader(s []byte, err error) {
	glog.Trace("tpkt recvHeader", hex.EncodeToString(s), err)
	if err != nil {
		t.emitReadError(err)
		return
	}

	r := bytes.NewReader(s)
	version, _ := core.ReadUInt8(r)
	if version == FASTPATH_ACTION_X224 {
		glog.Debug("tptk recvHeader FASTPATH_ACTION_X224, wait for recvExtendedHeader")
		core.StartReadBytes(2, t.Conn, t.recvExtendedHeader)
		return
	}
	t.secFlag = (version >> 6) & 0x3
	length, _ := core.ReadUInt8(r)
	t.lastShortLength = int(length)
	if t.lastShortLength&0x80 != 0 {
		core.StartReadBytes(1, t.Conn, t.recvExtendedFastPathHeader)
		return
	}
	if t.lastShortLength < 2 {
		t.emitReadError(fmt.Errorf("tpkt: invalid short length %d", t.lastShortLength))
		return
	}
	core.StartReadBytes(t.lastShortLength-2, t.Conn, t.recvFastPath)
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
	glog.Trace("tpkt recvExtendedHeader", hex.EncodeToString(s), err)
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
	core.StartReadBytes(int(size-4), t.Conn, t.recvData)
}

func (t *TPKT) recvData(s []byte, err error) {
	glog.Trace("tpkt recvData", hex.EncodeToString(s), err)
	if err != nil {
		t.emitReadError(err)
		return
	}
	t.Emit("data", s)
	core.StartReadBytes(2, t.Conn, t.recvHeader)
}

func (t *TPKT) recvExtendedFastPathHeader(s []byte, err error) {
	glog.Trace("tpkt recvExtendedFastPathHeader", hex.EncodeToString(s))
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
	core.StartReadBytes(packetSize-3, t.Conn, t.recvFastPath)
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
	core.StartReadBytes(2, t.Conn, t.recvHeader)
}
