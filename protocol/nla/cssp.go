package nla

import (
	"crypto/sha256"
	"encoding/asn1"

	"github.com/adfnekc/grdp/glog"
)

// NegoToken is one authentication token in a TSRequest, which is how the NTLM
// messages travel: the Go struct that holds a message's bytes becomes one of these.
type NegoToken struct {
	Data []byte `asn1:"explicit,tag:0"`
}

// TSRequest is the CredSSP message, and the only one: every step of the exchange is
// a TSRequest with different fields filled in. A request carries a version and the
// NTLM token; the reply to it carries the token the client answers, and later
// messages carry the authentication info and the public key binding instead.
//
// Every field is optional except the version, which is why each one is a slice that
// is left nil when it is not part of this message.
type TSRequest struct {
	Version     int         `asn1:"explicit,tag:0"`
	NegoTokens  []NegoToken `asn1:"optional,explicit,tag:1"`
	AuthInfo    []byte      `asn1:"optional,explicit,tag:2"`
	PubKeyAuth  []byte      `asn1:"optional,explicit,tag:3"`
	ErrorCode   int         `asn1:"optional,explicit,tag:4"`
	ClientNonce []byte      `asn1:"optional,explicit,tag:5"`
}

// CredSSP versions that change how the public key binding is computed.
const (
	// VersionLegacy binds the raw TLS public key. It is vulnerable to
	// CVE-2018-0886 and only kept for old servers.
	VersionLegacy = 4
	// VersionSha256 replaces the raw key with a direction specific SHA-256
	// binding hash. Windows servers speak at least this.
	VersionSha256 = 5
	// VersionNonce is what Windows 10 and later offer.
	VersionNonce = 6
)

// Binding prefixes from MS-CSSP 3.1.5, including the terminating NUL. They are
// what makes the client and server hashes different for the same key and nonce.
const (
	BindingPrefixClientToServer = "CredSSP Client-To-Server Binding Hash\x00"
	BindingPrefixServerToClient = "CredSSP Server-To-Client Binding Hash\x00"
)

// NonceLen is the length of the client nonce required from version 5 on.
const NonceLen = 32

// LegacyExpectedServerResponse returns what a pre-version-5 server replies in
// its pubKeyAuth: the server's SubjectPublicKeyInfo with its first byte
// incremented by one. It is a proof of freshness rather than a hash, which is
// why CVE-2018-0886 applies to it.
func LegacyExpectedServerResponse(serverPubKey []byte) []byte {
	out := make([]byte, len(serverPubKey))
	copy(out, serverPubKey)
	if len(out) > 0 {
		out[0]++
	}
	return out
}

// ClientToServerHash computes the SHA-256 binding hash the client seals into
// its pubKeyAuth. serverPubKey is the DER encoded SubjectPublicKeyInfo of the
// server's TLS certificate, exactly as it appears in the certificate.
func ClientToServerHash(nonce, serverPubKey []byte) []byte {
	h := sha256.New()
	h.Write([]byte(BindingPrefixClientToServer))
	h.Write(nonce)
	h.Write(serverPubKey)
	return h.Sum(nil)
}

// ServerToClientHash computes the hash the server is expected to return. The
// client compares it against the unsealed pubKeyAuth from the server.
func ServerToClientHash(nonce, serverPubKey []byte) []byte {
	h := sha256.New()
	h.Write([]byte(BindingPrefixServerToClient))
	h.Write(nonce)
	h.Write(serverPubKey)
	return h.Sum(nil)
}

// TSCredentials is the credentials the client sends once the authentication has
// succeeded, wrapped in the security context the NTLM exchange produced.
//
// CredType says what the credentials are: 1 is a user name and password, and 2 is a
// smart card.
type TSCredentials struct {
	CredType    int    `asn1:"explicit,tag:0"`
	Credentials []byte `asn1:"explicit,tag:1"`
}

// TSPasswordCreds is a domain, a user name and a password, in the character set the
// NTLM exchange settled on rather than as the caller wrote them.
type TSPasswordCreds struct {
	DomainName []byte `asn1:"explicit,tag:0"`
	UserName   []byte `asn1:"explicit,tag:1"`
	Password   []byte `asn1:"explicit,tag:2"`
}

// TSCspDataDetail names a smart card and the cryptographic service provider that
// holds it. It is defined so that a smart card credential structure can be parsed,
// and this client never sends one: there is no smart card support here.
type TSCspDataDetail struct {
	KeySpec       int    `asn1:"explicit,tag:0"`
	CardName      string `asn1:"explicit,tag:1"`
	ReaderName    string `asn1:"explicit,tag:2"`
	ContainerName string `asn1:"explicit,tag:3"`
	CspName       string `asn1:"explicit,tag:4"`
}

// TSSmartCardCreds is a smart card logon: a PIN and where to find the card.
//
// Nothing sends one of these. It is here because the credential structure is a
// choice and the parser has to be able to tell the two apart, not because smart
// cards work.
type TSSmartCardCreds struct {
	Pin        string            `asn1:"explicit,tag:0"`
	CspData    []TSCspDataDetail `asn1:"explicit,tag:1"`
	UserHint   string            `asn1:"explicit,tag:2"`
	DomainHint string            `asn1:"explicit,tag:3"`
}

// EncodeDERTRequest builds a TSRequest at the newest version the client speaks,
// with no client nonce of its own, which is what the first two messages of the
// exchange are encoded with.
//
// It is the version without a nonce because the nonce only appears once the
// versions have been agreed, which is what EncodeDERTRequestVersion is for.
func EncodeDERTRequest(msgs []Message, authInfo []byte, pubKeyAuth []byte) []byte {
	return EncodeDERTRequestVersion(VersionNonce, msgs, authInfo, pubKeyAuth, nil)
}

// EncodeDERTRequestVersion builds a TSRequest with an explicit version and, for
// version 5 and later, the client nonce that the public key binding hash is
// computed over.
func EncodeDERTRequestVersion(version int, msgs []Message, authInfo, pubKeyAuth, clientNonce []byte) []byte {
	req := TSRequest{
		Version: version,
	}

	if len(msgs) > 0 {
		req.NegoTokens = make([]NegoToken, 0, len(msgs))
	}

	for _, msg := range msgs {
		token := NegoToken{msg.Serialize()}
		req.NegoTokens = append(req.NegoTokens, token)
	}

	if len(authInfo) > 0 {
		req.AuthInfo = authInfo
	}

	if len(pubKeyAuth) > 0 {
		req.PubKeyAuth = pubKeyAuth
	}

	if len(clientNonce) > 0 {
		req.ClientNonce = clientNonce
	}

	result, err := asn1.Marshal(req)
	if err != nil {
		glog.Error(err)
	}
	return result
}

// DecodeDERTRequest parses a TSRequest, which is how the server's replies are read.
// A message that does not parse returns both the partial structure and the error,
// so a caller must check the error before using what it got.
func DecodeDERTRequest(s []byte) (*TSRequest, error) {
	treq := &TSRequest{}
	_, err := asn1.Unmarshal(s, treq)
	return treq, err
}

// EncodeDERTCredentials builds the credential structure the exchange ends with: a
// TSPasswordCreds inside a TSCredentials, encoded twice because the outer structure
// carries the inner one as opaque bytes.
//
// The result is what gets sealed with the NTLM security context before it is sent,
// which is the only thing that keeps the password off the wire in the clear: this
// function produces it in the clear, and its caller is what protects it.
func EncodeDERTCredentials(domain, username, password []byte) []byte {
	tpas := TSPasswordCreds{domain, username, password}
	result, err := asn1.Marshal(tpas)
	if err != nil {
		glog.Error(err)
	}
	tcre := TSCredentials{1, result}
	result, err = asn1.Marshal(tcre)
	if err != nil {
		glog.Error(err)
	}
	return result
}

// DecodeDERTCredentials parses a credential structure. A client sends one rather
// than reading one, so this exists for tests and for a server side.
func DecodeDERTCredentials(s []byte) (*TSCredentials, error) {
	tcre := &TSCredentials{}
	_, err := asn1.Unmarshal(s, tcre)
	return tcre, err
}
