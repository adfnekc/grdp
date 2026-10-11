package gcc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"testing"
	"time"
)

// selfSignedCert returns a DER encoded, self-signed certificate. It is a real
// certificate, so a chain built from it fails verification because nothing trusts
// it, which is not the same as failing because it could not be parsed.
func selfSignedCert(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "grdp test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der
}

// The bug this pins: Verify returned true without verifying anything, so a
// certificate that nothing trusts was reported as trusted. It is called on the
// Standard RDP Security path before the client random is encrypted to the
// certificate's key, which is the point at which being wrong matters.
func TestX509ChainVerifyRejectsAnUntrustedCertificate(t *testing.T) {
	chain := &X509CertificateChain{
		NumCertBlobs:  1,
		CertBlobArray: []CertBlob{{AbCert: selfSignedCert(t)}},
	}
	err := chain.Verify()
	if err == nil {
		t.Fatal("Verify accepted a self-signed certificate that nothing trusts")
	}
	if !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("Verify failed with %q, which does not say the certificate was untrusted", err)
	}
}

func TestX509ChainVerifyReportsWhatItCouldNotRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chain *X509CertificateChain
		want  string
	}{
		{"empty chain", &X509CertificateChain{}, "empty"},
		{"not a certificate", &X509CertificateChain{CertBlobArray: []CertBlob{{AbCert: []byte("not DER")}}}, "parse"},
	} {
		err := tc.chain.Verify()
		if err == nil {
			t.Errorf("%s: Verify returned nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Verify failed with %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// A version 1 certificate has no chain, so there is nothing to verify it against.
// The old implementation returned true here, which said the opposite.
func TestProprietaryCertificateCannotVerify(t *testing.T) {
	p := &ProprietaryServerCertificate{}
	if err := p.Verify(); err == nil {
		t.Fatal("Verify accepted a version 1 certificate, which has no chain to check")
	}
	if err := p.Verify(); !strings.Contains(err.Error(), "no chain") {
		t.Errorf("Verify failed with %q, which does not say why", err)
	}
}

// The public key from a chain is the last certificate's, which is the order the
// wire format uses and the order Verify's intermediates rely on.
func TestChainPublicKeyComesFromTheLastBlob(t *testing.T) {
	der := selfSignedCert(t)
	chain := &X509CertificateChain{CertBlobArray: []CertBlob{{AbCert: der}}}
	key, err := chain.GetPublicKey()
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if key.N.Cmp(cert.PublicKey.(*rsa.PublicKey).N) != 0 {
		t.Error("GetPublicKey returned a different key from the certificate's")
	}
}
