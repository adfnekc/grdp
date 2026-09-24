package nla

import (
	"encoding/hex"
	"testing"
)

// The two binding prefixes are what make the client and server hashes differ,
// and the trailing NUL is part of both strings. A missing NUL, or the two
// prefixes swapped, produces a hash the server will not accept, so pin the
// exact output against an independent implementation (Python hashlib).
func TestBindingHashes(t *testing.T) {
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	spki, _ := hex.DecodeString("30820122300d06092a864886f70d010101")

	gotC2S := hex.EncodeToString(ClientToServerHash(nonce, spki))
	gotS2C := hex.EncodeToString(ServerToClientHash(nonce, spki))

	const (
		wantC2S = "d0260979d0934614902edaef3c2257561dfd64f59a9c8b559481c07b15ef739d"
		wantS2C = "1a8c2eef1c5328a925fca64ffd219b9a7b343e5092163a09e0731f8891337a2d"
	)
	if gotC2S != wantC2S {
		t.Errorf("ClientToServerHash = %s, want %s", gotC2S, wantC2S)
	}
	if gotS2C != wantS2C {
		t.Errorf("ServerToClientHash = %s, want %s", gotS2C, wantS2C)
	}
	if gotC2S == gotS2C {
		t.Error("the two directions must not produce the same hash")
	}
}

// A zero value must not accidentally acquire the legacy meaning: version 6 is
// what modern Windows servers offer.
func TestVersionConstants(t *testing.T) {
	if VersionNonce != 6 || VersionSha256 != 5 {
		t.Fatalf("unexpected version constants: %d and %d", VersionNonce, VersionSha256)
	}
	if NonceLen != 32 {
		t.Fatalf("nonce length is %d, want 32", NonceLen)
	}
}

func TestNonceIsEncodedInTSRequest(t *testing.T) {
	// The nonce only reaches the server if it actually lands in the DER.
	nonce := make([]byte, NonceLen)
	for i := range nonce {
		nonce[i] = 0xAB
	}
	req := EncodeDERTRequestVersion(VersionNonce, nil, nil, []byte{1, 2, 3}, nonce)
	back, err := DecodeDERTRequest(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Version != VersionNonce {
		t.Fatalf("version round tripped as %d", back.Version)
	}
	if len(back.ClientNonce) != NonceLen {
		t.Fatalf("nonce round tripped as %d bytes", len(back.ClientNonce))
	}
	if len(back.PubKeyAuth) != 3 {
		t.Fatalf("pubKeyAuth round tripped as %d bytes", len(back.PubKeyAuth))
	}
}

func TestLegacyExpectedServerResponse(t *testing.T) {
	in := []byte{0x30, 0x82, 0x01, 0x22}
	got := LegacyExpectedServerResponse(in)
	want := []byte{0x31, 0x82, 0x01, 0x22}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %x, want %x", got, want)
		}
	}
	if in[0] != 0x30 {
		t.Fatal("the input must not be modified in place")
	}
	// A leading 0xFF must wrap rather than panic or saturate.
	if got := LegacyExpectedServerResponse([]byte{0xFF}); got[0] != 0x00 {
		t.Fatalf("expected a wrap to 0x00, got %x", got)
	}
}
