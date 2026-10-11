package lic

import (
	"io"

	"github.com/adfnekc/grdp/core"
)

// The licensing message types, from MS-RDPBCGR 2.2.4. The exchange is a small
// state machine: the server sends a request, challenges the platform, and then
// issues a licence, and either side can abort with ERROR_ALERT.
//
// This client answers the request and the challenge and accepts whatever licence
// comes back. It never asks for an upgrade (UPGRADE_LICENSE) and never sends a
// NEW_LICENSE_REQUEST, so two of these are defined but only ever read.
const (
	LICENSE_REQUEST             = 0x01
	PLATFORM_CHALLENGE          = 0x02
	NEW_LICENSE                 = 0x03
	UPGRADE_LICENSE             = 0x04
	LICENSE_INFO                = 0x12
	NEW_LICENSE_REQUEST         = 0x13
	PLATFORM_CHALLENGE_RESPONSE = 0x15
	ERROR_ALERT                 = 0xFF
)

// error code
const (
	ERR_INVALID_SERVER_CERTIFICATE = 0x00000001
	ERR_NO_LICENSE                 = 0x00000002
	ERR_INVALID_SCOPE              = 0x00000004
	ERR_NO_LICENSE_SERVER          = 0x00000006
	STATUS_VALID_CLIENT            = 0x00000007
	ERR_INVALID_CLIENT             = 0x00000008
	ERR_INVALID_PRODUCTID          = 0x0000000B
	ERR_INVALID_MESSAGE_LEN        = 0x0000000C
	ERR_INVALID_MAC                = 0x00000003
)

// state transition
const (
	ST_TOTAL_ABORT          = 0x00000001
	ST_NO_TRANSITION        = 0x00000002
	ST_RESET_PHASE_TO_START = 0x00000003
	ST_RESEND_LAST_MESSAGE  = 0x00000004
)

// BinaryBlobType is the tag on a licensing blob, which says what the bytes are
// without needing the rest of the message to be understood.
type BinaryBlobType uint16

// The blob types. A certificate blob carries the server's licence certificate,
// the two name blobs carry the client's user and machine name, and the encrypted
// data and key exchange blobs describe the exchange that produces the licence
// key.
const (
	BB_ANY_BLOB                 = 0x0000
	BB_DATA_BLOB                = 0x0001
	BB_RANDOM_BLOB              = 0x0002
	BB_CERTIFICATE_BLOB         = 0x0003
	BB_ERROR_BLOB               = 0x0004
	BB_ENCRYPTED_DATA_BLOB      = 0x0009
	BB_KEY_EXCHG_ALG_BLOB       = 0x000D
	BB_SCOPE_BLOB               = 0x000E
	BB_CLIENT_USER_NAME_BLOB    = 0x000F
	BB_CLIENT_MACHINE_NAME_BLOB = 0x0010
)

// ErrorMessage is the body of an ERROR_ALERT, which is how either side ends the
// licensing exchange. The state transition says whether the exchange is over or
// whether the last message should be sent again.
type ErrorMessage struct {
	DwErrorCode        uint32
	DwStateTransaction uint32
	Blob               []byte
}

func readErrorMessage(r io.Reader) *ErrorMessage {
	m := &ErrorMessage{}
	m.DwErrorCode, _ = core.ReadUInt32LE(r)
	m.DwStateTransaction, _ = core.ReadUInt32LE(r)
	return m
}

// LicensePacket is the three byte header that precedes every licensing message,
// and it is what the licensing phase dispatches on. WMsgSize counts the header,
// so the body is that many bytes less four.
//
// LicensingMessage holds whatever the message type implies: an *ErrorMessage for
// an alert, and the raw body for anything else, which the caller parses once it
// knows what it is looking at.
type LicensePacket struct {
	BMsgtype         uint8
	Flag             uint8
	WMsgSize         uint16
	LicensingMessage interface{}
}

// ReadLicensePacket reads one licensing message, header and all, and parses the
// body it recognises.
//
// Only ERROR_ALERT is interpreted: a licence blob needs the certificate and the
// encrypted exchange from the messages before it, so it is handed over as bytes.
// A read error is not reported, because this runs inside the licensing phase,
// where a short read ends the exchange anyway.
func ReadLicensePacket(r io.Reader) *LicensePacket {
	l := &LicensePacket{}
	l.BMsgtype, _ = core.ReadUInt8(r)
	l.Flag, _ = core.ReadUInt8(r)
	l.WMsgSize, _ = core.ReadUint16LE(r)

	switch l.BMsgtype {
	case ERROR_ALERT:
		l.LicensingMessage = readErrorMessage(r)
	default:
		l.LicensingMessage, _ = core.ReadBytes(int(l.WMsgSize-4), r)
	}

	return l
}

// LicenseBinaryBlob is a typed, length prefixed blob, which is how the licensing
// exchange carries certificates, the client's names and the encrypted data.
type LicenseBinaryBlob struct {
	WBlobType uint16 `struc:"little"`
	WBlobLen  uint16 `struc:"little"`
	BlobData  []byte `struc:"sizefrom=WBlobLen"`
}

/*
"""
@summary: License server product information
@see: http://msdn.microsoft.com/en-us/library/cc241915.aspx
"""
*/
type ProductInformation struct {
	DwVersion     uint32 `struc:"little"`
	CbCompanyName uint32 `struc:"little"`
	//may contain "Microsoft Corporation" from server microsoft
	PbCompanyName []byte `struc:"sizefrom=CbCompanyName"`
	CbProductId   uint32 `struc:"little"`
	//may contain "A02" from microsoft license server
	PbProductId []byte `struc:"sizefrom=CbProductId"`
}

/*
@summary:  Send by server to signal license request

	server -> client

@see: http://msdn.microsoft.com/en-us/library/cc241914.aspx
*/
type ServerLicenseRequest struct {
	ServerRandom      []byte             `struc:"[32]byte"`
	ProductInfo       ProductInformation `struc:"little"`
	KeyExchangeList   LicenseBinaryBlob  `struc:"little"`
	ServerCertificate LicenseBinaryBlob  `struc:"little"`
	//ScopeList         ScopeList
}

// ClientNewLicenseRequest is the client's answer to the licence request. It asks
// for a new licence rather than reusing one, which is all this client does: the
// preferred key exchange algorithm is RSA and must be, because the pre-master
// secret inside the blob is encrypted with the public key from the server's
// certificate, and the two name blobs are what the server records the licence
// against.
type ClientNewLicenseRequest struct {
	PreferredKeyExchangeAlg  uint32            `struc:"little"`
	PlatformId               uint32            `struc:"little"`
	ClientRandom             []byte            `struc:"[32]byte"`
	EncryptedPreMasterSecret LicenseBinaryBlob `struc:"little"`
	ClientUserName           LicenseBinaryBlob `struc:"little"`
	ClientMachineName        LicenseBinaryBlob `struc:"little"`
}

/*
@summary: challenge send from server to client
@see: http://msdn.microsoft.com/en-us/library/cc241921.aspx
*/
type ServerPlatformChallenge struct {
	ConnectFlags               uint32
	EncryptedPlatformChallenge LicenseBinaryBlob
	MACData                    [16]byte
}

/*
"""
@summary: client challenge response
@see: http://msdn.microsoft.com/en-us/library/cc241922.aspx
"""
*/
type ClientPLatformChallengeResponse struct {
	EncryptedPlatformChallengeResponse LicenseBinaryBlob
	EncryptedHWID                      LicenseBinaryBlob
	MACData                            []byte //[16]byte
}
