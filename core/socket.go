package core

import (
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"math/big"
	"net"

	"github.com/huin/asn1ber"
)

type SocketLayer struct {
	conn      net.Conn
	tlsConn   *tls.Conn
	tlsConfig *tls.Config
}

func NewSocketLayer(conn net.Conn) *SocketLayer {
	l := &SocketLayer{
		conn:    conn,
		tlsConn: nil,
	}
	return l
}

func (s *SocketLayer) Read(b []byte) (n int, err error) {
	if s.tlsConn != nil {
		return s.tlsConn.Read(b)
	}
	return s.conn.Read(b)
}

func (s *SocketLayer) Write(b []byte) (n int, err error) {
	if s.tlsConn != nil {
		return s.tlsConn.Write(b)
	}
	return s.conn.Write(b)
}

func (s *SocketLayer) Close() error {
	if s.tlsConn != nil {
		err := s.tlsConn.Close()
		if err != nil {
			return err
		}
	}
	return s.conn.Close()
}

// SetTLSConfig overrides the TLS configuration used by StartTLS. When unset a
// default configuration is used that accepts the self-signed certificates RDP
// servers normally present. Callers that require verification should supply a
// config with InsecureSkipVerify=false and the appropriate RootCAs/ServerName.
func (s *SocketLayer) SetTLSConfig(cfg *tls.Config) {
	s.tlsConfig = cfg
}

func (s *SocketLayer) StartTLS() error {
	config := s.tlsConfig
	if config == nil {
		config = &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
		}
	}
	s.tlsConn = tls.Client(s.conn, config)
	return s.tlsConn.Handshake()
}

type PublicKey struct {
	N *big.Int `asn1:"explicit,tag:0"` // modulus
	E int      `asn1:"explicit,tag:1"` // public exponent
}

// TlsPubKeySPKI returns the DER encoded SubjectPublicKeyInfo of the server's
// TLS certificate. CredSSP version 5 and later hash exactly these bytes to
// bind to the TLS channel, so the raw certificate field is what is needed here
// rather than a re-encoded key.
func (s *SocketLayer) TlsPubKeySPKI() ([]byte, error) {
	if s.tlsConn == nil {
		return nil, errors.New("TLS conn does not exist")
	}
	cs := s.tlsConn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return nil, errors.New("TLS peer presented no certificate")
	}
	spki := cs.PeerCertificates[0].RawSubjectPublicKeyInfo
	if len(spki) == 0 {
		return nil, errors.New("TLS peer certificate has no SubjectPublicKeyInfo")
	}
	out := make([]byte, len(spki))
	copy(out, spki)
	return out, nil
}

func (s *SocketLayer) TlsPubKey() ([]byte, error) {
	if s.tlsConn == nil {
		return nil, errors.New("TLS conn does not exist")
	}
	cs := s.tlsConn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return nil, errors.New("TLS peer presented no certificate")
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("TLS peer certificate key is not RSA")
	}
	return asn1ber.Marshal(*pub)
}
