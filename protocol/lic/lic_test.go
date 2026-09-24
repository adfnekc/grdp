package lic

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/adfnekc/grdp/protocol/t125/gcc"
	"github.com/lunixbochs/struc"
)

// A real Server License Request captured from Windows 11. Keeping it matters
// because the licence request carries a ProductInfo block, a KeyExchangeList
// and then the server certificate, and a struct that gets the order or the
// lengths wrong still parses without error, it just produces a nonsense RSA
// key. That is exactly what happened: the modulus came out even and Go refused
// it, and the server never completed licensing.
const windowsLicenseRequestHex = "80003e0101023e017b3c31a6aee874f6b4a50390e7c2c739ba531c30546e9005d005ce4418918381000004002c0000004d006900630072006f0073006f0066007400200043006f00720070006f0072006100740069006f006e0000000800000032003300360000000d000400010000000300b80001000000010000000100000006005c005253413148000000000200003f0000000100010001c7c9f78e5a38e429c300952ddd4c3e50450b0d9e2a5d186364c42cf78f29d53fc5352234ffad3ae6e39506ae5582e3c8c7b4a847c85071742953896d9ced70000000000000000008004800a8f431b9ab4be6b4f43989d6b1daf61eecb1f0543b5e3e6a71b4f775c8162f2400dee982995f330ba9a694afcb11c3f2db0942682956580156db590369db7d370000000000000000010000000e000e006d6963726f736f66742e636f6d00"

func TestParseRealWindowsLicenseRequest(t *testing.T) {
	raw, err := hex.DecodeString(windowsLicenseRequestHex)
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	// 4 byte security header, then the licence PDU header.
	body := raw[8:]

	var req ServerLicenseRequest
	if err := struc.Unpack(bytes.NewReader(body), &req); err != nil {
		t.Fatalf("unpack: %v", err)
	}

	if len(req.ServerRandom) != 32 {
		t.Fatalf("server random is %d bytes, want 32", len(req.ServerRandom))
	}
	if req.ProductInfo.DwVersion != 0x00040000 {
		t.Fatalf("product version is 0x%08x", req.ProductInfo.DwVersion)
	}
	if req.ServerCertificate.WBlobType != 0x0003 {
		t.Fatalf("certificate blob type is 0x%04x, want 0x0003", req.ServerCertificate.WBlobType)
	}
	if req.ServerCertificate.WBlobLen != 184 {
		t.Fatalf("certificate blob is %d bytes, want 184", req.ServerCertificate.WBlobLen)
	}

	// The certificate must yield a usable RSA key. An even modulus is the
	// symptom of reading the wrong bytes.
	var sc gcc.ServerCertificate
	if err := sc.Unpack(bytes.NewReader(req.ServerCertificate.BlobData)); err != nil {
		t.Fatalf("certificate unpack: %v", err)
	}
	pub, err := sc.CertData.GetPublicKey()
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	if pub.N.Bit(0) != 1 {
		t.Fatal("modulus is even, so the certificate was mis-parsed")
	}
	if pub.E != 65537 {
		t.Fatalf("exponent is %d, want 65537", pub.E)
	}
	if pub.N.BitLen() != 511 && pub.N.BitLen() != 512 {
		t.Fatalf("modulus is %d bits, want about 512", pub.N.BitLen())
	}
}
