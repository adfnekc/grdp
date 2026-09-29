package sec

import (
	"bytes"
	"crypto/rand"
	"crypto/rc4"
	"crypto/rsa"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/adfnekc/grdp/protocol/t125/gcc"
)

// The reference values below were produced by pyrdp's key update
// (pyrdp/security/key.py, updateKey), which is an implementation independent of
// this one. If the padding, the hash order or the salt of the 40 and 56 bit
// forms were wrong, the hex would not match.
func TestUpdateSessionKeyMatchesReference(t *testing.T) {
	cases := []struct {
		name     string
		initial  string
		current  string
		method   uint32
		expected string
	}{
		{
			"128bit", "000102030405060708090a0b0c0d0e0f",
			"101112131415161718191a1b1c1d1e1f",
			gcc.ENCRYPTION_FLAG_128BIT,
			"0c15dc15b1c7c9cbb033a0fd8e136710",
		},
		{
			"40bit", "d1269e0304050607",
			"d1269e1314151617",
			gcc.ENCRYPTION_FLAG_40BIT,
			"d1269ead563b43d3",
		},
		{
			"56bit", "d101020304050607",
			"d111121314151617",
			gcc.ENCRYPTION_FLAG_56BIT,
			"d12f348975bad8bb",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial, err := hex.DecodeString(tc.initial)
			if err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			current, err := hex.DecodeString(tc.current)
			if err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			got := updateSessionKey(initial, current, tc.method)
			if hex.EncodeToString(got) != tc.expected {
				t.Fatalf("updated key = %x, want %s", got, tc.expected)
			}
		})
	}
}

// A 40 bit key keeps its three byte salt and a 56 bit key its one byte salt, so
// a reduced key stays reduced across an update.
func TestUpdateSessionKeySaltsReducedKeys(t *testing.T) {
	initial := []byte{0xd1, 0x26, 0x9e, 0x03, 0x04, 0x05, 0x06, 0x07}
	got := updateSessionKey(initial, initial, gcc.ENCRYPTION_FLAG_40BIT)
	if len(got) != 8 || !bytes.Equal(got[:3], []byte{0xd1, 0x26, 0x9e}) {
		t.Fatalf("40 bit key = %x, want the d1269e salt and 8 bytes", got)
	}

	initial = []byte{0xd1, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07}
	got = updateSessionKey(initial, initial, gcc.ENCRYPTION_FLAG_56BIT)
	if len(got) != 8 || got[0] != 0xd1 {
		t.Fatalf("56 bit key = %x, want the d1 salt and 8 bytes", got)
	}
	if bytes.Equal(got, initial) {
		t.Fatal("the updated key equals the initial one")
	}
}

// Key material that does not belong together must be refused instead of
// deriving a key of the wrong length, which is what a mismatched encryption
// method or a truncated key would otherwise do.
func TestUpdateSessionKeyRejectsBadKeyMaterial(t *testing.T) {
	short := make([]byte, 8)
	long := make([]byte, 16)

	for _, tc := range []struct {
		name             string
		initial, current []byte
		method           uint32
	}{
		{"nil keys", nil, nil, gcc.ENCRYPTION_FLAG_128BIT},
		{"empty keys", []byte{}, []byte{}, gcc.ENCRYPTION_FLAG_128BIT},
		{"different lengths", long, short, gcc.ENCRYPTION_FLAG_128BIT},
		{"odd length", make([]byte, 20), make([]byte, 20), gcc.ENCRYPTION_FLAG_128BIT},
		{"8 byte key for a 128 bit session", short, short, gcc.ENCRYPTION_FLAG_128BIT},
		{"16 byte key for a 40 bit session", long, long, gcc.ENCRYPTION_FLAG_40BIT},
		{"16 byte key for a 56 bit session", long, long, gcc.ENCRYPTION_FLAG_56BIT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateSessionKey(tc.initial, tc.current, tc.method); got != nil {
				t.Fatalf("updated key = %x, want nil", got)
			}
		})
	}
}

// newKeyUpdateSEC builds a SEC whose session keys are already established, so
// the rollover can be driven without a server.
func newKeyUpdateSEC(method uint32, key []byte) *SEC {
	s := NewSEC(newFakeTransport())
	s.enableEncryption = true
	s.encryptionMethod = method
	s.macKey = bytes.Repeat([]byte{0x5a}, 16)
	s.initialEncryptKey = append([]byte(nil), key...)
	s.currentEncryptKey = append([]byte(nil), key...)
	s.initialDecrytKey = append([]byte(nil), key...)
	s.currentDecrytKey = append([]byte(nil), key...)
	return s
}

// newSessionKeys gives a client the 128 bit session keys and the MAC key that
// the Standard RDP Security exchange would have established.
func newSessionKeys(c *Client) {
	clientRandom := bytes.Repeat([]byte{0x01}, 32)
	serverRandom := bytes.Repeat([]byte{0x02}, 32)
	c.enableEncryption = true
	c.encryptionMethod = gcc.ENCRYPTION_FLAG_128BIT
	c.macKey, c.initialDecrytKey, c.initialEncryptKey = generateKeys(clientRandom, serverRandom, c.encryptionMethod)
	c.currentDecrytKey = c.initialDecrytKey
	c.currentEncryptKey = c.initialEncryptKey
}

func encryptWithReference(t *testing.T, cipher *rc4.Cipher, plain []byte) []byte {
	t.Helper()
	out := make([]byte, len(plain))
	cipher.XORKeyStream(out, plain)
	return out
}

// The first 4096 packets are encrypted with the key the session was established
// with, and the next one with the updated key. This is the loose end: the roll
// over used to be an empty branch, so packet 4097 continued the same RC4 stream
// and the server read it as noise.
func TestEncryptKeyRollsOverAfter4096Packets(t *testing.T) {
	initial := bytes.Repeat([]byte{0x11}, 16)
	s := newKeyUpdateSEC(gcc.ENCRYPTION_FLAG_128BIT, initial)
	plain := []byte("a share control PDU would be here")

	ref, err := rc4.NewCipher(initial)
	if err != nil {
		t.Fatalf("reference cipher: %v", err)
	}
	for i := 0; i < sessionKeyUpdateCount; i++ {
		got := s.writeEncryptedPayload(plain, false)
		if !bytes.Equal(got[8:], encryptWithReference(t, ref, plain)) {
			t.Fatalf("packet %d is not encrypted with the initial key", i)
		}
		if s.nbEncryptedPacket != i+1 {
			t.Fatalf("packet %d: count = %d, want %d", i, s.nbEncryptedPacket, i+1)
		}
	}

	updated := updateSessionKey(initial, initial, gcc.ENCRYPTION_FLAG_128BIT)
	if bytes.Equal(updated, initial) {
		t.Fatal("the updated key equals the initial key")
	}
	next, err := rc4.NewCipher(updated)
	if err != nil {
		t.Fatalf("reference cipher: %v", err)
	}
	got := s.writeEncryptedPayload(plain, false)
	if !bytes.Equal(got[8:], encryptWithReference(t, next, plain)) {
		t.Fatalf("packet %d is not encrypted with the updated key", sessionKeyUpdateCount)
	}
	if s.nbEncryptedPacket != 1 {
		t.Fatalf("count after the update = %d, want 1", s.nbEncryptedPacket)
	}
	if !bytes.Equal(s.currentEncryptKey, updated) {
		t.Fatal("the current key was not replaced by the updated one")
	}
	if !bytes.Equal(s.initialEncryptKey, initial) {
		t.Fatal("the initial key was modified; later updates derive from it")
	}
}

// The receiving side has to roll over at the same packet. Keeping the old key
// would turn the packet the server re-keyed into noise.
func TestDecryptKeyRollsOverAfter4096Packets(t *testing.T) {
	initial := bytes.Repeat([]byte{0x22}, 16)
	s := newKeyUpdateSEC(gcc.ENCRYPTION_FLAG_128BIT, initial)
	plain := []byte("a bitmap update PDU would be here")

	ref, err := rc4.NewCipher(initial)
	if err != nil {
		t.Fatalf("reference cipher: %v", err)
	}
	for i := 0; i < sessionKeyUpdateCount; i++ {
		data := append(make([]byte, 8), encryptWithReference(t, ref, plain)...)
		if got := s.readEncryptedPayload(data, false); !bytes.Equal(got, plain) {
			t.Fatalf("packet %d decrypted to %x, want %x", i, got, plain)
		}
	}

	updated := updateSessionKey(initial, initial, gcc.ENCRYPTION_FLAG_128BIT)
	next, err := rc4.NewCipher(updated)
	if err != nil {
		t.Fatalf("reference cipher: %v", err)
	}
	data := append(make([]byte, 8), encryptWithReference(t, next, plain)...)
	if got := s.readEncryptedPayload(data, false); !bytes.Equal(got, plain) {
		t.Fatalf("packet %d decrypted to %x, want %x", sessionKeyUpdateCount, got, plain)
	}
	if s.nbDecryptedPacket != 1 {
		t.Fatalf("count after the update = %d, want 1", s.nbDecryptedPacket)
	}
	if !bytes.Equal(s.currentDecrytKey, updated) {
		t.Fatal("the current key was not replaced by the updated one")
	}
}

// A long connection updates the keys more than once, and every update derives
// from the initial key and the current one rather than from the current one
// twice. The peer here follows the same rule, so a client that disagreed about
// when to update, or about what to update from, fails to decrypt.
func TestKeyUpdateKeepsBothSidesInSync(t *testing.T) {
	initial := bytes.Repeat([]byte{0x33}, 16)
	s := newKeyUpdateSEC(gcc.ENCRYPTION_FLAG_128BIT, initial)
	plain := []byte("payload")

	peerKey := append([]byte(nil), initial...)
	peer, err := rc4.NewCipher(peerKey)
	if err != nil {
		t.Fatalf("peer cipher: %v", err)
	}

	const packets = 2*sessionKeyUpdateCount + 7
	for i := 0; i < packets; i++ {
		if i > 0 && i%sessionKeyUpdateCount == 0 {
			peerKey = updateSessionKey(initial, peerKey, gcc.ENCRYPTION_FLAG_128BIT)
			if peerKey == nil {
				t.Fatalf("packet %d: the peer could not update its key", i)
			}
			peer, err = rc4.NewCipher(peerKey)
			if err != nil {
				t.Fatalf("packet %d: peer cipher: %v", i, err)
			}
		}
		got := s.writeEncryptedPayload(plain, false)
		out := make([]byte, len(plain))
		peer.XORKeyStream(out, got[8:])
		if !bytes.Equal(out, plain) {
			t.Fatalf("packet %d did not decrypt with the peer's key schedule", i)
		}
	}
}

// The Security Exchange PDU has to carry SEC_LICENSE_ENCRYPT_SC, which is what
// makes the server encrypt its licensing PDUs (MS-RDPBCGR 3.2.5.3.10). It must
// not be encrypted itself: there are no session keys yet at that point.
func TestSecurityExchangeAdvertisesLicenseEncryption(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	c, ft, _ := newStdSecClient(t, proprietaryCert(key))
	if err := c.sendClientRandom(); err != nil {
		t.Fatalf("sendClientRandom: %v", err)
	}

	raw := ft.written.Bytes()
	if len(raw) < 4 {
		t.Fatalf("exchange PDU is %d bytes", len(raw))
	}
	flag := binary.LittleEndian.Uint16(raw[0:2])
	if flag&EXCHANGE_PKT == 0 {
		t.Fatalf("security flags = 0x%04x, want SEC_EXCHANGE_PKT", flag)
	}
	if flag&LICENSE_ENCRYPT_SC == 0 {
		t.Fatalf("security flags = 0x%04x, want SEC_LICENSE_ENCRYPT_SC", flag)
	}
	if flag&ENCRYPT != 0 {
		t.Fatalf("security flags = 0x%04x, the exchange PDU must not be encrypted", flag)
	}
}

func writeUint16LE(b *bytes.Buffer, v uint16) {
	_ = binary.Write(b, binary.LittleEndian, v)
}

func writeUint32LE(b *bytes.Buffer, v uint32) {
	_ = binary.Write(b, binary.LittleEndian, v)
}

// validClientAlert builds the LICENSE_ERROR_MESSAGE with STATUS_VALID_CLIENT
// that a server answers the license request with and that ends the licensing
// phase.
func validClientAlert() []byte {
	out := &bytes.Buffer{}
	out.WriteByte(0xFF) // ERROR_ALERT
	out.WriteByte(0x02) // PREAMBLE_VERSION_3_0
	writeUint16LE(out, 12)
	writeUint32LE(out, 0x00000007) // STATUS_VALID_CLIENT
	writeUint32LE(out, 0x00000002) // ST_NO_TRANSITION
	return out.Bytes()
}

// encryptedLicensePDU wraps a licensing packet the way a server that was told
// the client can process encrypted licensing packets sends it: a security
// header with SEC_LICENSE_PKT and SEC_ENCRYPT, then the MAC and the ciphertext.
// The server encrypts what it sends with the key this client decrypts with, so
// the fixture uses currentDecrytKey.
func encryptedLicensePDU(t *testing.T, c *Client, payload []byte) []byte {
	t.Helper()
	cipher, err := rc4.NewCipher(c.currentDecrytKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	body := make([]byte, len(payload))
	cipher.XORKeyStream(body, payload)

	out := &bytes.Buffer{}
	writeUint16LE(out, LICENSE_PKT|ENCRYPT)
	writeUint16LE(out, 0)
	out.Write(make([]byte, 8)) // dataSignature; this client does not verify it
	out.Write(body)
	return out.Bytes()
}

// recvLicenceInfo has to decrypt a licensing PDU that arrives encrypted, or the
// license preamble is read out of ciphertext and the phase fails.
func TestRecvLicenseInfoDecryptsEncryptedLicensePacket(t *testing.T) {
	c, _, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	newSessionKeys(c)

	connected := false
	c.On("connect", func(*gcc.ClientCoreData, uint16, uint16) { connected = true })

	c.recvLicenceInfo("global", encryptedLicensePDU(t, c, validClientAlert()))
	if !connected {
		t.Fatal("an encrypted license packet did not complete the licensing phase")
	}
}

// The control: a server that answers in the clear, which is what xrdp does,
// still works.
func TestRecvLicenseInfoAcceptsPlaintextLicensePacket(t *testing.T) {
	c, _, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	newSessionKeys(c)

	connected := false
	c.On("connect", func(*gcc.ClientCoreData, uint16, uint16) { connected = true })

	raw := &bytes.Buffer{}
	writeUint16LE(raw, LICENSE_PKT)
	writeUint16LE(raw, 0)
	raw.Write(validClientAlert())
	c.recvLicenceInfo("global", raw.Bytes())
	if !connected {
		t.Fatal("a plaintext license packet did not complete the licensing phase")
	}
}

// Hostile input: an encrypted license packet with no MAC, one with an empty
// body, and one whose plaintext is nonsense must not take the process down.
func TestRecvLicenseInfoRejectsShortEncryptedPacket(t *testing.T) {
	c, _, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	newSessionKeys(c)

	errs := 0
	connected := false
	c.On("error", func(error) { errs++ })
	c.On("connect", func(*gcc.ClientCoreData, uint16, uint16) { connected = true })

	for _, body := range [][]byte{
		{},
		{0x01},
		bytes.Repeat([]byte{0x00}, 8),
		bytes.Repeat([]byte{0xff}, 40),
	} {
		raw := &bytes.Buffer{}
		writeUint16LE(raw, LICENSE_PKT|ENCRYPT)
		writeUint16LE(raw, 0)
		raw.Write(body)
		c.recvLicenceInfo("global", raw.Bytes())
	}

	// None of them parsed as a license packet that ends the phase.
	if errs == 0 {
		t.Log("no error was reported for the malformed license packets")
	}
	if connected {
		t.Fatal("a malformed license packet completed the licensing phase")
	}
}

// A licensing PDU this client sends is marked SEC_LICENSE_PKT and
// SEC_LICENSE_ENCRYPT_CS and encrypted with the session keys, the way FreeRDP
// sends it.
func TestSendLicensePktEncryptsUnderTheStandardSecurityLayer(t *testing.T) {
	c, ft, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	newSessionKeys(c)

	payload := validClientAlert()
	c.sendLicensePkt(payload)

	raw := ft.written.Bytes()
	if len(raw) < 12 {
		t.Fatalf("license PDU is %d bytes, want a header, a MAC and the payload", len(raw))
	}
	flag := binary.LittleEndian.Uint16(raw[0:2])
	if flag&LICENSE_PKT == 0 || flag&ENCRYPT == 0 || flag&LICENSE_ENCRYPT_CS == 0 {
		t.Fatalf("security flags = 0x%04x, want SEC_LICENSE_PKT|SEC_ENCRYPT|SEC_LICENSE_ENCRYPT_CS", flag)
	}

	cipher, err := rc4.NewCipher(c.currentEncryptKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	plain := make([]byte, len(raw)-12)
	cipher.XORKeyStream(plain, raw[12:])
	if !bytes.Equal(plain, payload) {
		t.Fatalf("license payload = %x, want %x", plain, payload)
	}
}

// Under TLS or NLA there are no session keys, so a licensing PDU goes out in
// the clear exactly as it did before.
func TestSendLicensePktStaysPlaintextWithoutStandardSecurity(t *testing.T) {
	c, ft, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})

	payload := validClientAlert()
	c.sendLicensePkt(payload)

	raw := ft.written.Bytes()
	flag := binary.LittleEndian.Uint16(raw[0:2])
	if flag != LICENSE_PKT {
		t.Fatalf("security flags = 0x%04x, want 0x%04x", flag, LICENSE_PKT)
	}
	if !bytes.Equal(raw[4:], payload) {
		t.Fatalf("license payload = %x, want %x", raw[4:], payload)
	}
}

// The licensing keys are not the session keys, so deriving them must leave the
// session MAC key and the update keys alone.
func TestLicenseKeysDoNotReplaceTheSessionKeys(t *testing.T) {
	c, _, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	newSessionKeys(c)
	sessionMAC := append([]byte(nil), c.macKey...)
	sessionDecrypt := append([]byte(nil), c.initialDecrytKey...)

	// What sendClientNewLicenseRequest derives, without needing a server
	// certificate to send it to.
	clientRandom := bytes.Repeat([]byte{0x0a}, 32)
	serverRandom := bytes.Repeat([]byte{0x0b}, 32)
	preMasterSecret := bytes.Repeat([]byte{0x0c}, 48)
	master := masterSecret(preMasterSecret, clientRandom, serverRandom)
	blob := masterSecret(master, serverRandom, clientRandom)
	c.licenseMacKey = blob[:16]
	c.licenseKey = finalHash(blob[16:32], clientRandom, serverRandom)

	if !bytes.Equal(c.macKey, sessionMAC) {
		t.Fatalf("the session MAC key changed to %x", c.macKey)
	}
	if !bytes.Equal(c.initialDecrytKey, sessionDecrypt) {
		t.Fatalf("the session update key changed to %x", c.initialDecrytKey)
	}
	if bytes.Equal(c.licenseMacKey, c.macKey) {
		t.Fatal("the licensing MAC key is the session MAC key")
	}
}

// A platform challenge carrying a blob longer than the 20 bytes it is defined
// to carry used to run past the end of the destination buffer.
func TestSendChallengeResponseBoundsTheChallenge(t *testing.T) {
	c, ft, _ := newStdSecClient(t, &gcc.ProprietaryServerCertificate{})
	newSessionKeys(c)
	// These would have been set by the license request.
	c.licenseKey = bytes.Repeat([]byte{0x44}, 16)
	c.licenseMacKey = bytes.Repeat([]byte{0x55}, 16)

	challenge := &bytes.Buffer{}
	writeUint32LE(challenge, 0) // ConnectFlags
	report := make([]byte, 200)
	if err := packedBlob(challenge, report, 0x0009); err != nil {
		t.Fatalf("build challenge: %v", err)
	}
	challenge.Write(make([]byte, 16)) // MACData

	c.sendClientChallengeResponse(challenge.Bytes())

	raw := ft.written.Bytes()
	if len(raw) == 0 {
		t.Fatal("no challenge response was sent")
	}
	if flag := binary.LittleEndian.Uint16(raw[0:2]); flag&LICENSE_PKT == 0 {
		t.Fatalf("security flags = 0x%04x, want SEC_LICENSE_PKT", flag)
	}
}

// packedBlob writes a LICENSE_BINARY_BLOB the way the licensing parser reads
// one: the blob type and length are tagged little-endian.
func packedBlob(b *bytes.Buffer, data []byte, blobType uint16) error {
	writeUint16LE(b, blobType)
	writeUint16LE(b, uint16(len(data)))
	_, err := b.Write(data)
	return err
}
