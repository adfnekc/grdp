package core

import (
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"math/big"
	"net"

	"github.com/huin/asn1ber"
)

// SocketLayer is a Transport over a network connection, with the option of
// upgrading it to TLS. Reads and writes are not safe for concurrent use, which
// matches the single reader the protocol layers expect.
type SocketLayer struct {
	conn      net.Conn
	tlsConn   *tls.Conn
	tlsConfig *tls.Config
}

// NewSocketLayer wraps a connection, reading and writing directly until StartTLS
// is called.
func NewSocketLayer(conn net.Conn) *SocketLayer {
	l := &SocketLayer{
		conn:    conn,
		tlsConn: nil,
	}
	return l
}

// Read implements the Transport interface.
func (s *SocketLayer) Read(b []byte) (n int, err error) {
	if s.tlsConn != nil {
		return s.tlsConn.Read(b)
	}
	return s.conn.Read(b)
}

// Write implements the Transport interface.
func (s *SocketLayer) Write(b []byte) (n int, err error) {
	if s.tlsConn != nil {
		return s.tlsConn.Write(b)
	}
	return s.conn.Write(b)
}

// Close closes the underlying connection.
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

// StartTLS upgrades the connection to TLS, using the configuration the layer
// was created with.
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

// PublicKey is an RSA public key as RDP carries it.
type PublicKey struct {
	N *big.Int `asn1:"explicit,tag:0"` // modulus
	E int      `asn1:"explicit,tag:1"` // public exponent
}

// TlsPubKey returns the server's RSA public key in DER form, which is what
// CredSSP's version 5 and later binding hash is computed over. It is only
// meaningful after StartTLS.
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
