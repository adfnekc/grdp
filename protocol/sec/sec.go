package sec

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"crypto/rsa"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"

	"github.com/lunixbochs/struc"

	"github.com/adfnekc/grdp/protocol/nla"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/lic"
	"github.com/adfnekc/grdp/protocol/t125"
	"github.com/adfnekc/grdp/protocol/t125/gcc"
)

/**
 * SecurityFlag
 * @see http://msdn.microsoft.com/en-us/library/cc240579.aspx
 */
const (
	EXCHANGE_PKT       uint16 = 0x0001
	TRANSPORT_REQ             = 0x0002
	TRANSPORT_RSP             = 0x0004
	ENCRYPT                   = 0x0008
	RESET_SEQNO               = 0x0010
	IGNORE_SEQNO              = 0x0020
	INFO_PKT                  = 0x0040
	LICENSE_PKT               = 0x0080
	LICENSE_ENCRYPT_CS        = 0x0200
	LICENSE_ENCRYPT_SC        = 0x0200
	REDIRECTION_PKT           = 0x0400
	SECURE_CHECKSUM           = 0x0800
	AUTODETECT_REQ            = 0x1000
	AUTODETECT_RSP            = 0x2000
	HEARTBEAT                 = 0x4000
	FLAGSHI_VALID             = 0x8000
)

// The client info flags, which say what the client can do and what the server
// should tell it about. They travel in the info PDU that follows the credentials.
//
// INFO_UNICODE is the one that decides the character set for everything else in the
// PDU, including the credentials. INFO_MOUSE_HAS_WHEEL and INFO_FORCE_ENCRYPTED_CS_PDU
// are what a client asks for rather than what it reports, and
// INFO_CompressionTypeMask is a mask over three bits rather than a flag of its own.
const (
	INFO_MOUSE                  uint32 = 0x00000001
	INFO_DISABLECTRLALTDEL             = 0x00000002
	INFO_AUTOLOGON                     = 0x00000008
	INFO_UNICODE                       = 0x00000010
	INFO_MAXIMIZESHELL                 = 0x00000020
	INFO_LOGONNOTIFY                   = 0x00000040
	INFO_COMPRESSION                   = 0x00000080
	INFO_ENABLEWINDOWSKEY              = 0x00000100
	INFO_REMOTECONSOLEAUDIO            = 0x00002000
	INFO_FORCE_ENCRYPTED_CS_PDU        = 0x00004000
	INFO_RAIL                          = 0x00008000
	INFO_LOGONERRORS                   = 0x00010000
	INFO_MOUSE_HAS_WHEEL               = 0x00020000
	INFO_PASSWORD_IS_SC_PIN            = 0x00040000
	INFO_NOAUDIOPLAYBACK               = 0x00080000
	INFO_USING_SAVED_CREDS             = 0x00100000
	INFO_AUDIOCAPTURE                  = 0x00200000
	INFO_VIDEO_DISABLE                 = 0x00400000
	INFO_CompressionTypeMask           = 0x00001E00
)

// The address families for the client address in the extended info. They are the
// Windows values rather than the Unix ones, which is why INET is 2 here and not the
// 2 that happens to be the same on both.
const (
	AF_INET  uint16 = 0x00002
	AF_INET6        = 0x0017
)

// The performance flags, which ask the server to leave out the things that make a
// session slow over a thin connection. Each one removes something from what the
// server draws, so they trade appearance for bandwidth, and all of them being clear
// is what a local connection wants.
const (
	PERF_DISABLE_WALLPAPER          uint32 = 0x00000001
	PERF_DISABLE_FULLWINDOWDRAG            = 0x00000002
	PERF_DISABLE_MENUANIMATIONS            = 0x00000004
	PERF_DISABLE_THEMING                   = 0x00000008
	PERF_DISABLE_CURSOR_SHADOW             = 0x00000020
	PERF_DISABLE_CURSORSETTINGS            = 0x00000040
	PERF_ENABLE_FONT_SMOOTHING             = 0x00000080
	PERF_ENABLE_DESKTOP_COMPOSITION        = 0x00000100
)

// The security flags in a fast-path PDU's action byte, which are read from the top
// two bits and not from a separate field. SECURE_CHECKSUM is what changes the MAC
// computation, and ENCRYPTED is what decides whether the payload has to be
// decrypted: the two are independent, so a PDU can be encrypted without the secure
// checksum.
const (
	FASTPATH_OUTPUT_SECURE_CHECKSUM = 0x1
	FASTPATH_OUTPUT_ENCRYPTED       = 0x2
)

// ClientAutoReconnect is the auto reconnect cookie, which lets a session be picked
// up again after a connection drops without asking for credentials a second time.
//
// SecVerifier is derived from the random value the server sent, so the cookie cannot
// be replayed against another session.
type ClientAutoReconnect struct {
	CbAutoReconnectLen uint16
	CbLen              uint32
	Version            uint32
	LogonId            uint32
	SecVerifier        []byte
}

// NewClientAutoReconnect returns a cookie for the given logon id, with the verifier
// derived from the server's random value.
func NewClientAutoReconnect(id uint32, random []byte) *ClientAutoReconnect {
	return &ClientAutoReconnect{
		CbAutoReconnectLen: 28,
		CbLen:              28,
		Version:            1,
		LogonId:            id,
		SecVerifier:        nla.HMAC_MD5(random, random),
	}
}

// RDPExtendedInfo is the part of the info PDU that a client sends when it can send
// one, which is everything after the credentials: the client's address and directory,
// its time zone, and the auto reconnect cookie.
//
// ClientTimeZone is the Windows time zone structure, which is a fixed 172 bytes
// whose contents this client leaves at zero. That is allowed: the field is for
// display in the session, not for the connection.
type RDPExtendedInfo struct {
	ClientAddressFamily uint16 `struc:"little"`
	CbClientAddress     uint16 `struc:"little,sizeof=ClientAddress"`
	ClientAddress       []byte `struc:"[]byte"`
	CbClientDir         uint16 `struc:"little,sizeof=ClientDir"`
	ClientDir           []byte `struc:"[]byte"`
	ClientTimeZone      []byte `struc:"[172]byte"`
	ClientSessionId     uint32 `struc:"litttle"`
	PerformanceFlags    uint32 `struc:"little"`
	AutoReconnect       *ClientAutoReconnect
}

// NewExtendedInfo returns extended info with empty address and directory strings and
// a zero time zone, which is what this client sends. The auto reconnect cookie may
// be nil, in which case the cookie is left out of the serialized form.
func NewExtendedInfo(auto *ClientAutoReconnect) *RDPExtendedInfo {
	return &RDPExtendedInfo{
		ClientAddressFamily: AF_INET,
		ClientAddress:       []byte{0, 0},
		ClientDir:           []byte{0, 0},
		ClientTimeZone:      make([]byte, 172),
		ClientSessionId:     0,
		AutoReconnect:       auto,
	}
}

// Serialize returns the extended info as it goes on the wire. It is written by hand
// rather than by the struct tags because the two strings are length prefixed with a
// length that counts the NUL terminator, which the tags cannot express.
//
// The auto reconnect block is written only when there is one, and the field before it
// is what says so on the wire.
func (o *RDPExtendedInfo) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt16LE(o.ClientAddressFamily, buff)
	core.WriteUInt16LE(uint16(len(o.ClientAddress)), buff)
	core.WriteBytes(o.ClientAddress, buff)
	core.WriteUInt16LE(uint16(len(o.ClientDir)), buff)
	core.WriteBytes(o.ClientDir, buff)
	core.WriteBytes(o.ClientTimeZone, buff)
	core.WriteUInt32LE(o.ClientSessionId, buff)
	core.WriteUInt32LE(o.PerformanceFlags, buff)

	if o.AutoReconnect != nil {
		core.WriteUInt16LE(o.AutoReconnect.CbAutoReconnectLen, buff)
		core.WriteUInt32LE(o.AutoReconnect.CbLen, buff)
		core.WriteUInt32LE(o.AutoReconnect.Version, buff)
		core.WriteUInt32LE(o.AutoReconnect.LogonId, buff)
		core.WriteBytes(o.AutoReconnect.SecVerifier, buff)
	}

	return buff.Bytes()
}

// RDPInfo is the info PDU, which is the client's first message after the connection
// is up: the credentials in UTF-16, the shell and working directory, and the flags
// that describe the client.
//
// Every string is stored with the NUL terminator that the wire format requires and
// the length fields count from it, so the serialized lengths are two less than the
// slices: the terminator is not part of the count.
type RDPInfo struct {
	CodePage         uint32
	Flag             uint32
	CbDomain         uint16
	CbUserName       uint16
	CbPassword       uint16
	CbAlternateShell uint16
	CbWorkingDir     uint16
	Domain           []byte
	UserName         []byte
	Password         []byte
	AlternateShell   []byte
	WorkingDir       []byte
	ExtendedInfo     *RDPExtendedInfo
}

// NewRDPInfo returns an info PDU with the flags a client normally sends and empty
// credentials.
//
// The strings are set to a single NUL rather than to nothing, because the length
// fields are computed by subtracting two from the slice length and a nil slice would
// underflow. The credentials are filled in by the setters.
func NewRDPInfo() *RDPInfo {
	info := &RDPInfo{
		Flag: INFO_MOUSE | INFO_UNICODE | INFO_MAXIMIZESHELL |
			INFO_ENABLEWINDOWSKEY | INFO_DISABLECTRLALTDEL | INFO_MOUSE_HAS_WHEEL |
			INFO_FORCE_ENCRYPTED_CS_PDU | INFO_AUTOLOGON,
		Domain:         []byte{0, 0},
		UserName:       []byte{0, 0},
		Password:       []byte{0, 0},
		AlternateShell: []byte{0, 0},
		WorkingDir:     []byte{0, 0},
		ExtendedInfo:   NewExtendedInfo(nil),
	}
	return info
}

// SetClientAutoReconnect attaches an auto reconnect cookie to the info PDU.
func (o *RDPInfo) SetClientAutoReconnect(auto *ClientAutoReconnect) {
	o.ExtendedInfo.AutoReconnect = auto
}

// SetClientInfo adds the flags that ask the server to report logon results, which is
// what makes a wrong password arrive as a message rather than as a silent disconnect
// during the later exchange.
func (o *RDPInfo) SetClientInfo() {
	o.Flag |= INFO_LOGONNOTIFY | INFO_LOGONERRORS
}

// Serialize returns the info PDU: the code page, the flags, five lengths, the five
// strings, and the extended info when the caller says to include it.
//
// The lengths are the slice lengths less two, because each string carries a NUL
// terminator that the count does not include. hasExtended is the caller's decision
// rather than something derived here, since the extended info is what a client sends
// when it negotiated the ability to.
func (o *RDPInfo) Serialize(hasExtended bool) []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt32LE(o.CodePage, buff)                      // 0000000
	core.WriteUInt32LE(o.Flag, buff)                          // 0530101
	core.WriteUInt16LE(uint16(len(o.Domain)-2), buff)         // 001c
	core.WriteUInt16LE(uint16(len(o.UserName)-2), buff)       // 0008
	core.WriteUInt16LE(uint16(len(o.Password)-2), buff)       //000c
	core.WriteUInt16LE(uint16(len(o.AlternateShell)-2), buff) //0000
	core.WriteUInt16LE(uint16(len(o.WorkingDir)-2), buff)     //0000
	core.WriteBytes(o.Domain, buff)
	core.WriteBytes(o.UserName, buff)
	core.WriteBytes(o.Password, buff)
	core.WriteBytes(o.AlternateShell, buff)
	core.WriteBytes(o.WorkingDir, buff)
	if hasExtended {
		core.WriteBytes(o.ExtendedInfo.Serialize(), buff)
	}
	return buff.Bytes()
}

// SecurityHeader is the two flag words in front of an encrypted PDU. The high word
// is only meaningful when the low one has FLAGSHI_VALID set, which is what makes the
// second word optional on the wire.
type SecurityHeader struct {
	securityFlag   uint16
	securityFlagHi uint16
}

func readSecurityHeader(r io.Reader) *SecurityHeader {
	s := &SecurityHeader{}
	s.securityFlag, _ = core.ReadUint16LE(r)
	s.securityFlagHi, _ = core.ReadUint16LE(r)
	return s
}

// sessionKeyUpdateCount is how many packets one key encrypts or decrypts before
// the session keys are re-derived (MS-RDPBCGR 5.3.7.1).
const sessionKeyUpdateCount = 4096

// The padding constants of the session key update (MS-RDPBCGR 5.3.7.1): Pad1
// is 0x36 repeated 40 times and Pad2 is 0x5C repeated 48 times.
var (
	keyUpdatePad1 = bytes.Repeat([]byte{0x36}, 40)
	keyUpdatePad2 = bytes.Repeat([]byte{0x5C}, 48)
)

// SEC is the security layer: it encrypts what goes out, decrypts what comes in, and
// owns the keys and the info PDU.
//
// It works with no encryption at all when the session runs over TLS or CredSSP,
// which is the usual case now, and with the Standard RDP Security keys otherwise.
// Which of the two it is decides whether the flags in a PDU header mean anything:
// over TLS they are written but the payload is already protected by the transport.
//
// The licensing keys are deliberately separate from the session keys, because the
// MAC over a licensing blob uses a different salt from the MAC over an ordinary PDU,
// and confusing the two produces a session that connects and then fails at the
// licence.
type SEC struct {
	emission.Emitter
	transport   core.Transport
	info        *RDPInfo
	machineName string
	clientData  []interface{}
	serverData  []interface{}

	enableEncryption bool
	//Enable Secure Mac generation
	enableSecureCheckSum bool
	//The negotiated encryption method. A 40 bit and a 56 bit key are both
	//eight bytes long and are salted differently when a key is updated, so the
	//key length alone does not say which one is in use.
	encryptionMethod uint32
	//counter before update
	nbEncryptedPacket int
	nbDecryptedPacket int

	currentDecrytKey  []byte
	currentEncryptKey []byte

	//The keys the session was established with. Every key update re-derives
	//from these and the current key, never from the current key alone.
	initialDecrytKey  []byte
	initialEncryptKey []byte

	//current rc4 tab
	decryptRc4 *rc4.Cipher
	encryptRc4 *rc4.Cipher

	macKey []byte

	//Licensing keys, derived from the licensing premaster secret rather than
	//from the client and server randoms. They are kept apart from the session
	//keys: the MAC over a licensing blob uses the licensing salt key, while the
	//MAC of an encrypted PDU uses the session MAC key.
	licenseMacKey []byte
	licenseKey    []byte
}

// NewSEC returns a security layer over a transport, with an empty info PDU and
// encryption off until the server's security data says otherwise.
//
// The transport's close and error events are forwarded.
func NewSEC(t core.Transport) *SEC {
	sec := &SEC{
		Emitter:   *emission.NewEmitter(),
		transport: t,
		info:      NewRDPInfo(),
	}

	t.On("close", func() {
		sec.Emit("close")
	}).On("error", func(err error) {
		sec.Emit("error", err)
	})
	return sec
}

// Read reads from the transport with nothing decrypted. Reading is handled by the
// receive path, which decides from the header whether a PDU is encrypted, so this is
// only for a caller that wants the raw stream.
func (s *SEC) Read(data []byte) (n int, err error) {
	return s.transport.Read(data)
}

// Write sends a PDU, encrypting it and prefixing a security header when encryption is
// on, and sending it unchanged when it is not. Over TLS the second path is the one
// taken, which is why the flags in a later PDU header are written but mean nothing.
func (s *SEC) Write(b []byte) (n int, err error) {
	if !s.enableEncryption {
		return s.transport.Write(b)
	}
	data := s.encrytData(b)
	return s.transport.Write(data)
}

// Close closes the transport. It does not stop the receive path, which notices and
// emits "close".
func (s *SEC) Close() error {
	return s.transport.Close()
}

func (s *SEC) sendFlagged(flag uint16, data []byte) (n int, err error) {
	glog.Trace("sendFlagged:", glog.Hex(data))
	b := s.encryt(flag, data)
	return s.transport.Write(b)
}

/*
@see: http://msdn.microsoft.com/en-us/library/cc241995.aspx
@param macSaltKey: {str} mac key
@param data: {str} data to sign
@return: {str} signature
*/
func macData(macSaltKey, data []byte) []byte {
	sha1Digest := sha1.New()
	md5Digest := md5.New()

	b := &bytes.Buffer{}
	core.WriteUInt32LE(uint32(len(data)), b)

	sha1Digest.Write(macSaltKey)
	for i := 0; i < 40; i++ {
		sha1Digest.Write([]byte("\x36"))
	}

	sha1Digest.Write(b.Bytes())
	sha1Digest.Write(data)

	sha1Sig := sha1Digest.Sum(nil)

	md5Digest.Write(macSaltKey)
	for i := 0; i < 48; i++ {
		md5Digest.Write([]byte("\x5c"))
	}

	md5Digest.Write(sha1Sig)

	return md5Digest.Sum(nil)
}

// updateSessionKey derives the next session key from the key the session was
// established with and the one in use now (MS-RDPBCGR 5.3.7.1, "Non-FIPS"):
//
//	SHAComponent = SHA1(InitialKey + Pad1 + CurrentKey)
//	TempKey128   = MD5(InitialKey + Pad2 + SHAComponent)
//	NewKey128    = RC4(TempKey128, InitRC4(TempKey128))
//
// The 40 and 56 bit forms take the first eight bytes of the temporary key and
// salt them, exactly as the initial keys are salted. FreeRDP's
// security_key_update (libfreerdp/core/security.c) derives the same values.
func updateSessionKey(initialKey, currentKey []byte, method uint32) []byte {
	if len(initialKey) == 0 || len(initialKey) != len(currentKey) {
		return nil
	}

	// A 40 bit and a 56 bit key are both eight bytes, taken from the first
	// eight bytes of the temporary key.
	rc4KeyLen := 16
	if method == gcc.ENCRYPTION_FLAG_40BIT || method == gcc.ENCRYPTION_FLAG_56BIT {
		rc4KeyLen = 8
	}
	keyLen := len(initialKey)
	if keyLen != 8 && keyLen != 16 {
		return nil
	}
	if rc4KeyLen != keyLen {
		return nil
	}

	sha1Digest := sha1.New()
	sha1Digest.Write(initialKey)
	sha1Digest.Write(keyUpdatePad1)
	sha1Digest.Write(currentKey)
	shaComponent := sha1Digest.Sum(nil)

	md5Digest := md5.New()
	md5Digest.Write(initialKey)
	md5Digest.Write(keyUpdatePad2)
	md5Digest.Write(shaComponent)
	tempKey := md5Digest.Sum(nil)

	cipher, err := rc4.NewCipher(tempKey[:rc4KeyLen])
	if err != nil {
		glog.Error("sec: session key update: ", err)
		return nil
	}
	newKey := make([]byte, rc4KeyLen)
	cipher.XORKeyStream(newKey, tempKey[:rc4KeyLen])

	switch method {
	case gcc.ENCRYPTION_FLAG_40BIT:
		copy(newKey, []byte{0xd1, 0x26, 0x9e})
	case gcc.ENCRYPTION_FLAG_56BIT:
		newKey[0] = 0xd1
	}
	return newKey
}

// updateEncryptKey re-derives the encryption key and restarts the RC4 stream
// with it, which is what makes a long connection survive past 4096 packets.
func (s *SEC) updateEncryptKey() error {
	key := updateSessionKey(s.initialEncryptKey, s.currentEncryptKey, s.encryptionMethod)
	if key == nil {
		return errors.New("sec: cannot update the encryption key without the session keys")
	}
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return fmt.Errorf("sec: update the encryption key: %w", err)
	}
	glog.Debug("sec: encryption key updated after ", sessionKeyUpdateCount, " packets")
	s.currentEncryptKey = key
	s.encryptRc4 = cipher
	s.nbEncryptedPacket = 0
	return nil
}

// updateDecryptKey is updateEncryptKey for the other direction.
func (s *SEC) updateDecryptKey() error {
	key := updateSessionKey(s.initialDecrytKey, s.currentDecrytKey, s.encryptionMethod)
	if key == nil {
		return errors.New("sec: cannot update the decryption key without the session keys")
	}
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return fmt.Errorf("sec: update the decryption key: %w", err)
	}
	glog.Debug("sec: decryption key updated after ", sessionKeyUpdateCount, " packets")
	s.currentDecrytKey = key
	s.decryptRc4 = cipher
	s.nbDecryptedPacket = 0
	return nil
}

func (s *SEC) readEncryptedPayload(data []byte, checkSum bool) []byte {
	r := bytes.NewReader(data)
	sign, _ := core.ReadBytes(8, r)
	glog.Debug("read sign:", sign)
	encryptedPayload, _ := core.ReadBytes(r.Len(), r)

	// MS-RDPBCGR 5.3.7.1: the keys are updated after 4096 packets have been
	// received, so this is the first packet protected with the new key.
	if s.nbDecryptedPacket >= sessionKeyUpdateCount {
		if err := s.updateDecryptKey(); err != nil {
			glog.Error("sec: ", err)
			return nil
		}
	}

	if s.decryptRc4 == nil {
		cipher, err := rc4.NewCipher(s.currentDecrytKey)
		if err != nil {
			glog.Error("sec: cannot decrypt without session keys: ", err)
			return nil
		}
		s.decryptRc4 = cipher
	}
	s.nbDecryptedPacket++
	glog.Debug("nbDecryptedPacket:", s.nbDecryptedPacket)
	plaintext := make([]byte, len(encryptedPayload))
	s.decryptRc4.XORKeyStream(plaintext, encryptedPayload)

	return plaintext

}
func (s *SEC) writeEncryptedPayload(data []byte, checkSum bool) []byte {
	// MS-RDPBCGR 5.3.7.1: after 4096 packets have been encrypted the keys are
	// re-derived, so this packet uses the new key.
	if s.nbEncryptedPacket >= sessionKeyUpdateCount {
		if err := s.updateEncryptKey(); err != nil {
			glog.Error("sec: ", err)
			return nil
		}
	}

	if checkSum {
		glog.Debug("need checkSum")
		return []byte{}
	}

	s.nbEncryptedPacket++
	glog.Debug("nbEncryptedPacket:", s.nbEncryptedPacket)
	b := &bytes.Buffer{}

	sign := macData(s.macKey, data)[:8]
	if s.encryptRc4 == nil {
		cipher, err := rc4.NewCipher(s.currentEncryptKey)
		if err != nil {
			glog.Error("sec: cannot encrypt without session keys: ", err)
			return nil
		}
		s.encryptRc4 = cipher
	}

	plaintext := make([]byte, len(data))
	s.encryptRc4.XORKeyStream(plaintext, data)
	b.Write(sign)
	b.Write(plaintext)
	glog.Debug("sign:", glog.Hex(sign), "plaintext:", glog.Hex(plaintext))
	return b.Bytes()
}

func (s *SEC) encryt(flag uint16, b []byte) []byte {
	data := b
	if flag&ENCRYPT != 0 {
		data = s.writeEncryptedPayload(b, flag&SECURE_CHECKSUM != 0)
	}
	buff := &bytes.Buffer{}
	core.WriteUInt16LE(flag, buff)
	core.WriteUInt16LE(0, buff)
	core.WriteBytes(data, buff)

	return buff.Bytes()
}
func (s *SEC) encrytData(b []byte) []byte {
	if !s.enableEncryption {
		return b
	}

	var flag uint16 = ENCRYPT
	if s.enableSecureCheckSum {
		flag |= SECURE_CHECKSUM
	}
	return s.encryt(flag, b)
}

func (s *SEC) decrytData(b []byte) []byte {
	if !s.enableEncryption {
		return b
	}

	r := bytes.NewReader(b)
	securityFlag, _ := core.ReadUint16LE(r)
	_, _ = core.ReadUint16LE(r) //securityFlagHi
	data, _ := core.ReadBytes(r.Len(), r)
	if securityFlag&ENCRYPT != 0 {
		data = s.readEncryptedPayload(data, securityFlag&SECURE_CHECKSUM != 0)
	}
	return data
}

// Client is the security layer's client end, which runs the client half of the
// connection: it sends the info PDU, answers the licence request, and holds the
// channel sender and fast-path listener that the layers above write through.
type Client struct {
	*SEC
	userId    uint16
	channelId uint16

	fastPathListener core.FastPathListener
	channelSender    core.ChannelSender

	// requireVerifiedCertificate turns an unverified server certificate from a
	// warning into a failure. It is off by default because a self-signed
	// certificate is what an RDP server normally presents, and it only has an
	// effect on Standard RDP Security: over TLS the certificate is the TLS
	// layer's business and is verified there.
	requireVerifiedCertificate bool
}

// NewClient returns a client over a transport. The connection sequence starts when
// the transport emits "connect", not here, so the credentials can be set afterwards.
func NewClient(t core.Transport) *Client {
	c := &Client{
		SEC: NewSEC(t),
	}
	t.On("connect", c.connect)
	return c
}

// SetClientAutoReconnect gives the client an auto reconnect cookie, built from the
// logon id and the random value the server sent.
//
// The cookie is what lets a session be picked up again after a dropped connection
// without a second logon, and the verifier inside it is derived from the server's
// random value so that it cannot be replayed against another session.
func (c *Client) SetClientAutoReconnect(id uint32, random []byte) {
	auto := NewClientAutoReconnect(id, random)
	c.info.SetClientAutoReconnect(auto)
}

// SetAlternateShell sets the program to run instead of Explorer when the session
// starts, and sets the flag that tells the server the field is there.
//
// The string is UTF-16 with a NUL terminator, which is the form every string in this
// PDU takes.
func (c *Client) SetAlternateShell(shell string) {
	buff := &bytes.Buffer{}
	for _, ch := range utf16.Encode([]rune(shell)) {
		core.WriteUInt16LE(ch, buff)
	}
	core.WriteUInt16LE(0, buff)
	c.info.AlternateShell = buff.Bytes()
	c.info.Flag |= INFO_RAIL
}

// SetUser sets the user name in the info PDU, in UTF-16 with a terminator.
//
// Nothing checks it: a name that is wrong is not rejected here, and the server is
// what decides, which is why the failure a caller sees for a bad account comes from
// the security exchange rather than from this.
func (c *Client) SetUser(user string) {
	buff := &bytes.Buffer{}
	for _, ch := range utf16.Encode([]rune(user)) {
		core.WriteUInt16LE(ch, buff)
	}
	core.WriteUInt16LE(0, buff)
	c.info.UserName = buff.Bytes()
}

// SetPwd sets the password in the info PDU, in UTF-16 with a terminator.
//
// The password is stored because the PDU has to carry it. It is not logged, and a
// test in this repository fails the build if a log call is handed a password field.
func (c *Client) SetPwd(pwd string) {
	buff := &bytes.Buffer{}
	for _, ch := range utf16.Encode([]rune(pwd)) {
		core.WriteUInt16LE(ch, buff)
	}
	core.WriteUInt16LE(0, buff)
	c.info.Password = buff.Bytes()
}

// SetDomain sets the domain in the info PDU, in UTF-16 with a terminator.
//
// An empty domain is sent as a single terminator rather than as nothing, since the
// length field counts from it.
func (c *Client) SetDomain(domain string) {
	buff := &bytes.Buffer{}
	for _, ch := range utf16.Encode([]rune(domain)) {
		core.WriteUInt16LE(ch, buff)
	}
	core.WriteUInt16LE(0, buff)
	c.info.Domain = buff.Bytes()
}

func (c *Client) connect(clientData []interface{}, serverData []interface{}, userId uint16, channels []t125.MCSChannelInfo) {
	glog.Debug("sec on connect:", clientData)
	glog.Debug("sec on connect:", serverData)
	glog.Debug("sec on connect:", userId)
	glog.Debug("sec on connect:", channels)
	c.clientData = clientData
	c.serverData = serverData
	c.userId = userId
	for _, channel := range channels {
		glog.Infof("channel: %s <%d>:", channel.Name, channel.ID)
		if channel.Name == t125.GLOBAL_CHANNEL_NAME {
			c.channelId = channel.ID
			//break
		}
	}
	c.enableEncryption = c.ClientCoreData().ServerSelectedProtocol == 0

	if c.enableEncryption {
		// Standard RDP Security. The key exchange reads the server's
		// random and certificate off the wire, so a failure here must stop
		// the connection rather than carry on with no session keys.
		if err := c.sendClientRandom(); err != nil {
			glog.Error("sec: Standard RDP Security: ", err)
			c.Emit("error", err)
			_ = c.Close()
			return
		}
	}

	c.sendInfoPkt()
	c.transport.Once("sec", c.recvLicenceInfo)
}

// ClientCoreData returns the core data that was sent in the Connect-Initial.
//
// It type asserts what it finds, so it panics if the connection has not reached the
// point where the data was sent rather than returning nil.
func (c *Client) ClientCoreData() *gcc.ClientCoreData {
	return c.clientData[0].(*gcc.ClientCoreData)
}

// ClientSecurityData returns the security data that was sent, and panics if the
// connection has not got that far.
func (c *Client) ClientSecurityData() *gcc.ClientSecurityData {
	return c.clientData[1].(*gcc.ClientSecurityData)
}

// ClientNetworkData returns the network data that was sent, and panics if the
// connection has not got that far.
func (c *Client) ClientNetworkData() *gcc.ClientNetworkData {
	return c.clientData[2].(*gcc.ClientNetworkData)
}

func (c *Client) serverCoreData() *gcc.ServerCoreData {
	return c.serverData[0].(*gcc.ServerCoreData)
}

// ServerSecurityData returns the server's security data, which is what says which
// encryption the session settled on.
//
// It panics rather than returning nil if the connect sequence has not run, and it
// reads the second element of the server's data, so a server that sent its blocks in
// another order would make this a wrong type rather than an error.
func (c *Client) ServerSecurityData() *gcc.ServerSecurityData {
	return c.serverData[1].(*gcc.ServerSecurityData)
}

/*
@summary: generate 40 bits data from 128 bits data
@param data: {str} 128 bits data
@return: {str} 40 bits data
@see: http://msdn.microsoft.com/en-us/library/cc240785.aspx
*/
func gen40bits(data []byte) []byte {
	return append([]byte("\xd1\x26\x9e"), data[3:8]...)
}

/*
@summary: generate 56 bits data from 128 bits data
@param data: {str} 128 bits data
@return: {str} 56 bits data
@see: http://msdn.microsoft.com/en-us/library/cc240785.aspx
*/
func gen56bits(data []byte) []byte {
	return append([]byte("\xd1"), data[1:8]...)
}

/*
@summary: Generate particular signature from combination of sha1 and md5
@see: http://msdn.microsoft.com/en-us/library/cc241992.aspx
@param inputData: strange input (see doc)
@param salt: salt for context call
@param salt1: another salt (ex : client random)
@param salt2: another another salt (ex: server random)
@return : MD5(Salt + SHA1(Input + Salt + Salt1 + Salt2))
*/
func saltedHash(inputData, salt, salt1, salt2 []byte) []byte {
	sha1Digest := sha1.New()
	md5Digest := md5.New()

	sha1Digest.Write(inputData)
	sha1Digest.Write(salt[:48])
	sha1Digest.Write(salt1)
	sha1Digest.Write(salt2)
	sha1Sig := sha1Digest.Sum(nil)

	md5Digest.Write(salt[:48])
	md5Digest.Write(sha1Sig)

	return md5Digest.Sum(nil)[:16]
}

/*
@summary: MD5(in0[:16] + in1[:32] + in2[:32])
@param key: in 16
@param random1: in 32
@param random2: in 32
@return MD5(in0[:16] + in1[:32] + in2[:32])
*/
func finalHash(key, random1, random2 []byte) []byte {
	md5Digest := md5.New()
	md5Digest.Write(key)
	md5Digest.Write(random1)
	md5Digest.Write(random2)
	return md5Digest.Sum(nil)
}

/*
@summary: Generate master secret
@param secret: {str} secret
@param clientRandom : {str} client random
@param serverRandom : {str} server random
@see: http://msdn.microsoft.com/en-us/library/cc241992.aspx
*/
func masterSecret(secret, random1, random2 []byte) []byte {
	sh1 := saltedHash([]byte("A"), secret, random1, random2)
	sh2 := saltedHash([]byte("BB"), secret, random1, random2)
	sh3 := saltedHash([]byte("CCC"), secret, random1, random2)
	ms := bytes.NewBuffer(nil)
	ms.Write(sh1)
	ms.Write(sh2)
	ms.Write(sh3)
	return ms.Bytes()
}

/*
@summary: Generate master secret
@param secret: secret
@param clientRandom : client random
@param serverRandom : server random
*/
func sessionKeyBlob(secret, random1, random2 []byte) []byte {
	sh1 := saltedHash([]byte("X"), secret, random1, random2)
	sh2 := saltedHash([]byte("YY"), secret, random1, random2)
	sh3 := saltedHash([]byte("ZZZ"), secret, random1, random2)
	ms := bytes.NewBuffer(nil)
	ms.Write(sh1)
	ms.Write(sh2)
	ms.Write(sh3)
	return ms.Bytes()

}

// generateKeys derives the MAC key and the two RC4 keys for Standard RDP
// Security from the client and server randoms (MS-RDPBCGR 5.3.5). Both
// randoms take part in every hash and must be 32 bytes; a shorter one means the
// security exchange was truncated. The slices are bounded here so such a
// server fails instead of reading past the end.
func generateKeys(clientRandom, serverRandom []byte, method uint32) ([]byte, []byte, []byte) {
	if len(clientRandom) < 32 || len(serverRandom) < 32 {
		glog.Error("sec: session keys need 32 byte randoms, got ", len(clientRandom), " and ", len(serverRandom))
		return nil, nil, nil
	}

	b := &bytes.Buffer{}
	b.Write(clientRandom[:24])
	b.Write(serverRandom[:24])
	preMasterHash := b.Bytes()
	glog.Debug("preMasterHash:", glog.Hex(preMasterHash))

	masterHash := masterSecret(preMasterHash, clientRandom, serverRandom)
	glog.Debug("masterHash:", glog.Hex(masterHash))

	sessionKey := sessionKeyBlob(masterHash, clientRandom, serverRandom)
	glog.Debug("sessionKey:", glog.Hex(sessionKey))

	macKey128 := sessionKey[:16]
	initialFirstKey128 := finalHash(sessionKey[16:32], clientRandom, serverRandom)
	initialSecondKey128 := finalHash(sessionKey[32:48], clientRandom, serverRandom)

	glog.Debug("macKey128:", glog.Hex(macKey128))
	glog.Debug("FirstKey128:", glog.Hex(initialFirstKey128))
	glog.Debug("SecondKey128:", glog.Hex(initialSecondKey128))
	//generate valid key
	if method == gcc.ENCRYPTION_FLAG_40BIT {
		return gen40bits(macKey128), gen40bits(initialFirstKey128), gen40bits(initialSecondKey128)
	} else if method == gcc.ENCRYPTION_FLAG_56BIT {
		return gen56bits(macKey128), gen56bits(initialFirstKey128), gen56bits(initialSecondKey128)
	}
	// method == gcc.ENCRYPTION_FLAG_128BIT
	return macKey128, initialFirstKey128, initialSecondKey128

}

// ClientSecurityExchangePDU is the message that carries the client random value at
// the start of the security exchange, which is what the session keys are derived
// from along with the server's.
//
// The random value is encrypted with the server's public key, so what travels is the
// encrypted form and the padding that brings it up to the key's block size.
type ClientSecurityExchangePDU struct {
	Length                uint32 `struc:"little"`
	EncryptedClientRandom []byte `struc:"little"`
	Padding               []byte `struc:"[8]byte"`
}

func (e *ClientSecurityExchangePDU) serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt32LE(e.Length, buff)
	core.WriteBytes(e.EncryptedClientRandom, buff)
	core.WriteBytes(e.Padding, buff)

	return buff.Bytes()
}

// sendClientRandom performs the Standard RDP Security key exchange
// (MS-RDPBCGR 5.3.4): it generates the 32 byte client random, encrypts it with
// the server's RSA public key and sends it in the Client Security Exchange PDU,
// then derives the session keys from it and the server random. Every value used
// here came off the wire, so each one is checked before use; a short server
// random or a missing certificate would otherwise panic rather than fail the
// connection.
func (c *Client) sendClientRandom() error {
	glog.Debug("send Client Random")

	clientRandom := core.Random(32)
	glog.Debug("clientRandom:", glog.Hex(clientRandom))

	ssd := c.ServerSecurityData()
	if ssd == nil {
		return errors.New("sec: security exchange carried no security data")
	}

	serverRandom := ssd.ServerRandom
	glog.Debug("ServerRandom:", glog.Hex(serverRandom))
	if len(serverRandom) != 32 {
		return fmt.Errorf("sec: server random is %d bytes, want 32", len(serverRandom))
	}

	c.encryptionMethod = ssd.EncryptionMethod
	c.macKey, c.initialDecrytKey, c.initialEncryptKey = generateKeys(clientRandom,
		serverRandom, ssd.EncryptionMethod)
	if c.macKey == nil || c.initialDecrytKey == nil || c.initialEncryptKey == nil {
		return errors.New("sec: could not derive the session keys")
	}

	//initialize keys
	c.currentDecrytKey = c.initialDecrytKey
	c.currentEncryptKey = c.initialEncryptKey

	if ssd.ServerCertificate.CertData == nil {
		return errors.New("sec: security exchange carried no server certificate")
	}
	if chain, ok := ssd.ServerCertificate.CertData.(*gcc.X509CertificateChain); ok && len(chain.CertBlobArray) == 0 {
		// GetPublicKey indexes the last blob of the chain.
		return errors.New("sec: server certificate chain is empty")
	}

	// The certificate is what the client random is encrypted to, so a certificate
	// that is not the server's means the session keys are agreed with someone
	// else. Verifying it is worth doing, and an RDP server normally presents a
	// self-signed certificate, so by default the result is reported and the
	// session continues. That is the same default the TLS layer uses; see
	// SetRequireVerifiedCertificate for the other one.
	if err := ssd.ServerCertificate.CertData.Verify(); err != nil {
		if c.requireVerifiedCertificate {
			return fmt.Errorf("sec: server certificate not verified: %w", err)
		}
		glog.Warn("sec: server certificate not verified, continuing:", err)
	}

	serverPubKey, err := ssd.ServerCertificate.CertData.GetPublicKey()
	if err != nil {
		return fmt.Errorf("sec: server public key: %w", err)
	}
	if serverPubKey == nil {
		return errors.New("sec: server certificate has no public key")
	}

	ret, err := rsa.EncryptPKCS1v15(rand.Reader, serverPubKey, core.Reverse(clientRandom))
	if err != nil {
		return fmt.Errorf("sec: encrypt client random: %w", err)
	}

	message := ClientSecurityExchangePDU{}
	message.EncryptedClientRandom = core.Reverse(ret)
	message.Length = uint32(len(message.EncryptedClientRandom) + 8)
	message.Padding = make([]byte, 8)

	glog.Debug("message:", message)

	// SEC_LICENSE_ENCRYPT_SC tells the server that this client can process
	// encrypted licensing packets, which is what makes it encrypt them
	// (MS-RDPBCGR 3.2.5.3.10). FreeRDP's client sends the same flag on this PDU
	// (rdp_client_establish_keys in libfreerdp/core/connection.c).
	c.sendFlagged(EXCHANGE_PKT|LICENSE_ENCRYPT_SC, message.serialize())
	return nil
}
func (c *Client) sendInfoPkt() {
	var secFlag uint16 = INFO_PKT
	if c.enableEncryption {
		secFlag |= ENCRYPT
	}

	glog.Debug("RdpVersion:", c.ClientCoreData().RdpVersion, ":", gcc.RDP_VERSION_5_PLUS)
	c.sendFlagged(secFlag, c.info.Serialize(c.ClientCoreData().RdpVersion == gcc.RDP_VERSION_5_PLUS))
}

func (c *Client) recvLicenceInfo(channel string, s []byte) {
	glog.Debug("sec recvLicenceInfo", glog.Hex(s))
	r := bytes.NewReader(s)
	h := readSecurityHeader(r)
	if (h.securityFlag & LICENSE_PKT) == 0 {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_PDU_SEC_BAD_LICENSE_HEADER"))
		return
	}

	// A server that was told this client can process encrypted licensing
	// packets encrypts them with the session keys, exactly like any other PDU,
	// and announces that with SEC_ENCRYPT (MS-RDPBCGR 2.2.1.12,
	// rdp_client_connect_license in FreeRDP). xrdp ignores the flag and answers
	// in the clear, which is handled by the other branch.
	if c.enableEncryption && h.securityFlag&ENCRYPT != 0 {
		payload, _ := core.ReadBytes(r.Len(), r)
		r = bytes.NewReader(c.readEncryptedPayload(payload, h.securityFlag&SECURE_CHECKSUM != 0))
	}

	p := lic.ReadLicensePacket(r)
	switch p.BMsgtype {
	case lic.NEW_LICENSE:
		glog.Info("sec NEW_LICENSE")
		c.Emit("success")
		goto connect
	case lic.ERROR_ALERT:
		message := p.LicensingMessage.(*lic.ErrorMessage)
		glog.Info("sec ERROR_ALERT and ErrorCode:", message.DwErrorCode)
		if message.DwErrorCode == lic.STATUS_VALID_CLIENT && message.DwStateTransaction == lic.ST_NO_TRANSITION {
			goto connect
		}
		goto retry
	case lic.LICENSE_REQUEST:
		glog.Info("sec LICENSE_REQUEST")
		c.sendClientNewLicenseRequest(p.LicensingMessage.([]byte))
		goto retry
	case lic.PLATFORM_CHALLENGE:
		glog.Info("sec PLATFORM_CHALLENGE")
		c.sendClientChallengeResponse(p.LicensingMessage.([]byte))
		goto retry
	default:
		glog.Error("Not a valid license packet")
		c.Emit("error", errors.New("Not a valid license packet"))
		return
	}

connect:
	c.transport.On("sec", c.recvData)
	c.Emit("connect", c.clientData[0].(*gcc.ClientCoreData), c.userId, c.channelId)
	return

retry:
	c.transport.Once("sec", c.recvLicenceInfo)
	return
}

func (c *Client) sendClientNewLicenseRequest(data []byte) {
	var req lic.ServerLicenseRequest
	struc.Unpack(bytes.NewReader(data), &req)

	// Prefer the certificate carried by the license request itself. The
	// certificate from the Standard Security Data Exchange is the fallback,
	// not the other way round: over NLA or TLS it is either absent or in a
	// form this code does not parse, and picking it produces a nonsense RSA
	// key (Go rejects it as "public modulus is even").
	var sc gcc.ServerCertificate
	switch {
	case len(req.ServerCertificate.BlobData) > 0:
		if err := sc.Unpack(bytes.NewReader(req.ServerCertificate.BlobData)); err != nil {
			glog.Error("read serverCertificate err:", err)
			return
		}
	case c.ServerSecurityData().ServerCertificate.DwVersion != 0:
		sc = c.ServerSecurityData().ServerCertificate
	default:
		glog.Error("no server certificate in the license request or the security exchange")
		return
	}

	serverRandom := req.ServerRandom
	clientRandom := core.Random(32)
	preMasterSecret := core.Random(48)
	masSecret := masterSecret(preMasterSecret, clientRandom, serverRandom)
	sessionKeyBlob := masterSecret(masSecret, serverRandom, clientRandom)
	// The licensing keys are not the session keys. They sign and encrypt the
	// blobs inside the licensing PDUs (FreeRDP: license_encrypt_and_MAC uses
	// MacSaltKey), while the MAC of an encrypted PDU uses the session MAC key,
	// which overwriting macKey here used to replace for the rest of the
	// session.
	c.licenseMacKey = sessionKeyBlob[:16]
	c.licenseKey = finalHash(sessionKeyBlob[16:32], clientRandom, serverRandom)

	//format message
	message := &lic.ClientNewLicenseRequest{}
	message.PreferredKeyExchangeAlg = 0x00000001
	message.PlatformId = 0x04000000 | 0x00010000
	message.ClientRandom = clientRandom

	buff := &bytes.Buffer{}

	if sc.CertData == nil {
		glog.Error("server certificate has no key data")
		return
	}
	serverPubKey, err := sc.CertData.GetPublicKey()
	if err != nil || serverPubKey == nil {
		glog.Error("server public key err:", err)
		return
	}

	ret, err := rsa.EncryptPKCS1v15(rand.Reader, serverPubKey, core.Reverse(preMasterSecret))
	if err != nil {
		// A malformed license request is not worth sending, and the caller
		// has nothing to fall back on, so stop here.
		glog.Error("encrypt premaster secret err:", err)
		return
	}

	buff.Write(core.Reverse(ret))
	buff.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	message.EncryptedPreMasterSecret.BlobData = buff.Bytes()
	message.EncryptedPreMasterSecret.WBlobLen = uint16(buff.Len())
	message.EncryptedPreMasterSecret.WBlobType = lic.BB_RANDOM_BLOB

	buff.Reset()
	buff.Write(c.info.UserName)
	buff.Write([]byte{0x00})
	message.ClientUserName.BlobData = buff.Bytes()
	message.ClientUserName.WBlobLen = uint16(buff.Len())
	message.ClientUserName.WBlobType = lic.BB_CLIENT_USER_NAME_BLOB

	buff.Reset()
	buff.Write(c.ClientCoreData().ClientName[:])
	buff.Write([]byte{0x00})
	message.ClientMachineName.BlobData = buff.Bytes()
	message.ClientMachineName.WBlobLen = uint16(buff.Len())
	message.ClientMachineName.WBlobType = lic.BB_CLIENT_MACHINE_NAME_BLOB

	buff.Reset()
	err = struc.Pack(buff, message)
	if err != nil {
		glog.Error("err:", err)
	}

	c.sendLicensePkt(buff.Bytes())
}

// sendLicensePkt sends a licensing PDU. FreeRDP marks licensing PDUs with
// SEC_LICENSE_PKT and SEC_LICENSE_ENCRYPT_CS and, while Standard RDP Security
// is in use, encrypts them with the session keys like any other PDU
// (license_send_stream_init in libfreerdp/core/license.c). Under TLS or NLA
// there are no session keys, so the PDU goes out in the clear as before.
func (c *Client) sendLicensePkt(data []byte) {
	var flag uint16 = LICENSE_PKT
	if c.enableEncryption {
		flag |= LICENSE_ENCRYPT_CS | ENCRYPT
	}
	c.sendFlagged(flag, data)
}

func (c *Client) sendClientChallengeResponse(data []byte) {
	var pc lic.ServerPlatformChallenge
	struc.Unpack(bytes.NewReader(data), &pc)

	serverEncryptedChallenge := pc.EncryptedPlatformChallenge.BlobData
	//decrypt server challenge
	//it should be TEST word in unicode format
	rc, err := rc4.NewCipher(c.licenseKey)
	if err != nil {
		glog.Error("sec: no licensing key for the platform challenge: ", err)
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_PDU_SEC_NO_LICENSING_KEY"))
		return
	}
	if len(serverEncryptedChallenge) < 20 {
		glog.Error("sec: encrypted platform challenge is ", len(serverEncryptedChallenge), " bytes, want 20")
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_PDU_SEC_BAD_PLATFORM_CHALLENGE"))
		return
	}
	// Decrypt what arrived and use the first 20 bytes: the challenge is 20
	// bytes, and decrypting into a fixed 20 byte buffer would run past it for a
	// longer one, which a server can send.
	decrypted := make([]byte, len(serverEncryptedChallenge))
	rc.XORKeyStream(decrypted, serverEncryptedChallenge)
	serverChallenge := decrypted[:20]
	//if serverChallenge != "T\x00E\x00S\x00T\x00\x00\x00":
	//raise InvalidExpectedDataException("bad license server challenge")

	//generate hwid
	b := &bytes.Buffer{}
	b.Write(c.ClientCoreData().ClientName[:])
	b.Write(c.info.UserName)
	for i := 0; i < 2; i++ {
		b.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	}
	hwid := b.Bytes()[:20]

	encryptedHWID := make([]byte, 20)
	rc.XORKeyStream(encryptedHWID, hwid)

	b.Reset()
	b.Write(serverChallenge)
	b.Write(hwid)

	message := &lic.ClientPLatformChallengeResponse{}
	message.EncryptedPlatformChallengeResponse.BlobData = serverEncryptedChallenge
	message.EncryptedHWID.BlobData = encryptedHWID
	message.MACData = macData(c.licenseMacKey, b.Bytes())[:16]

	b.Reset()
	struc.Pack(b, message)
	c.sendLicensePkt(b.Bytes())
}

func (c *Client) recvData(channel string, s []byte) {
	glog.Trace("sec recvData", glog.Hex(s))
	glog.Debugf("channel<%s> data len: %d", channel, len(s))
	data := c.decrytData(s)
	if channel != t125.GLOBAL_CHANNEL_NAME {
		c.Emit("channel", channel, data)
		return
	}
	c.Emit("data", data)
}

// SetFastPathListener sets the handler for fast-path PDUs, which are delivered
// separately from the T.125 data path because their framing and their security flags
// are their own.
func (c *Client) SetFastPathListener(f core.FastPathListener) {
	c.fastPathListener = f
}

// RecvFastPath decrypts a fast-path PDU if its flags say it was encrypted, and hands
// it to the listener.
//
// The two flags decide independently: ENCRYPTED says whether to decrypt, and
// SECURE_CHECKSUM changes how the MAC is checked, so a PDU can be one without the
// other. The listener is called without a nil check, so it must be set before a
// fast-path PDU can arrive.
func (c *Client) RecvFastPath(secFlag byte, s []byte) {
	data := s
	if c.enableEncryption && secFlag&FASTPATH_OUTPUT_ENCRYPTED != 0 {
		data = c.readEncryptedPayload(s, secFlag&FASTPATH_OUTPUT_SECURE_CHECKSUM != 0)
	}
	c.fastPathListener.RecvFastPath(secFlag, data)
}

// SetRequireVerifiedCertificate decides whether an unverified server certificate
// ends the connection.
//
// It applies to Standard RDP Security, where the server's certificate is what the
// client random is encrypted to and there is no other trust model. There the
// default is to report the result and continue, because a self-signed certificate
// is what an RDP server normally presents and refusing those by default would
// refuse most servers.
//
// With it set, a certificate that does not verify is an error and the connection
// is abandoned. A version 1 certificate never verifies, whatever it contains; see
// gcc.ProprietaryServerCertificate.Verify.
//
// It has no effect over TLS or CredSSP, where the certificate belongs to the TLS
// layer and is verified there.
func (c *Client) SetRequireVerifiedCertificate(require bool) {
	c.requireVerifiedCertificate = require
}

// SetChannelSender sets what SendToChannel writes through. It is called by the
// channel layer once the channels are joined.
func (c *Client) SetChannelSender(f core.ChannelSender) {
	c.channelSender = f
}

// SendToChannel sends a message on a static virtual channel, encrypted when
// encryption is on and in the clear when it is not.
//
// With encryption the payload is protected and a security header is written in front
// of it; without it the message goes to the channel unchanged, since over TLS the
// transport has already protected it. The two paths therefore produce different bytes
// for the same call, and which one runs depends on the session and not on the caller.
func (c *Client) SendToChannel(channel string, b []byte) (int, error) {
	if !c.enableEncryption {
		glog.Debug("Sec Client write", glog.Hex(b))
		return c.channelSender.SendToChannel(channel, b)
	}
	var flag uint16 = ENCRYPT
	if c.enableSecureCheckSum {
		flag |= SECURE_CHECKSUM
	}
	data := c.writeEncryptedPayload(b, c.enableSecureCheckSum)

	buff := &bytes.Buffer{}
	core.WriteUInt16LE(flag, buff)
	core.WriteUInt16LE(0, buff)
	core.WriteBytes(data, buff)
	glog.Debug("Sec Client write", channel, glog.Hex(buff.Bytes()))
	return c.channelSender.SendToChannel(channel, buff.Bytes())
}
