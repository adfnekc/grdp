package codec

import (
	"encoding/binary"
	"os"
	"testing"
)

// TestDecodeCapturedZGFX decodes a capture of real channel payloads with our
// decoder and writes the results, so the reference decoder can be run over the
// same streams and the two compared.
//
// This is how the decoder was checked against real Windows traffic rather than
// against streams from our own test encoder. The validation in
// zgfx_vectors_test.go covers the format thoroughly, but the streams there come
// from us, so a shared misreading would survive it. Real captures have no such
// problem. The capture itself is the concatenation of length prefixed channel
// payloads that plugin/rdpgfx writes when GRDP_DUMP_ZGFX is set.
//
//	GRDP_DUMP_ZGFX=/tmp/real.bin go run ./cmd/rdpcli -host H -user U -pass P \
//	    -proto nla -egfx -wait 20s -bitmaps 0
//	ZGFX_REAL_IN=/tmp/real.bin ZGFX_REAL_OUT=/tmp/plain.bin \
//	    go test ./codec -run TestDecodeCapturedZGFX
//	# then decode the same streams with scripts/gen-zgfx-vectors.c, which
//	# compares its output against /tmp/plain.bin and reports any difference
func TestDecodeCapturedZGFX(t *testing.T) {
	in, out := os.Getenv("ZGFX_REAL_IN"), os.Getenv("ZGFX_REAL_OUT")
	if in == "" || out == "" {
		t.Skip("set ZGFX_REAL_IN and ZGFX_REAL_OUT")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var streams [][]byte
	for len(raw) >= 4 {
		n := int(binary.LittleEndian.Uint32(raw))
		raw = raw[4:]
		if n > len(raw) {
			break
		}
		streams = append(streams, raw[:n])
		raw = raw[n:]
	}

	z := NewZGFX()
	decoded := make([][]byte, 0, len(streams))
	for i, s := range streams {
		d, err := z.Decompress(s)
		if err != nil {
			t.Fatalf("stream %d (%d bytes): %v", i, len(s), err)
		}
		decoded = append(decoded, d)
	}
	writeZGFXRecords(t, out, decoded)
	t.Logf("decoded %d streams", len(streams))
}
