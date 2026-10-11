package gcc

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/adfnekc/grdp/glog"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/protocol/t125/per"
	"github.com/lunixbochs/struc"
)

var t124_02_98_oid = []byte{0, 0, 20, 124, 0, 1}
var h221_cs_key = "Duca"
var h221_sc_key = "McDn"

/**
 * @see http://msdn.microsoft.com/en-us/library/cc240509.aspx
 */
// Message is the type of a GCC conference PDU, which is what the Connect-Initial
// and Connect-Response carry as their user data. The two directions have separate
// ranges, so a value says which end sent it as well as what it is.
type Message uint16

// The conference message types: the three the server sends and the five a client
// can send. This client sends core, security and network, and adds cluster and
// monitor only when a monitor layout is being requested.
const (
	//server -> client
	SC_CORE     Message = 0x0C01
	SC_SECURITY         = 0x0C02
	SC_NET              = 0x0C03
	//client -> server
	CS_CORE     = 0xC001
	CS_SECURITY = 0xC002
	CS_NET      = 0xC003
	CS_CLUSTER  = 0xC004
	CS_MONITOR  = 0xC005
)

// ColorDepth is the colour depth a client asks for in its core data, as one of
// the RNS_UD_COLOR values. It names a colour format rather than a bit count,
// which is why the two 16 bit entries are separate: 555 and 565 have different
// bit layouts and a client that picks the wrong one gets the wrong colours.
//
// See MS-RDPBCGR 2.2.1.3.2.
type ColorDepth uint16

// The colour depths a client can ask for.
const (
	RNS_UD_COLOR_8BPP      ColorDepth = 0xCA01
	RNS_UD_COLOR_16BPP_555            = 0xCA02
	RNS_UD_COLOR_16BPP_565            = 0xCA03
	RNS_UD_COLOR_24BPP                = 0xCA04
)

// HighColor is the colour depth in the server's core data, which is a different
// enumeration from the one a client asks with: these are bit counts, where
// ColorDepth above is a format name for the same idea. Comparing the two directly
// is a mistake the values invite.
type HighColor uint16

// The colour depths a server can report.
const (
	HIGH_COLOR_4BPP  HighColor = 0x0004
	HIGH_COLOR_8BPP            = 0x0008
	HIGH_COLOR_15BPP           = 0x000f
	HIGH_COLOR_16BPP           = 0x0010
	HIGH_COLOR_24BPP           = 0x0018
)

// Support holds the high colour flags, which say which colour depths the session
// can use. They travel beside the HighColor value they qualify.
type Support uint16

// The colour depths the session supports.
const (
	RNS_UD_24BPP_SUPPORT uint16 = 0x0001
	RNS_UD_16BPP_SUPPORT        = 0x0002
	RNS_UD_15BPP_SUPPORT        = 0x0004
	RNS_UD_32BPP_SUPPORT        = 0x0008
)

// CapabilityFlag holds the early capability flags, which a client advertises in
// its core data before any capability exchange has happened. They are what decides
// whether the server offers the features they ask about, so a flag missing here
// cannot be asked for later.
type CapabilityFlag uint16

// The early capability flags. RNS_UD_CS_WANT_32BPP_SESSION is what makes a 32 bit
// session possible at all, and RNS_UD_CS_SUPPORT_DYNVC_GFX_PROTOCOL is what the
// graphics channel and Display Control depend on.
const (
	RNS_UD_CS_SUPPORT_ERRINFO_PDU        uint16 = 0x0001
	RNS_UD_CS_WANT_32BPP_SESSION                = 0x0002
	RNS_UD_CS_SUPPORT_STATUSINFO_PDU            = 0x0004
	RNS_UD_CS_STRONG_ASYMMETRIC_KEYS            = 0x0008
	RNS_UD_CS_UNUSED                            = 0x0010
	RNS_UD_CS_VALID_CONNECTION_TYPE             = 0x0020
	RNS_UD_CS_SUPPORT_MONITOR_LAYOUT_PDU        = 0x0040
	RNS_UD_CS_SUPPORT_NETCHAR_AUTODETECT        = 0x0080
	RNS_UD_CS_SUPPORT_DYNVC_GFX_PROTOCOL        = 0x0100
	RNS_UD_CS_SUPPORT_DYNAMIC_TIME_ZONE         = 0x0200
	RNS_UD_CS_SUPPORT_HEARTBEAT_PDU             = 0x0400
)

// ConnectionType is the connection type a client claims, which the server uses to
// pick a bandwidth profile. It is a claim rather than a measurement.
type ConnectionType uint8

// The connection types. AUTODETECT asks the server to work it out, which
// RNS_UD_CS_SUPPORT_NETCHAR_AUTODETECT is the flag for.
const (
	CONNECTION_TYPE_MODEM          ConnectionType = 0x01
	CONNECTION_TYPE_BROADBAND_LOW                 = 0x02
	CONNECTION_TYPE_SATELLITEV                    = 0x03
	CONNECTION_TYPE_BROADBAND_HIGH                = 0x04
	CONNECTION_TYPE_WAN                           = 0x05
	CONNECTION_TYPE_LAN                           = 0x06
	CONNECTION_TYPE_AUTODETECT                    = 0x07
)

// VERSION is the RDP version in the core data. The two values are four byte
// integers that read as version 8.1 and 8.4, which is not what they mean: the
// encoding is its own thing.
type VERSION uint32

// The two versions a client can claim. Everything current is RDP_VERSION_5_PLUS.
const (
	RDP_VERSION_4      VERSION = 0x00080001
	RDP_VERSION_5_PLUS         = 0x00080004
)

// Sequence is the SAS sequence in the client security data, which is the
// Ctrl+Alt+Delete sequence a server should expect.
type Sequence uint16

// The SAS sequence a client sends.
const (
	RNS_UD_SAS_DEL Sequence = 0xAA03
)

// EncryptionMethod holds the encryption methods in the server's security data, as
// flags rather than as a single choice: a server can offer several and report all
// of them, with the one actually in use given by EncryptionLevel.
type EncryptionMethod uint32

// The encryption methods, as flags.
const (
	ENCRYPTION_FLAG_40BIT  uint32 = 0x00000001
	ENCRYPTION_FLAG_128BIT        = 0x00000002
	ENCRYPTION_FLAG_56BIT         = 0x00000008
	FIPS_ENCRYPTION_FLAG          = 0x00000010
)

// EncryptionLevel is the encryption level in the server's security data, which is
// the one actually in use. ENCRYPTION_LEVEL_NONE is what a session over TLS or
// CredSSP reports, because the RDP layer then does no encryption of its own.
type EncryptionLevel uint32

// The encryption levels.
const (
	ENCRYPTION_LEVEL_NONE              EncryptionLevel = 0x00000000
	ENCRYPTION_LEVEL_LOW                               = 0x00000001
	ENCRYPTION_LEVEL_CLIENT_COMPATIBLE                 = 0x00000002
	ENCRYPTION_LEVEL_HIGH                              = 0x00000003
	ENCRYPTION_LEVEL_FIPS                              = 0x00000004
)

// ChannelOptions holds the flags on a static virtual channel, which a client sets
// when it asks for the channel. They say how the channel's traffic is treated:
// whether it is encrypted, what priority it gets, and whether it may be
// compressed.
type ChannelOptions uint32

// The channel options. INITIALIZED is set on every channel a client asks for, and
// is what a server reads as "this channel is wanted". ENCRYPT_RDP asks for the
// channel to be encrypted like the rest of the session, and the priority flags
// choose between three fixed levels rather than a range.
const (
	CHANNEL_OPTION_INITIALIZED   ChannelOptions = 0x80000000
	CHANNEL_OPTION_ENCRYPT_RDP                  = 0x40000000
	CHANNEL_OPTION_ENCRYPT_SC                   = 0x20000000
	CHANNEL_OPTION_ENCRYPT_CS                   = 0x10000000
	CHANNEL_OPTION_PRI_HIGH                     = 0x08000000
	CHANNEL_OPTION_PRI_MED                      = 0x04000000
	CHANNEL_OPTION_PRI_LOW                      = 0x02000000
	CHANNEL_OPTION_COMPRESS_RDP                 = 0x00800000
	CHANNEL_OPTION_COMPRESS                     = 0x00400000
	CHANNEL_OPTION_SHOW_PROTOCOL                = 0x00200000
	REMOTE_CONTROL_PERSISTENT                   = 0x00100000
)

// KeyboardType is the keyboard type a client reports in its core data.
type KeyboardType uint32

// The keyboard types, in the order the specification lists them. KT_IBM_101_102_KEYS
// is the one that is almost always right, including for keyboards that do not
// physically match it: it is what other clients send, and the server uses it to
// decide how to interpret scancodes.
const (
	KT_IBM_PC_XT_83_KEY KeyboardType = 0x00000001
	KT_OLIVETTI                      = 0x00000002
	KT_IBM_PC_AT_84_KEY              = 0x00000003
	KT_IBM_101_102_KEYS              = 0x00000004
	KT_NOKIA_1050                    = 0x00000005
	KT_NOKIA_9140                    = 0x00000006
	KT_JAPANESE                      = 0x00000007
)

// KeyboardLayout is the keyboard layout a client reports, which is the Windows
// locale id rather than a layout number of its own. The server uses it to decide
// what a scancode produces when the layout matters, which is what makes a
// non-English keyboard behave.
//
// A layout that is not in this list is not a protocol problem: the field is a
// Windows LCID, so any of them can be sent. These are the ones a client is likely
// to want a name for.
type KeyboardLayout uint32

// Keyboard layouts, by Windows locale id.
const (
	ARABIC              KeyboardLayout = 0x00000401
	BULGARIAN                          = 0x00000402
	CHINESE_US_KEYBOARD                = 0x00000404
	CZECH                              = 0x00000405
	DANISH                             = 0x00000406
	GERMAN                             = 0x00000407
	GREEK                              = 0x00000408
	US                                 = 0x00000409
	SPANISH                            = 0x0000040a
	FINNISH                            = 0x0000040b
	FRENCH                             = 0x0000040c
	HEBREW                             = 0x0000040d
	HUNGARIAN                          = 0x0000040e
	ICELANDIC                          = 0x0000040f
	ITALIAN                            = 0x00000410
	JAPANESE                           = 0x00000411
	KOREAN                             = 0x00000412
	DUTCH                              = 0x00000413
	NORWEGIAN                          = 0x00000414
)

// CertificateType is the version of the certificate chain in the server's
// certificate data.
type CertificateType uint32

// The two certificate chain versions. Version 1 is the older RSA-only form that
// the proprietary certificate uses, and version 2 is a real X.509 chain, which is
// what a current server sends.
const (
	CERT_CHAIN_VERSION_1 CertificateType = 0x00000001
	CERT_CHAIN_VERSION_2                 = 0x00000002
)

// ChannelDef is one static virtual channel as the client asks for it: the name,
// which is fixed at eight bytes on the wire and padded with zeroes, and the
// options the channel is to be opened with.
type ChannelDef struct {
	Name    string `struc:"little"`
	Options uint32 `struc:"little"`
}

// ClientCoreData is the first thing a client sends: the desktop size, the colour
// depth, the keyboard and the early capability flags.
//
// It is read by the server before any capability exchange, so a value that is
// wrong here cannot be corrected later. The fields that look redundant are not:
// ColorDepth and PostBeta2ColorDepth are separate fields in the message, and
// HighColorDepth and SupportedColorDepths qualify each other.
//
// See MS-RDPBCGR 2.2.1.3.2.
type ClientCoreData struct {
	RdpVersion             VERSION        `struc:"uint32,little"`
	DesktopWidth           uint16         `struc:"little"`
	DesktopHeight          uint16         `struc:"little"`
	ColorDepth             ColorDepth     `struc:"little"`
	SasSequence            Sequence       `struc:"little"`
	KbdLayout              KeyboardLayout `struc:"little"`
	ClientBuild            uint32         `struc:"little"`
	ClientName             [32]byte       `struc:"[32]byte"`
	KeyboardType           uint32         `struc:"little"`
	KeyboardSubType        uint32         `struc:"little"`
	KeyboardFnKeys         uint32         `struc:"little"`
	ImeFileName            [64]byte       `struc:"[64]byte"`
	PostBeta2ColorDepth    ColorDepth     `struc:"little"`
	ClientProductId        uint16         `struc:"little"`
	SerialNumber           uint32         `struc:"little"`
	HighColorDepth         HighColor      `struc:"little"`
	SupportedColorDepths   uint16         `struc:"little"`
	EarlyCapabilityFlags   uint16         `struc:"little"`
	ClientDigProductId     [64]byte       `struc:"[64]byte"`
	ConnectionType         uint8          `struc:"uint8"`
	Pad1octet              uint8          `struc:"uint8"`
	ServerSelectedProtocol uint32         `struc:"little"`
}

// NewClientCoreData returns core data filled in with the values a client
// normally sends: 1280x800, the host's name, and the early capability flag for
// error info.
//
// The colour depth fields are set to 8bpp while the high colour depth is 24bpp
// and all four depths are advertised as supported, which is what other clients
// send: the older fields are what a server falls back to, and the flags are what
// it actually chooses from.
func NewClientCoreData() *ClientCoreData {
	name, _ := os.Hostname()
	var ClientName [32]byte
	copy(ClientName[:], core.UnicodeEncode(name)[:])
	return &ClientCoreData{
		RDP_VERSION_5_PLUS, 1280, 800, RNS_UD_COLOR_8BPP,
		RNS_UD_SAS_DEL, US, 3790, ClientName, KT_IBM_101_102_KEYS,
		0, 12, [64]byte{}, RNS_UD_COLOR_8BPP, 1, 0, HIGH_COLOR_24BPP,
		RNS_UD_15BPP_SUPPORT | RNS_UD_16BPP_SUPPORT | RNS_UD_24BPP_SUPPORT | RNS_UD_32BPP_SUPPORT,
		RNS_UD_CS_SUPPORT_ERRINFO_PDU, [64]byte{}, 0, 0, 0}
}

// Pack returns the core data as it goes on the wire: the message type and a
// length that includes them, then the structure.
//
// The length is a fixed 0xd8 rather than a computed one, because the structure is
// fixed size. A field added to it without changing this length would be sent and
// then ignored.
func (data *ClientCoreData) Pack() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt16LE(CS_CORE, buff) // 01C0
	core.WriteUInt16LE(0xd8, buff)    // d800
	struc.Pack(buff, data)
	return buff.Bytes()
}

// ClientNetworkData is the client's list of static virtual channels, which is what
// asks the server to create them. A channel that is not in this list cannot be
// joined later.
type ClientNetworkData struct {
	ChannelCount    uint32
	ChannelDefArray []ChannelDef
}

// NewClientNetworkData returns an empty channel list. Channels are added with
// AddVirtualChannel.
func NewClientNetworkData() *ClientNetworkData {
	n := &ClientNetworkData{ChannelDefArray: make([]ChannelDef, 0, 100)}
	return n
}

// AddVirtualChannel adds a channel to the list. The name is truncated to eight
// bytes by Pack, so a longer name is not an error here and would be a mismatched
// channel on the wire.
func (n *ClientNetworkData) AddVirtualChannel(name string, option uint32) {
	var d ChannelDef
	d.Name = name
	d.Options = option
	n.ChannelDefArray = append(n.ChannelDefArray, d)
	n.ChannelCount++
}

// Pack returns the channel list: the message type, a length, the count, and then
// twelve bytes per channel, which is the name padded to eight and four bytes of
// options.
//
// The length counts the eight byte header as well as the entries, which is what
// makes it ChannelCount*12+8 rather than ChannelCount*12.
func (n *ClientNetworkData) Pack() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt16LE(CS_NET, buff) // type
	length := uint16(n.ChannelCount*12 + 8)
	core.WriteUInt16LE(length, buff) // len 8
	core.WriteUInt32LE(n.ChannelCount, buff)
	for i := 0; i < int(n.ChannelCount); i++ {
		v := n.ChannelDefArray[i]
		name := make([]byte, 8)
		copy(name, []byte(v.Name))
		core.WriteBytes(name[:], buff)
		core.WriteUInt32LE(v.Options, buff)
	}
	return buff.Bytes()
}

// ClientSecurityData holds the encryption the client supports, as flags.
//
// ExtEncryptionMethods is the second field of the same message and is zero for
// ordinary RDP. It exists because the two fields together are what a server reads
// when it decides whether it can use FIPS encryption.
type ClientSecurityData struct {
	EncryptionMethods    uint32
	ExtEncryptionMethods uint32
}

// NewClientSecurityData returns security data offering all three of the
// non-FIPS encryption methods, which is what every client offers and what a
// server chooses from.
func NewClientSecurityData() *ClientSecurityData {
	return &ClientSecurityData{
		ENCRYPTION_FLAG_40BIT | ENCRYPTION_FLAG_56BIT | ENCRYPTION_FLAG_128BIT,
		00}
}

// Pack returns the security data with its message type and fixed length.
func (d *ClientSecurityData) Pack() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt16LE(CS_SECURITY, buff) // type
	core.WriteUInt16LE(0x0c, buff)        // len 12
	core.WriteUInt32LE(d.EncryptionMethods, buff)
	core.WriteUInt32LE(d.ExtEncryptionMethods, buff)
	return buff.Bytes()
}

// RSAPublicKey is the public key inside a proprietary certificate. Keylen counts
// the whole structure rather than the modulus, so the modulus is what is left of
// it after the eight bytes of the fields before, and the eight bytes of padding
// that follow.
//
// The modulus arrives least significant byte first, which is why GetPublicKey
// reverses it.
type RSAPublicKey struct {
	Magic   uint32 `struc:"little"` //0x31415352
	Keylen  uint32 `struc:"little,sizeof=Modulus"`
	Bitlen  uint32 `struc:"little"`
	Datalen uint32 `struc:"little"`
	PubExp  uint32 `struc:"little"`
	Modulus []byte `struc:"little"`
	Padding []byte `struc:"[8]byte"`
}

// ProprietaryServerCertificate is the older server certificate, which is an RSA
// public key and a signature rather than an X.509 chain. A current server sends
// the chain instead, so this is what parses the one case rather than the usual
// one.
type ProprietaryServerCertificate struct {
	DwSigAlgId        uint32       `struc:"little"` //0x00000001
	DwKeyAlgId        uint32       `struc:"little"` //0x00000001
	PublicKeyBlobType uint16       `struc:"little"` //0x0006
	PublicKeyBlobLen  uint16       `struc:"little,sizeof=PublicKeyBlob"`
	PublicKeyBlob     RSAPublicKey `struc:"little"`
	SignatureBlobType uint16       `struc:"little"` //0x0008
	SignatureBlobLen  uint16       `struc:"little,sizeof=SignatureBlob"`
	SignatureBlob     []byte       `struc:"little"`
	//PaddingLen        uint16       `struc:"little,sizeof=Padding,skip"`
	Padding []byte `struc:"[8]byte"`
}

// GetPublicKey returns the RSA public key from the certificate.
//
// The modulus is reversed because it arrives least significant byte first, and the
// public exponent is a plain integer. The error is always nil: nothing here can
// fail.
func (p *ProprietaryServerCertificate) GetPublicKey() (*rsa.PublicKey, error) {
	b := new(big.Int).SetBytes(core.Reverse(p.PublicKeyBlob.Modulus))
	e := new(big.Int).SetInt64(int64(p.PublicKeyBlob.PubExp))
	return &rsa.PublicKey{N: b, E: int(e.Int64())}, nil
}

// Verify reports that this certificate cannot be verified, which is the truth
// rather than a failure of this implementation.
//
// A version 1 certificate is a bare RSA public key with a signature blob and no
// chain: there is no issuer, no validity period and no trust anchor, so there is
// nothing to check it against. The signature blob is not verified either, because a
// signature with no issuer to name is not a statement about anything.
//
// A caller that needs the server authenticated should use TLS or CredSSP, which
// have a trust model. This returns an error so that it cannot be mistaken for a
// check that passed.
func (p *ProprietaryServerCertificate) Verify() error {
	return errors.New("gcc: a version 1 server certificate is a bare public key with no chain to verify against")
}

// Unpack reads the certificate. The lengths in it count eight bytes of padding
// that follow each blob, so the blobs are read as the length less eight with the
// padding taken separately, and a read error is not reported.
func (p *ProprietaryServerCertificate) Unpack(r io.Reader) error {
	p.DwSigAlgId, _ = core.ReadUInt32LE(r)
	p.DwKeyAlgId, _ = core.ReadUInt32LE(r)
	p.PublicKeyBlobType, _ = core.ReadUint16LE(r)
	p.PublicKeyBlobLen, _ = core.ReadUint16LE(r)
	var b RSAPublicKey
	b.Magic, _ = core.ReadUInt32LE(r)
	b.Keylen, _ = core.ReadUInt32LE(r)
	b.Bitlen, _ = core.ReadUInt32LE(r)
	b.Datalen, _ = core.ReadUInt32LE(r)
	b.PubExp, _ = core.ReadUInt32LE(r)
	b.Modulus, _ = core.ReadBytes(int(b.Keylen)-8, r)
	b.Padding, _ = core.ReadBytes(8, r)
	p.PublicKeyBlob = b
	p.SignatureBlobType, _ = core.ReadUint16LE(r)
	p.SignatureBlobLen, _ = core.ReadUint16LE(r)
	p.SignatureBlob, _ = core.ReadBytes(int(p.SignatureBlobLen)-8, r)
	p.Padding, _ = core.ReadBytes(8, r)

	return nil
}

// CertBlob is one certificate in an X.509 chain: a length and the DER bytes.
type CertBlob struct {
	CbCert uint32 `struc:"little,sizeof=AbCert"`
	AbCert []byte `struc:"little"`
}

// X509CertificateChain is the certificate chain a current server sends: one or
// more certificates and twelve bytes of padding.
type X509CertificateChain struct {
	NumCertBlobs  uint32     `struc:"little,sizeof=CertBlobArray"`
	CertBlobArray []CertBlob `struc:"little"`
	Padding       []byte     `struc:"[12]byte"`
}

// GetPublicKey returns the public key of the last certificate in the chain, which
// is the server's own: the others are the authorities that signed it.
//
// The key is normally already parsed by the standard library. The path for a
// certificate whose key it did not recognise parses the subject public key info by
// hand and expects a PKCS#1 key, so a certificate carrying the same key in another
// encoding fails here rather than working.
func (x *X509CertificateChain) GetPublicKey() (*rsa.PublicKey, error) {
	data := x.CertBlobArray[len(x.CertBlobArray)-1].AbCert
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		glog.Error("X509 ParseCertificate err:", err)
		return nil, err
	}
	var rsaPublicKey *rsa.PublicKey
	if cert.PublicKey == nil {
		var pubKeyInfo struct {
			Algorithm        pkix.AlgorithmIdentifier
			SubjectPublicKey asn1.BitString
		}
		_, err = asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &pubKeyInfo)
		if err != nil {
			return nil, err
		}
		rsaPublicKey, err = x509.ParsePKCS1PublicKey(pubKeyInfo.SubjectPublicKey.Bytes)
		if err != nil {
			return nil, err
		}
	} else {
		rsaPublicKey = cert.PublicKey.(*rsa.PublicKey)
	}

	return rsaPublicKey, nil
}

// Verify verifies the chain against the system's certificate authorities and reports
// whether the server's own certificate is trusted.
//
// The last blob is the server's certificate and the ones before it are the
// intermediates that signed it, which is the order the wire format uses and the
// order GetPublicKey relies on. The leaf is verified with the system roots and those
// intermediates, so a chain signed by a public certificate authority passes and a
// self-signed one does not, since there is nothing in the roots to anchor it.
//
// The server's name is not checked, because there is nothing here to check it
// against: this structure carries no name and RDP servers commonly present a
// certificate issued for a host that the client did not dial. A caller that needs
// the name checked should use TLS, which does it.
func (x *X509CertificateChain) Verify() error {
	if len(x.CertBlobArray) == 0 {
		return errors.New("gcc: the server certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(x.CertBlobArray[len(x.CertBlobArray)-1].AbCert)
	if err != nil {
		return fmt.Errorf("gcc: parse the server certificate: %w", err)
	}
	opts := x509.VerifyOptions{Intermediates: x509.NewCertPool()}
	for _, blob := range x.CertBlobArray[:len(x.CertBlobArray)-1] {
		cert, err := x509.ParseCertificate(blob.AbCert)
		if err != nil {
			// A chain with an unreadable intermediate cannot be verified through
			// it, and dropping it is the same as not having it.
			continue
		}
		opts.Intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("gcc: server certificate is not trusted: %w", err)
	}
	return nil
}

// Unpack reads the chain using the struct tags, which is what makes the length
// counted slices work.
func (x *X509CertificateChain) Unpack(r io.Reader) error {
	return struc.Unpack(r, x)
}

// ServerCoreData is the server's core data, which follows the client's in the
// response and is much smaller: it confirms the version and reports what the
// server decided about capabilities.
type ServerCoreData struct {
	RdpVersion              VERSION `struc:"uint32,little"`
	ClientRequestedProtocol uint32  `struc:"little"`
	EarlyCapabilityFlags    uint32  `struc:"little"`
}

// ScType reports which conference message this is, so that a reader can dispatch
// on a value read from the wire without constructing the type first.
func (d *ServerCoreData) ScType() Message {
	return SC_CORE
}

// Unpack reads the server's core data: the version it chose, the protocol the
// client asked for and its early capability flags.
func (d *ServerCoreData) Unpack(r io.Reader) error {
	version, _ := core.ReadUInt32LE(r)
	d.RdpVersion = VERSION(version)
	d.ClientRequestedProtocol, _ = core.ReadUInt32LE(r)
	d.EarlyCapabilityFlags, _ = core.ReadUInt32LE(r)

	return nil
	//return struc.Unpack(r, d)
}

// ServerNetworkData is the server's answer about channels: the id it gave the
// client's own channel and the ids it assigned to the static channels, in the
// order they were requested.
type ServerNetworkData struct {
	MCSChannelId   uint16   `struc:"little"`
	ChannelCount   uint16   `struc:"little,sizeof=ChannelIdArray"`
	ChannelIdArray []uint16 `struc:"little"`
}

// ScType reports which conference message this is.
func (d *ServerNetworkData) ScType() Message {
	return SC_NET
}

// Unpack reads the channel ids. The count counts the ids rather than the bytes,
// and the struct tag is what turns it into a slice.
func (d *ServerNetworkData) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// CertData is a server certificate, in whichever of the two forms it arrived.
//
// Verify reports whether the certificate can be trusted for this connection, and
// every implementation now says something true: a version 2 chain is verified
// against the system roots, and a version 1 certificate is a bare public key with
// no chain, which cannot be verified at all and says so. Nothing here returns true
// without having checked something.
//
// An error means the certificate is not trusted, and carries the reason. The
// default in the security layer is to report it and continue, because an RDP
// server normally presents a self-signed certificate and refusing those by default
// would refuse most servers; see sec.Client.SetRequireVerifiedCertificate.
type CertData interface {
	GetPublicKey() (*rsa.PublicKey, error)
	Verify() error
	Unpack(io.Reader) error
}

// ServerCertificate is the certificate field of the server's security data, which
// is a version and then the certificate itself in the form that version implies.
type ServerCertificate struct {
	DwVersion uint32
	CertData  CertData
}

// Unpack reads the version and then dispatches on it, choosing the proprietary
// form for a version 1 chain and X.509 for version 2.
//
// The top bit of the version is masked off before comparing, because it is a flag
// rather than part of the version. An unrecognised version is an error rather than
// a guess, since the rest of the field is unreadable if the form is unknown.
func (sc *ServerCertificate) Unpack(r io.Reader) error {
	sc.DwVersion, _ = core.ReadUInt32LE(r)
	var cd CertData
	switch CertificateType(sc.DwVersion & 0x7fffffff) {
	case CERT_CHAIN_VERSION_1:
		glog.Debug("ProprietaryServerCertificate")
		cd = &ProprietaryServerCertificate{}
	case CERT_CHAIN_VERSION_2:
		glog.Debug("X509CertificateChain")
		cd = &X509CertificateChain{}
	default:
		glog.Error("Unsupported version:", sc.DwVersion&0x7fffffff)
		return errors.New("Unsupported version")
	}
	if cd != nil {
		err := cd.Unpack(r)
		if err != nil {
			glog.Error("Unpack:", err)
			return err
		}
	}
	sc.CertData = cd

	return nil
}

// ServerSecurityData is the server's security data: the encryption it chose and,
// when it is using Standard RDP Security, the random value and certificate the
// client needs to derive the session keys from.
//
// There is no ServerCertificate when TLS or CredSSP is in use, which is why Unpack
// reads it only when the encryption method and level are not both zero.
type ServerSecurityData struct {
	EncryptionMethod  uint32 `struc:"little"`
	EncryptionLevel   uint32 `struc:"little"`
	ServerRandomLen   uint32 //0x00000020
	ServerCertLen     uint32
	ServerRandom      []byte
	ServerCertificate ServerCertificate
}

// NewServerSecurityData returns security data with no encryption, which is the
// form used over TLS and CredSSP.
//
// A client never sends this, so the constructor exists for tests.
func NewServerSecurityData() *ServerSecurityData {
	return &ServerSecurityData{
		0, 0, 0x00000020, 0, []byte{}, ServerCertificate{}}
}

// ScType reports which conference message this is.
func (d *ServerSecurityData) ScType() Message {
	return SC_SECURITY
}

// Unpack reads the security data. The random value and the certificate are present
// only when the server is using Standard RDP Security, since over TLS or CredSSP
// there is nothing for the client to derive: a server that sends neither reports
// both the method and the level as zero, and the fields after them are absent
// rather than empty.
func (s *ServerSecurityData) Unpack(r io.Reader) error {
	s.EncryptionMethod, _ = core.ReadUInt32LE(r)
	s.EncryptionLevel, _ = core.ReadUInt32LE(r)
	if !(s.EncryptionMethod == 0 && s.EncryptionLevel == 0) {
		s.ServerRandomLen, _ = core.ReadUInt32LE(r)
		s.ServerCertLen, _ = core.ReadUInt32LE(r)
		s.ServerRandom, _ = core.ReadBytes(int(s.ServerRandomLen), r)
		var sc ServerCertificate
		data, _ := core.ReadBytes(int(s.ServerCertLen), r)
		rd := bytes.NewReader(data)
		err := sc.Unpack(rd)
		if err != nil {
			return err
		}
		s.ServerCertificate = sc
	}

	return nil
}

// MakeConferenceCreateRequest wraps GCC user data in a T.125 Conference Create
// Request, which is the outer structure of the Connect-Initial. The user data is
// the packed core, security and network blocks in that order.
//
// The eight fields before the user data trace the path the structure takes through
// the ASN.1: a choice, the T.124 object identifier, a length, another choice, a
// selection, the numeric string "1", padding, and the count of a set. They are
// fixed values and every client sends the same ones; the length is the only field
// that depends on the caller.
func MakeConferenceCreateRequest(userData []byte) []byte {
	buff := &bytes.Buffer{}
	per.WriteChoice(0, buff)                        // 00
	per.WriteObjectIdentifier(t124_02_98_oid, buff) // 05:00:14:7c:00:01
	per.WriteLength(len(userData)+14, buff)
	per.WriteChoice(0, buff)                                                     // 00
	per.WriteSelection(0x08, buff)                                               // 08
	per.WriteNumericString("1", per.MinSimpleNumericStringLen, buff)             // 00 10
	per.WritePadding(1, buff)                                                    // 00
	per.WriteNumberOfSet(1, buff)                                                // 01
	per.WriteChoice(0xc0, buff)                                                  // c0
	per.WriteOctetStream(h221_cs_key, per.MinH221NonStandardIdentifierLen, buff) // 00 44:75:63:61
	per.WriteOctetStream(string(userData), 0, buff)
	return buff.Bytes()
}

// ReadConferenceCreateRequest reads a Conference Create Request and returns the
// user data blocks in it: core, security and network, in the order they arrived.
//
// The fixed fields are read with ReadExcept, which panics on a value it does not
// expect, and the panic is recovered at the top of this function rather than
// propagated. A malformed request therefore returns an error rather than taking the
// process with it, and the error names the identifier or the length, whichever
// failed.
func ReadConferenceCreateRequest(data []byte) ([]interface{}, error) {
	defer func() {
		if err := recover(); err != nil {
			glog.Errorf("err in ReadConferenceCreateRequest: %+v", err)
		}
	}()
	r := bytes.NewReader(data)

	// Each fixed byte is checked rather than skipped: the value is what the
	// protocol says it is, and a wrong one means everything after it was read from
	// the wrong offset. The value is still consumed either way, which is why a
	// failure ends the message rather than being retried at another position.
	if err := per.ReadExcept(0x00, r); err != nil { // 00 choice
		return nil, fmt.Errorf("gcc: conference create request: %w", err)
	}
	// The identifier is read before the length in a request and after it in a
	// response, which is the one place the two readers differ in field order.
	oidMatch := per.MatchObjectIdentifier(r, t124_02_98_oid)
	if _, err := per.ReadLength(r); err != nil {
		return nil, fmt.Errorf("gcc: conference create request length: %w", err)
	}
	for _, want := range []struct {
		b   byte
		why string
	}{
		{0x00, "choice"},
		{0x08, "selection"},
		{0x00, "padding"},
		{0x01, "number of set"},
		{0xC0, "choice"},
	} {
		if err := per.ReadExcept(want.b, r); err != nil {
			return nil, fmt.Errorf("gcc: conference create request: %s: %w", want.why, err)
		}
	}
	per.ReadNumericString(per.MinSimpleNumericStringLen, r) // 00 10
	if !per.MatchOctetStream(r, h221_cs_key, per.MinH221NonStandardIdentifierLen) {
		return nil, errors.New("gcc: conference create request does not carry the H.221 client key")
	}
	if !oidMatch {
		return nil, errors.New("gcc: conference create request does not carry the T.124 identifier")
	}

	ret := make([]interface{}, 0, 3)
	ln, _ := per.ReadLength(r)
	for ln > 0 {
		t, _ := core.ReadUint16LE(r)
		l, _ := core.ReadUint16LE(r)
		dataBytes, _ := core.ReadBytes(int(l)-4, r)
		ln = ln - l
		var d ScData
		switch Message(t) {
		case SC_CORE:
			d = &ServerCoreData{}
		case SC_SECURITY:
			d = &ServerSecurityData{}
		case SC_NET:
			d = &ServerNetworkData{}
		default:
			glog.Error("userData Unknown type", t)
			continue
		}
		if d != nil {
			r := bytes.NewReader(dataBytes)
			err := d.Unpack(r)
			if err != nil {
				glog.Warn("Unpack:", err)
			}
			ret = append(ret, d)
		}
	}

	return ret, nil
}

// ScData is one block of GCC user data: a server core, security or network block.
// The type of the block travels in the header ahead of the bytes, so it is read
// first and the block is parsed once it is known.
type ScData interface {
	ScType() Message
	Unpack(io.Reader) error
}

// ReadConferenceCreateResponse reads a Conference Create Response, which is what
// carries the server's user data back.
//
// It returns the blocks it understood and stops at the end of the user data. A
// response that does not carry T.124's identifier returns an empty list rather than
// an error, so a caller that must know the exchange succeeded has to check the
// list it gets rather than trusting that no error means success.
func ReadConferenceCreateResponse(data []byte) []interface{} {
	ret := make([]interface{}, 0, 3)

	r := bytes.NewReader(data)
	per.ReadChoice(r)
	if !per.MatchObjectIdentifier(r, t124_02_98_oid) {
		glog.Error("NODE_RDP_PROTOCOL_T125_GCC_BAD_OBJECT_IDENTIFIER_T124")
		return ret
	}
	per.ReadLength(r)
	per.ReadChoice(r)
	per.ReadInteger16(r)
	per.ReadInteger(r)
	per.ReadEnumerates(r)
	per.ReadNumberOfSet(r)
	per.ReadChoice(r)

	if !per.MatchOctetStream(r, h221_sc_key, 4) {
		glog.Error("NODE_RDP_PROTOCOL_T125_GCC_BAD_H221_SC_KEY")
		return ret
	}

	ln, _ := per.ReadLength(r)
	for ln > 0 {
		t, _ := core.ReadUint16LE(r)
		glog.Debugf("Message type 0x%x,ln:%v", t, ln)
		l, _ := core.ReadUint16LE(r)
		dataBytes, _ := core.ReadBytes(int(l)-4, r)
		ln = ln - l
		var d ScData
		switch Message(t) {
		case SC_CORE:
			d = &ServerCoreData{}
		case SC_SECURITY:
			d = &ServerSecurityData{}
		case SC_NET:
			d = &ServerNetworkData{}
		default:
			glog.Error("userData Unknown type", t)
			continue
		}

		if d != nil {
			r := bytes.NewReader(dataBytes)
			err := d.Unpack(r)
			if err != nil {
				glog.Warn("Unpack:", err)
			}
			ret = append(ret, d)
		}
	}

	return ret
}
