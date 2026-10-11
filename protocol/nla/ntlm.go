package nla

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"time"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/glog"
	"github.com/lunixbochs/struc"
)

// The operating system version a client claims in its NTLM version field, and the
// NTLM revision with it. These are the values Windows itself reports: version 6.0
// build 6002, which is what other clients send, and revision 15, which is the one
// that came with Windows 2003 and is what the field means when interpreted.
const (
	WINDOWS_MINOR_VERSION_0 = 0x00
	WINDOWS_MINOR_VERSION_1 = 0x01
	WINDOWS_MINOR_VERSION_2 = 0x02
	WINDOWS_MINOR_VERSION_3 = 0x03

	WINDOWS_MAJOR_VERSION_5 = 0x05
	WINDOWS_MAJOR_VERSION_6 = 0x06
	NTLMSSP_REVISION_W2K3   = 0x0F
)

// The AV pair identifiers in the target info a server sends with its challenge.
// MsvAvTimestamp is the one that matters to a client: its presence is what says
// the server wants a MIC, and its value is the time the response is to claim.
//
// MsvChannelBindings is defined and never used here, which is worth knowing: the
// extended protection this would carry is not implemented.
const (
	MsvAvEOL             = 0x0000
	MsvAvNbComputerName  = 0x0001
	MsvAvNbDomainName    = 0x0002
	MsvAvDnsComputerName = 0x0003
	MsvAvDnsDomainName   = 0x0004
	MsvAvDnsTreeName     = 0x0005
	MsvAvFlags           = 0x0006
	MsvAvTimestamp       = 0x0007
	MsvAvSingleHost      = 0x0008
	MsvAvTargetName      = 0x0009
	MsvChannelBindings   = 0x000A
)

// AVPair is one AV pair in the target info: an identifier, a length, and the
// value. The target info is a sequence of these, terminated by MsvAvEOL.
type AVPair struct {
	Id    uint16 `struc:"little"`
	Len   uint16 `struc:"little,sizeof=Value"`
	Value []byte `struc:"little"`
}

// The NTLM negotiate flags. They are a bitmask and both sides set and read them,
// so the ones a client sends decide what the server is allowed to do with the
// session: NEGOTIATE_SIGN and NEGOTIATE_SEAL are what make the CredSSP messages
// signed and encrypted at all, and NEGOTIATE_KEY_EXCH is what lets the client
// choose the session key instead of deriving it.
//
// Several are defined and never set by this client, including the LM key, the
// datagram and non-NT session key flags and the target type flags, which a server
// sets rather than a client.
const (
	NTLMSSP_NEGOTIATE_56                       = 0x80000000
	NTLMSSP_NEGOTIATE_KEY_EXCH                 = 0x40000000
	NTLMSSP_NEGOTIATE_128                      = 0x20000000
	NTLMSSP_NEGOTIATE_VERSION                  = 0x02000000
	NTLMSSP_NEGOTIATE_TARGET_INFO              = 0x00800000
	NTLMSSP_REQUEST_NON_NT_SESSION_KEY         = 0x00400000
	NTLMSSP_NEGOTIATE_IDENTIFY                 = 0x00100000
	NTLMSSP_NEGOTIATE_EXTENDED_SESSIONSECURITY = 0x00080000
	NTLMSSP_TARGET_TYPE_SERVER                 = 0x00020000
	NTLMSSP_TARGET_TYPE_DOMAIN                 = 0x00010000
	NTLMSSP_NEGOTIATE_ALWAYS_SIGN              = 0x00008000
	NTLMSSP_NEGOTIATE_OEM_WORKSTATION_SUPPLIED = 0x00002000
	NTLMSSP_NEGOTIATE_OEM_DOMAIN_SUPPLIED      = 0x00001000
	NTLMSSP_NEGOTIATE_NTLM                     = 0x00000200
	NTLMSSP_NEGOTIATE_LM_KEY                   = 0x00000080
	NTLMSSP_NEGOTIATE_DATAGRAM                 = 0x00000040
	NTLMSSP_NEGOTIATE_SEAL                     = 0x00000020
	NTLMSSP_NEGOTIATE_SIGN                     = 0x00000010
	NTLMSSP_REQUEST_TARGET                     = 0x00000004
	NTLM_NEGOTIATE_OEM                         = 0x00000002
	NTLMSSP_NEGOTIATE_UNICODE                  = 0x00000001
)

// NVersion is the version structure NTLM messages carry when the negotiate flags
// ask for it. It describes the operating system the sender claims to be, and is
// informational: nothing in the exchange depends on what it says.
type NVersion struct {
	ProductMajorVersion uint8   `struc:"little"`
	ProductMinorVersion uint8   `struc:"little"`
	ProductBuild        uint16  `struc:"little"`
	Reserved            [3]byte `struc:"little"`
	NTLMRevisionCurrent uint8   `struc:"little"`
}

// NewNVersion returns the version this client claims: Windows 6.0 build 6002 and
// NTLM revision 15, which is what other clients report.
func NewNVersion() NVersion {
	return NVersion{
		ProductMajorVersion: WINDOWS_MAJOR_VERSION_6,
		ProductMinorVersion: WINDOWS_MINOR_VERSION_0,
		ProductBuild:        6002,
		NTLMRevisionCurrent: NTLMSSP_REVISION_W2K3,
	}
}

// Message is an NTLM message that can be put on the wire. The three message types
// implement it, and it is what MIC takes, since a MIC is computed over all three.
type Message interface {
	Serialize() []byte
}

// NegotiateMessage is the first NTLM message, which the client sends to open the
// exchange and which carries the flags it is willing to use.
//
// The offsets and lengths in it describe payload that follows the fixed fields,
// which for this client is empty: the domain and workstation names are not sent,
// so their lengths are zero and the offsets point past the end.
type NegotiateMessage struct {
	Signature               [8]byte  `struc:"little"`
	MessageType             uint32   `struc:"little"`
	NegotiateFlags          uint32   `struc:"little"`
	DomainNameLen           uint16   `struc:"little"`
	DomainNameMaxLen        uint16   `struc:"little"`
	DomainNameBufferOffset  uint32   `struc:"little"`
	WorkstationLen          uint16   `struc:"little"`
	WorkstationMaxLen       uint16   `struc:"little"`
	WorkstationBufferOffset uint32   `struc:"little"`
	Version                 NVersion `struc:"little"`
	Payload                 [32]byte `struc:"skip"`
}

// NewNegotiateMessage returns a negotiate message with the signature and message
// type set and the flags left at zero, for the caller to fill in.
func NewNegotiateMessage() *NegotiateMessage {
	return &NegotiateMessage{
		Signature:   [8]byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0x00},
		MessageType: 0x00000001,
	}
}

// Serialize returns the message as it goes on the wire. The version structure is
// written only when NTLMSSP_NEGOTIATE_VERSION is set, because the message is a
// different length without it.
func (m *NegotiateMessage) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteBytes(m.Signature[:], buff)
	core.WriteUInt32LE(m.MessageType, buff)
	core.WriteUInt32LE(m.NegotiateFlags, buff)
	core.WriteUInt16LE(m.DomainNameLen, buff)
	core.WriteUInt16LE(m.DomainNameMaxLen, buff)
	core.WriteUInt32LE(m.DomainNameBufferOffset, buff)
	core.WriteUInt16LE(m.WorkstationLen, buff)
	core.WriteUInt16LE(m.WorkstationMaxLen, buff)
	core.WriteUInt32LE(m.WorkstationBufferOffset, buff)
	// The Version field is only present when NTLMSSP_NEGOTIATE_VERSION is set
	// (MS-NLMP 2.2.1.1). Packing it unconditionally produced a non-conformant
	// 40-byte message.
	if (m.NegotiateFlags & NTLMSSP_NEGOTIATE_VERSION) != 0 {
		m.Version = NewNVersion()
		struc.Pack(buff, m.Version)
	}
	return buff.Bytes()
}

// ChallengeMessage is the server's challenge: its name, its flags, and the eight
// byte server challenge the response is computed over.
//
// The payload is not part of the structure on the wire, and TargetInfoLen says how
// much of it is the target info. Everything in the payload is addressed by an
// offset from the start of the message, which is why the accessors subtract the
// fixed length before indexing into it.
type ChallengeMessage struct {
	Signature              []byte   `struc:"[8]byte"`
	MessageType            uint32   `struc:"little"`
	TargetNameLen          uint16   `struc:"little"`
	TargetNameMaxLen       uint16   `struc:"little"`
	TargetNameBufferOffset uint32   `struc:"little"`
	NegotiateFlags         uint32   `struc:"little"`
	ServerChallenge        [8]byte  `struc:"little"`
	Reserved               [8]byte  `struc:"little"`
	TargetInfoLen          uint16   `struc:"little"`
	TargetInfoMaxLen       uint16   `struc:"little"`
	TargetInfoBufferOffset uint32   `struc:"little"`
	Version                NVersion `struc:"skip"`
	Payload                []byte   `struc:"skip"`
}

// Serialize returns the message as it goes on the wire. It is used by MIC, which
// hashes all three messages, so it has to produce the bytes as the server sent
// them: the version structure is written only when the flag asks for it, and the
// payload follows.
func (m *ChallengeMessage) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, m)
	if (m.NegotiateFlags & NTLMSSP_NEGOTIATE_VERSION) != 0 {
		struc.Pack(buff, m.Version)
	}
	buff.Write(m.Payload)
	return buff.Bytes()
}

// total len - payload len
// BaseLen returns the length of the message up to its payload, which is what an
// offset in the message is measured against.
func (m *ChallengeMessage) BaseLen() uint32 {
	return 56
}

func (m *ChallengeMessage) getTargetInfo() []byte {
	if m.TargetInfoLen == 0 {
		return make([]byte, 0)
	}
	offset := m.BaseLen()
	start := m.TargetInfoBufferOffset - offset
	return m.Payload[start : start+uint32(m.TargetInfoLen)]
}
func (m *ChallengeMessage) getTargetName() []byte {
	if m.TargetNameLen == 0 {
		return make([]byte, 0)
	}
	offset := m.BaseLen()
	start := m.TargetNameBufferOffset - offset
	return m.Payload[start : start+uint32(m.TargetNameLen)]
}
func (m *ChallengeMessage) getTargetInfoTimestamp(data []byte) []byte {
	r := bytes.NewReader(data)
	for r.Len() > 0 {
		avPair := &AVPair{}
		struc.Unpack(r, avPair)
		if avPair.Id == MsvAvTimestamp {
			return avPair.Value
		}

		if avPair.Id == MsvAvEOL {
			break
		}
	}
	return nil
}

// AuthenticateMessage is the client's third message: the challenge responses, the
// account's domain, user and workstation, the wrapped session key, and the MIC.
//
// Every variable length field is a length, a maximum length and an offset, and the
// offsets are absolute rather than relative to the payload, which is why
// NewAuthenticateMessage computes each one from the length before it rather than
// from the buffer it is building.
type AuthenticateMessage struct {
	Signature                          [8]byte
	MessageType                        uint32   `struc:"little"`
	LmChallengeResponseLen             uint16   `struc:"little"`
	LmChallengeResponseMaxLen          uint16   `struc:"little"`
	LmChallengeResponseBufferOffset    uint32   `struc:"little"`
	NtChallengeResponseLen             uint16   `struc:"little"`
	NtChallengeResponseMaxLen          uint16   `struc:"little"`
	NtChallengeResponseBufferOffset    uint32   `struc:"little"`
	DomainNameLen                      uint16   `struc:"little"`
	DomainNameMaxLen                   uint16   `struc:"little"`
	DomainNameBufferOffset             uint32   `struc:"little"`
	UserNameLen                        uint16   `struc:"little"`
	UserNameMaxLen                     uint16   `struc:"little"`
	UserNameBufferOffset               uint32   `struc:"little"`
	WorkstationLen                     uint16   `struc:"little"`
	WorkstationMaxLen                  uint16   `struc:"little"`
	WorkstationBufferOffset            uint32   `struc:"little"`
	EncryptedRandomSessionLen          uint16   `struc:"little"`
	EncryptedRandomSessionMaxLen       uint16   `struc:"little"`
	EncryptedRandomSessionBufferOffset uint32   `struc:"little"`
	NegotiateFlags                     uint32   `struc:"little"`
	Version                            NVersion `struc:"little"`
	MIC                                [16]byte `struc:"little"`
	Payload                            []byte   `struc:"skip"`
}

// BaseLen returns the length of the message up to its payload, which is where the
// first payload offset points.
func (m *AuthenticateMessage) BaseLen() uint32 {
	return 88
}

// NewAuthenticateMessage builds the third message, laying the variable length
// fields out in the payload in the order the offsets describe and computing each
// offset from the ones before it.
//
// The fields are written in one order and could be sent in another: the offsets are
// what say where each one is, and the server reads them rather than assuming. The
// version structure is written when the flags ask for it, and the MIC is left at
// zero here because it is computed over the message after it is built.
func NewAuthenticateMessage(negFlag uint32, domain, user, workstation []byte,
	lmchallResp, ntchallResp, enRandomSessKey []byte) *AuthenticateMessage {
	msg := &AuthenticateMessage{
		Signature:      [8]byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0x00},
		MessageType:    0x00000003,
		NegotiateFlags: negFlag,
	}
	payloadBuff := &bytes.Buffer{}

	msg.LmChallengeResponseLen = uint16(len(lmchallResp))
	msg.LmChallengeResponseMaxLen = msg.LmChallengeResponseLen
	msg.LmChallengeResponseBufferOffset = msg.BaseLen()
	payloadBuff.Write(lmchallResp)

	msg.NtChallengeResponseLen = uint16(len(ntchallResp))
	msg.NtChallengeResponseMaxLen = msg.NtChallengeResponseLen
	msg.NtChallengeResponseBufferOffset = msg.LmChallengeResponseBufferOffset + uint32(msg.LmChallengeResponseLen)
	payloadBuff.Write(ntchallResp)

	msg.DomainNameLen = uint16(len(domain))
	msg.DomainNameMaxLen = msg.DomainNameLen
	msg.DomainNameBufferOffset = msg.NtChallengeResponseBufferOffset + uint32(msg.NtChallengeResponseLen)
	payloadBuff.Write(domain)

	msg.UserNameLen = uint16(len(user))
	msg.UserNameMaxLen = msg.UserNameLen
	msg.UserNameBufferOffset = msg.DomainNameBufferOffset + uint32(msg.DomainNameLen)
	payloadBuff.Write(user)

	msg.WorkstationLen = uint16(len(workstation))
	msg.WorkstationMaxLen = msg.WorkstationLen
	msg.WorkstationBufferOffset = msg.UserNameBufferOffset + uint32(msg.UserNameLen)
	payloadBuff.Write(workstation)

	msg.EncryptedRandomSessionLen = uint16(len(enRandomSessKey))
	msg.EncryptedRandomSessionMaxLen = msg.EncryptedRandomSessionLen
	msg.EncryptedRandomSessionBufferOffset = msg.WorkstationBufferOffset + uint32(msg.WorkstationLen)
	payloadBuff.Write(enRandomSessKey)

	if (msg.NegotiateFlags & NTLMSSP_NEGOTIATE_VERSION) != 0 {
		msg.Version = NewNVersion()
	}
	msg.Payload = payloadBuff.Bytes()

	return msg
}

// Serialize returns the message as it goes on the wire: the fixed fields with the
// version structure the tags place inside them, then the payload. It is what MIC
// hashes, so the MIC field in the middle of the structure is the one that was
// computed, not a placeholder.
func (m *AuthenticateMessage) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, m)
	buff.Write(m.Payload)
	return buff.Bytes()
}

// NTLMv2 runs the NTLMv2 exchange for one set of credentials, which CredSSP uses
// to authenticate the connection.
//
// It holds the credentials and the keys derived from them, and the three messages
// as they are produced, because the MIC is computed over all three and the
// responses are computed over the challenge. The password is kept only to derive
// the two keys, which happens in NewNTLMv2; nothing derived from it is logged.
type NTLMv2 struct {
	domain              string
	user                string
	password            string
	respKeyNT           []byte
	respKeyLM           []byte
	negotiateMessage    *NegotiateMessage
	challengeMessage    *ChallengeMessage
	authenticateMessage *AuthenticateMessage
	enableUnicode       bool
}

// NewNTLMv2 returns an exchange for one account, with the two response keys
// derived from the password and the account name.
//
// The keys are derived here rather than when the challenge arrives, so the password
// is not needed after this returns. The derivation is the one NTLMv2 defines,
// which is also what LMOWFv2 returns, so the two keys are the same value.
func NewNTLMv2(domain, user, password string) *NTLMv2 {
	return &NTLMv2{
		domain:    domain,
		user:      user,
		password:  password,
		respKeyNT: NTOWFv2(password, user, domain),
		respKeyLM: LMOWFv2(password, user, domain),
	}
}

// generate first handshake messgae
func (n *NTLMv2) GetNegotiateMessage() *NegotiateMessage {
	negoMsg := NewNegotiateMessage()
	negoMsg.NegotiateFlags = NTLMSSP_NEGOTIATE_KEY_EXCH |
		NTLMSSP_NEGOTIATE_128 |
		NTLMSSP_NEGOTIATE_EXTENDED_SESSIONSECURITY |
		NTLMSSP_NEGOTIATE_ALWAYS_SIGN |
		NTLMSSP_NEGOTIATE_NTLM |
		NTLMSSP_NEGOTIATE_SEAL |
		NTLMSSP_NEGOTIATE_SIGN |
		NTLMSSP_REQUEST_TARGET |
		NTLMSSP_NEGOTIATE_UNICODE
	n.negotiateMessage = negoMsg
	return n.negotiateMessage
}

// process NTLMv2 Authenticate hash
func (n *NTLMv2) ComputeResponseV2(respKeyNT, respKeyLM, serverChallenge, clientChallenge,
	timestamp, serverInfo []byte) (ntChallResp, lmChallResp, SessBaseKey []byte) {

	tempBuff := &bytes.Buffer{}
	tempBuff.Write([]byte{0x01, 0x01}) // Responser version, HiResponser version
	tempBuff.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	tempBuff.Write(timestamp)
	tempBuff.Write(clientChallenge)
	tempBuff.Write([]byte{0x00, 0x00, 0x00, 0x00})
	tempBuff.Write(serverInfo)
	tempBuff.Write([]byte{0x00, 0x00, 0x00, 0x00})

	ntBuf := bytes.NewBuffer(serverChallenge)
	ntBuf.Write(tempBuff.Bytes())
	ntProof := HMAC_MD5(respKeyNT, ntBuf.Bytes())

	ntChallResp = make([]byte, 0, len(ntProof)+tempBuff.Len())
	ntChallResp = append(ntChallResp, ntProof...)
	ntChallResp = append(ntChallResp, tempBuff.Bytes()...)

	lmBuf := bytes.NewBuffer(serverChallenge)
	lmBuf.Write(clientChallenge)
	lmChallResp = HMAC_MD5(respKeyLM, lmBuf.Bytes())
	lmChallResp = append(lmChallResp, clientChallenge...)

	SessBaseKey = HMAC_MD5(respKeyNT, ntProof)
	return
}

// MIC computes the message integrity code over the three messages of the exchange,
// which is what proves to the server that the messages it saw are the messages
// that were sent.
//
// It is computed over the exported session key and not the session key, which is
// the distinction that matters: the session key is what seals traffic, and the
// exported key is what the exchange itself produced.
func MIC(exportedSessionKey []byte, negotiateMessage, challengeMessage, authenticateMessage Message) []byte {
	buff := bytes.Buffer{}
	buff.Write(negotiateMessage.Serialize())
	buff.Write(challengeMessage.Serialize())
	buff.Write(authenticateMessage.Serialize())
	return HMAC_MD5(exportedSessionKey, buff.Bytes())
}

func concat(bs ...[]byte) []byte {
	return bytes.Join(bs, nil)
}

// SIGNKEY computes the NTLM signing key from the exported session key
// (MS-NLMP 3.4.5.3). isClient selects the client-to-server direction.
// SIGNKEY derives a signing key from the exported session key for one direction,
// which is MS-NLMP 3.4.5.3. isClient selects client to server.
//
// It is exported and unused: GetAuthenticateMessage derives the four signing and
// sealing keys itself from the session key and the magic constants, so this and the
// constants beside it are a second path to the same values.
func SIGNKEY(exportedSessionKey []byte, isClient bool) []byte {
	magic := serverSigning
	if isClient {
		magic = clientSigning
	}
	return MD5(concat(exportedSessionKey, magic))
}

var (
	clientSigning = concat([]byte("session key to client-to-server signing key magic constant"), []byte{0x00})
	serverSigning = concat([]byte("session key to server-to-client signing key magic constant"), []byte{0x00})
	clientSealing = concat([]byte("session key to client-to-server sealing key magic constant"), []byte{0x00})
	serverSealing = concat([]byte("session key to server-to-client sealing key magic constant"), []byte{0x00})
)

// GetAuthenticateMessage reads the server's challenge and returns the message to
// answer it with, along with the security context that seals what follows.
//
// The exchange it performs is the whole of NTLMv2: the responses are computed over
// the server challenge and a client challenge, the session key is chosen and sent
// wrapped under the exported key, the signing and sealing keys are derived from it,
// and the MIC is computed when the server's target info carried a timestamp. The
// order matters, because the MIC is computed over the finished authenticate
// message.
//
// A failure returns two nils rather than an error, so a caller has to check what it
// gets back.
func (n *NTLMv2) GetAuthenticateMessage(s []byte) (*AuthenticateMessage, *NTLMv2Security) {
	challengeMsg := &ChallengeMessage{}
	r := bytes.NewReader(s)
	err := struc.Unpack(r, challengeMsg)
	if err != nil {
		glog.Error("read challengeMsg", err)
		return nil, nil
	}
	if challengeMsg.NegotiateFlags&NTLMSSP_NEGOTIATE_VERSION != 0 {
		version := NVersion{}
		err := struc.Unpack(r, &version)
		if err != nil {
			glog.Error("read version", err)
			return nil, nil
		}
		challengeMsg.Version = version
	}
	challengeMsg.Payload, _ = core.ReadBytes(r.Len(), r)
	n.challengeMessage = challengeMsg
	glog.Debugf("challengeMsg:%+v", challengeMsg)

	serverName := challengeMsg.getTargetName()
	serverInfo := challengeMsg.getTargetInfo()
	timestamp := challengeMsg.getTargetInfoTimestamp(serverInfo)
	computeMIC := false
	if timestamp == nil {
		ft := uint64(time.Now().UnixNano()) / 100
		ft += 116444736000000000 // add time between unix & windows offset
		timestamp = make([]byte, 8)
		binary.LittleEndian.PutUint64(timestamp, ft)
	} else {
		computeMIC = true
	}
	glog.Infof("serverName=%+v", core.UnicodeDecode(serverName))
	serverChallenge := challengeMsg.ServerChallenge[:]
	clientChallenge := core.Random(8)
	ntChallengeResponse, lmChallengeResponse, SessionBaseKey := n.ComputeResponseV2(
		n.respKeyNT, n.respKeyLM, serverChallenge, clientChallenge, timestamp, serverInfo)

	// ExportedSessionKey is derived from the NTLMv2 exchange (SessionBaseKey
	// for NTLMv2), not chosen. It keys the MIC and wraps the random session
	// key. Writing a random value here, as this code used to, makes the server
	// derive a different key and reject every sealed message.
	exportedSessionKey := SessionBaseKey

	// Key exchange: the client picks the session key and sends it wrapped
	// under the exported key. The sealing and signing keys are derived from
	// this one, while the MIC still uses the exported key.
	sessionKey := core.Random(16)
	EncryptedRandomSessionKey := make([]byte, len(sessionKey))
	rc, _ := rc4.NewCipher(exportedSessionKey)
	rc.XORKeyStream(EncryptedRandomSessionKey, sessionKey)

	if challengeMsg.NegotiateFlags&NTLMSSP_NEGOTIATE_UNICODE != 0 {
		n.enableUnicode = true
	}
	// Never the password, and never anything derived from it. This line used
	// to read `glog.Infof("user: %s, passwd:%s", n.user, n.password)`, which
	// wrote the account's password to the log in clear text on every NLA
	// connection. What is worth logging about this step is what it decided:
	// the character set and the length of the responses, which is enough to
	// tell a wrong password from a protocol fault without any of the material
	// being in the log. A test in this package fails the build if a log call
	// in this repository is handed a password field again.
	glog.Debugf("nla: authenticate uses unicode=%v, nt response %d bytes, lm response %d bytes",
		n.enableUnicode, len(ntChallengeResponse), len(lmChallengeResponse))
	domain, user, _ := n.GetEncodedCredentials()

	n.authenticateMessage = NewAuthenticateMessage(challengeMsg.NegotiateFlags,
		domain, user, []byte(""), lmChallengeResponse, ntChallengeResponse, EncryptedRandomSessionKey)

	if computeMIC {
		copy(n.authenticateMessage.MIC[:], MIC(exportedSessionKey, n.negotiateMessage, n.challengeMessage, n.authenticateMessage)[:16])
	}

	md := md5.New()
	//ClientSigningKey
	a := concat(sessionKey, clientSigning)
	md.Write(a)
	ClientSigningKey := md.Sum(nil)
	//ServerSigningKey
	md.Reset()
	a = concat(sessionKey, serverSigning)
	md.Write(a)
	ServerSigningKey := md.Sum(nil)
	//ClientSealingKey
	md.Reset()
	a = concat(sessionKey, clientSealing)
	md.Write(a)
	ClientSealingKey := md.Sum(nil)
	//ServerSealingKey
	md.Reset()
	a = concat(sessionKey, serverSealing)
	md.Write(a)
	ServerSealingKey := md.Sum(nil)

	glog.Debugf("ClientSigningKey:%s", glog.Hex(ClientSigningKey))
	glog.Debugf("ServerSigningKey:%s", glog.Hex(ServerSigningKey))
	glog.Debugf("ClientSealingKey:%s", glog.Hex(ClientSealingKey))
	glog.Debugf("ServerSealingKey:%s", glog.Hex(ServerSealingKey))

	encryptRC4, _ := rc4.NewCipher(ClientSealingKey)
	decryptRC4, _ := rc4.NewCipher(ServerSealingKey)

	ntlmSec := &NTLMv2Security{encryptRC4, decryptRC4, ClientSigningKey, ServerSigningKey, 0}

	return n.authenticateMessage, ntlmSec
}

// GetEncodedCredentials returns the domain, user and password in the character set
// the exchange settled on, which is Unicode whenever the server's flags allow it
// and the bytes as given otherwise. The password is returned because the
// credential structure has to carry it, and it does not reach the log.
func (n *NTLMv2) GetEncodedCredentials() ([]byte, []byte, []byte) {
	if n.enableUnicode {
		return core.UnicodeEncode(n.domain), core.UnicodeEncode(n.user), core.UnicodeEncode(n.password)
	}
	return []byte(n.domain), []byte(n.user), []byte(n.password)
}

// NTLMv2Security seals and signs the messages that follow the exchange, which is
// what CredSSP's authentication info and public key binding travel in.
//
// There are two RC4 ciphers rather than one because each direction has its own
// sealing key, and a sequence number because each message is signed over it, so the
// two sides have to count in step. The count is per direction and never reset.
type NTLMv2Security struct {
	EncryptRC4 *rc4.Cipher
	DecryptRC4 *rc4.Cipher
	SigningKey []byte
	VerifyKey  []byte
	SeqNum     uint32
}

// GssEncrypt seals a message and returns it with its signature: a version, an
// eight byte checksum, and the sequence number.
//
// With key exchange in use, the checksum is sealed as well as the message, which is
// why the same cipher is applied to both. The checksum is computed over the
// sequence number and the plaintext, so the receiver can recompute it after
// unsealing. The sequence number advances whether or not the message is accepted,
// since both sides count the same way.
func (n *NTLMv2Security) GssEncrypt(s []byte) []byte {
	// Key exchange seals the checksum as well as the message.
	p := make([]byte, len(s))
	n.EncryptRC4.XORKeyStream(p, s)

	b := &bytes.Buffer{}
	core.WriteUInt32LE(n.SeqNum, b)
	core.WriteBytes(s, b)
	checksum := make([]byte, 8)
	n.EncryptRC4.XORKeyStream(checksum, HMAC_MD5(n.SigningKey, b.Bytes())[:8])

	b.Reset()
	core.WriteUInt32LE(0x00000001, b) // signature version
	core.WriteBytes(checksum, b)
	core.WriteUInt32LE(n.SeqNum, b)
	core.WriteBytes(p, b)

	n.SeqNum++

	return b.Bytes()
}

// GssDecrypt unseals a message and returns its plaintext, or nil when the checksum
// does not match.
//
// A nil return is the only signal that a message failed, and it is not an error: a
// caller that treats nil as a short message rather than as a failure would act on
// data that was not authenticated. The sequence number in the message is used to
// verify the checksum, but the counter here is not advanced, so a receiver keeps
// its own count.
func (n *NTLMv2Security) GssDecrypt(s []byte) []byte {
	r := bytes.NewReader(s)
	core.ReadUInt32LE(r) // signature version
	checksum, _ := core.ReadBytes(8, r)
	seqNum, _ := core.ReadUInt32LE(r)
	data, _ := core.ReadBytes(r.Len(), r)

	p := make([]byte, len(data))
	n.DecryptRC4.XORKeyStream(p, data)

	check := make([]byte, len(checksum))
	n.DecryptRC4.XORKeyStream(check, checksum)

	b := &bytes.Buffer{}
	core.WriteUInt32LE(seqNum, b)
	core.WriteBytes(p, b)
	verify := HMAC_MD5(n.VerifyKey, b.Bytes())[:8]
	if !bytes.Equal(verify, check) {
		return nil
	}
	return p
}
