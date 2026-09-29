package sec

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/emission"
	"github.com/adfnekc/grdp/protocol/t125/gcc"
)

// fakeTransport captures everything the client writes so a test can inspect
// the PDUs the Standard RDP Security handshake produces.
type fakeTransport struct {
	*emission.Emitter
	written bytes.Buffer
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{Emitter: emission.NewEmitter()}
}

func (f *fakeTransport) Read(b []byte) (int, error)  { return 0, io.EOF }
func (f *fakeTransport) Write(b []byte) (int, error) { return f.written.Write(b) }
func (f *fakeTransport) Close() error                { return nil }

// newStdSecClient builds a client whose server data carries the security
// exchange values a server would send, so sendClientRandom can be exercised
// without a live connection.
func newStdSecClient(t *testing.T, certData gcc.CertData) (*Client, *fakeTransport, *gcc.ServerSecurityData) {
	t.Helper()
	ft := newFakeTransport()
	c := NewClient(ft)
	c.clientData = []interface{}{
		&gcc.ClientCoreData{},
		gcc.NewClientSecurityData(),
		gcc.NewClientNetworkData(),
	}
	ssd := gcc.NewServerSecurityData()
	ssd.EncryptionMethod = gcc.ENCRYPTION_FLAG_128BIT
	ssd.ServerRandom = bytes.Repeat([]byte{0xA5}, 32)
	if certData != nil {
		ssd.ServerCertificate = gcc.ServerCertificate{
			DwVersion: uint32(gcc.CERT_CHAIN_VERSION_1),
			CertData:  certData,
		}
	}
	c.serverData = []interface{}{&gcc.ServerCoreData{}, ssd}
	return c, ft, ssd
}

// proprietaryCert builds the CERT_CHAIN_VERSION_1 certificate a server sends,
// with the modulus byte-reversed the way RSAPublicKey.GetPublicKey reads it.
func proprietaryCert(key *rsa.PrivateKey) *gcc.ProprietaryServerCertificate {
	return &gcc.ProprietaryServerCertificate{
		PublicKeyBlob: gcc.RSAPublicKey{
			Bitlen:  uint32(key.PublicKey.N.BitLen()),
			PubExp:  uint32(key.PublicKey.E),
			Modulus: core.Reverse(key.PublicKey.N.Bytes()),
		},
	}
}

// TestGenerateKeysMatchesReference checks the session key schedule against
// values computed independently from MS-RDPBCGR 5.3.5. The randoms are fixed so
// the expected keys are stable; if the salt strings, the hash order or the
// 40/56 bit truncation were wrong the hex would not match.
//
//	clientRandom = 00 01 .. 1f
//	serverRandom = 20 21 .. 3f
func TestGenerateKeysMatchesReference(t *testing.T) {
	clientRandom := make([]byte, 32)
	for i := range clientRandom {
		clientRandom[i] = byte(i)
	}
	serverRandom := make([]byte, 32)
	for i := range serverRandom {
		serverRandom[i] = byte(i + 32)
	}

	cases := []struct {
		name   string
		method uint32
		mac    string
		dec    string
		enc    string
	}{
		{
			"128bit", gcc.ENCRYPTION_FLAG_128BIT,
			"815370c6e31347c463ed25f1af48bbdf",
			"1cb207f61b7cd10dca9ec78871d0a142",
			"702783c08474414a33a259c6faed480c",
		},
		{
			"40bit", gcc.ENCRYPTION_FLAG_40BIT,
			"d1269ec6e31347c4",
			"d1269ef61b7cd10d",
			"d1269ec08474414a",
		},
		{
			"56bit", gcc.ENCRYPTION_FLAG_56BIT,
			"d15370c6e31347c4",
			"d1b207f61b7cd10d",
			"d12783c08474414a",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac, dec, enc := generateKeys(clientRandom, serverRandom, tc.method)
			if got := hex.EncodeToString(mac); got != tc.mac {
				t.Errorf("mac key = %s, want %s", got, tc.mac)
			}
			if got := hex.EncodeToString(dec); got != tc.dec {
				t.Errorf("decrypt key = %s, want %s", got, tc.dec)
			}
			if got := hex.EncodeToString(enc); got != tc.enc {
				t.Errorf("encrypt key = %s, want %s", got, tc.enc)
			}
		})
	}
}

// The wire supplies the server random, so a truncated one must be rejected
// rather than sliced past the end.
func TestGenerateKeysRejectsShortRandoms(t *testing.T) {
	full := make([]byte, 32)

	for _, tc := range []struct {
		name string
		c, s []byte
	}{
		{"short client random", make([]byte, 23), full},
		{"short server random", full, make([]byte, 31)},
		{"empty server random", full, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mac, dec, enc := generateKeys(tc.c, tc.s, gcc.ENCRYPTION_FLAG_128BIT)
			if mac != nil || dec != nil || enc != nil {
				t.Fatalf("keys = %x/%x/%x, want nil for a short random", mac, dec, enc)
			}
		})
	}
}

// TestMacDataMatchesReference checks the MAC over a known key and payload
// against the same reference algorithm.
func TestMacDataMatchesReference(t *testing.T) {
	key, err := hex.DecodeString("815370c6e31347c463ed25f1af48bbdf")
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	want, err := hex.DecodeString("08e297801925be5021fa86abe25b30f1")
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	if got := macData(key, []byte("hello")); !bytes.Equal(got, want) {
		t.Fatalf("macData = %x, want %x", got, want)
	}
}

// A server that declares a server random shorter than 32 bytes used to crash
// the client: the key schedule slices it to 24. It must fail instead.
func TestSendClientRandomRejectsShortServerRandom(t *testing.T) {
	c, _, ssd := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	ssd.ServerRandom = []byte{0x01, 0x02, 0x03}

	if err := c.sendClientRandom(); err == nil {
		t.Fatal("expected an error for a short server random")
	}
}

// A security exchange with no certificate used to panic when the client called
// Verify through the nil CertData interface. It must fail instead.
func TestSendClientRandomRejectsMissingCertificate(t *testing.T) {
	c, _, ssd := newStdSecClient(t, nil)
	ssd.ServerRandom = make([]byte, 32)

	if err := c.sendClientRandom(); err == nil {
		t.Fatal("expected an error for a missing server certificate")
	}
}

// TestConnectFailsOnKeyExchangeError checks that a failed Standard RDP
// Security key exchange is reported through the error event instead of letting
// the connection continue without session keys.
func TestConnectFailsOnKeyExchangeError(t *testing.T) {
	c, _, _ := newStdSecClient(t, nil)

	gotErr := make(chan error, 1)
	c.On("error", func(err error) {
		select {
		case gotErr <- err:
		default:
		}
	})

	c.connect(c.clientData, c.serverData, 1, nil)

	select {
	case err := <-gotErr:
		if err == nil {
			t.Fatal("connect reported a nil error")
		}
	default:
		t.Fatal("connect did not report the key exchange failure")
	}
}

// TestSendClientRandomSecurityExchange checks the Client Security Exchange
// PDU on the wire against MS-RDPBCGR 5.3.4.1: a security header, a length of
// modulusLength + 8, the RSA-encrypted client random and 8 bytes of padding.
// It also decrypts the random with the private key and re-derives the session
// keys from it, so a wrong encryption input or byte order shows up here.
func TestSendClientRandomSecurityExchange(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	c, ft, ssd := newStdSecClient(t, proprietaryCert(key))
	if err := c.sendClientRandom(); err != nil {
		t.Fatalf("sendClientRandom: %v", err)
	}

	raw := ft.written.Bytes()
	modulusLen := key.PublicKey.Size()
	wantLen := 2 + 2 + 4 + modulusLen + 8
	if len(raw) != wantLen {
		t.Fatalf("PDU is %d bytes, want %d", len(raw), wantLen)
	}

	if flag := binary.LittleEndian.Uint16(raw[0:2]); flag != EXCHANGE_PKT {
		t.Fatalf("security flag = 0x%04x, want 0x%04x", flag, EXCHANGE_PKT)
	}
	if hi := binary.LittleEndian.Uint16(raw[2:4]); hi != 0 {
		t.Fatalf("security flag hi = 0x%04x, want 0", hi)
	}
	if length := binary.LittleEndian.Uint32(raw[4:8]); int(length) != modulusLen+8 {
		t.Fatalf("length field = %d, want %d", length, modulusLen+8)
	}
	if padding := raw[8+modulusLen:]; !bytes.Equal(padding, make([]byte, 8)) {
		t.Fatalf("padding = %x, want 8 zero bytes", padding)
	}

	cipher := raw[8 : 8+modulusLen]
	plain, err := rsa.DecryptPKCS1v15(nil, key, core.Reverse(cipher))
	if err != nil {
		t.Fatalf("decrypt client random: %v", err)
	}
	if len(plain) != 32 {
		t.Fatalf("client random is %d bytes, want 32", len(plain))
	}
	clientRandom := core.Reverse(plain)

	mac, dec, enc := generateKeys(clientRandom, ssd.ServerRandom, ssd.EncryptionMethod)
	if !bytes.Equal(c.macKey, mac) || !bytes.Equal(c.initialDecrytKey, dec) || !bytes.Equal(c.initialEncryptKey, enc) {
		t.Fatal("the stored session keys do not match the client random that was sent")
	}
}
