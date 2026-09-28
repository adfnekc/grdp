package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// The vectors in testdata/zgfx_vectors.bin are produced by
// scripts/gen-zgfx-vectors.sh.
//
// ZGFX is the one codec here whose reference encoder cannot be used: FreeRDP's
// compressor is a stub that only stores bytes. So the streams come from the
// test-only encoder below, and FreeRDP's decoder supplies the reference output.
// That still checks what matters, because a stream our decoder misreads will
// not agree with what the reference decoded it to. The generator additionally
// refuses to write a vector unless the reference output matches the plaintext
// we asked for, which is what catches a stream our encoder built wrongly.
//
// The streams must be decoded in order through one decoder: the history is
// shared between messages, and one of the vectors depends on that.

// zgfxCase is a named payload for the round trip tests.
type zgfxCase struct {
	name string
	data []byte
}

// zgfxRoundTripCases is the input set used both for the round trip tests and
// for the reference vectors, so the same data is checked both ways.
func zgfxRoundTripCases() []zgfxCase {
	rng := rand.New(rand.NewSource(20240521))
	incompressible := make([]byte, 20000)
	rng.Read(incompressible)
	mixed := append(bytes.Repeat([]byte("abcabcabc"), 500), incompressible[:4000]...)

	return []zgfxCase{
		{"empty", []byte{}},
		{"single", []byte{'q'}},
		{"zeroes", bytes.Repeat([]byte{0}, 3000)},
		{"repeated", bytes.Repeat([]byte("remote desktop protocol "), 400)},
		{"runs", bytes.Repeat([]byte{0x00, 0x00, 0x00, 0xff, 0x11, 0x22}, 700)},
		// The encoder still finds stray matches in incompressible data, which
		// is exactly the mixture the decoder has to cope with.
		{"incompress", incompressible},
		{"mixed", mixed},
	}
}

// zgfxMultipart builds a multipart stream from already encoded segments.
func zgfxMultipart(segments [][]byte, uncompressedSize int) []byte {
	out := []byte{0xE1}
	count := make([]byte, 2)
	binary.LittleEndian.PutUint16(count, uint16(len(segments)))
	out = append(out, count...)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(uncompressedSize))
	out = append(out, size...)
	for _, s := range segments {
		l := make([]byte, 4)
		binary.LittleEndian.PutUint32(l, uint32(len(s)))
		out = append(out, l...)
		out = append(out, s...)
	}
	return out
}

// zgfxVectorPairs returns the streams to check against the reference decoder,
// each paired with the bytes it has to decode to.
func zgfxVectorPairs() [][2][]byte {
	var pairs [][2][]byte
	add := func(stream, want []byte) {
		pairs = append(pairs, [2][]byte{stream, want})
	}

	// Every byte value, through the catch-all literal.
	all := &zgfxBitWriter{}
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
		all.literal(byte(i))
	}
	add(zgfxStream(all.segment()), allBytes)

	// Every short literal token in the table, in table order.
	short := &zgfxBitWriter{}
	var shortBytes []byte
	for _, tok := range zgfxTokens {
		if tok.tokenType != 0 || tok.valueBits != 0 || tok.prefixLength == 1 {
			continue
		}
		short.put(tok.prefixCode, tok.prefixLength)
		shortBytes = append(shortBytes, byte(tok.valueBase))
	}
	add(zgfxStream(short.segment()), shortBytes)

	// Every length form: the short one, and the doubling run either side of
	// each of its steps.
	lens := &zgfxBitWriter{}
	lens.literal('A')
	wantLens := []byte{'A'}
	for _, n := range []uint32{3, 4, 5, 7, 8, 15, 16, 17, 31, 32, 63, 64, 127, 128, 255, 256, 1000, 4096} {
		lens.match(1, n)
		wantLens = append(wantLens, bytes.Repeat([]byte{'A'}, int(n))...)
	}
	add(zgfxStream(lens.segment()), wantLens)

	// A match that runs over the bytes it is producing.
	overlap := &zgfxBitWriter{}
	overlap.literal('a')
	overlap.literal('b')
	overlap.match(2, 3000)
	add(zgfxStream(overlap.segment()), bytes.Repeat([]byte("ab"), 1501))

	// One stream per distance range, so every value width in the table is
	// exercised. Each writes three 'Z's, pads out to the distance, then matches
	// back over them.
	for _, distance := range []uint32{1, 31, 32, 159, 160, 672, 1696, 5792, 22176, 60000} {
		w := &zgfxBitWriter{}
		var want []byte
		for i := uint32(0); i < 3; i++ {
			w.literal('Z')
			want = append(want, 'Z')
		}
		for i := uint32(3); i < distance; i++ {
			c := byte('a' + i%26)
			w.literal(c)
			want = append(want, c)
		}
		w.match(distance, 3)
		want = append(want, 'Z', 'Z', 'Z')
		add(zgfxStream(w.segment()), want)
	}

	// Unencoded runs, which is how data the encoder cannot compress travels.
	rng := rand.New(rand.NewSource(20240521))
	un := &zgfxBitWriter{}
	var unWant []byte
	for i := 0; i < 3; i++ {
		run := make([]byte, 2000)
		rng.Read(run)
		un.unencoded(run)
		unWant = append(unWant, run...)
	}
	add(zgfxStream(un.segment()), unWant)

	// A stored segment: the stream says its bytes are not compressed.
	stored := []byte("stored, not compressed")
	add(append([]byte{0xE0, 0x04}, stored...), stored)

	// This stream matches back into the stored segment's bytes, so it only
	// decodes correctly if the history carried over from the previous message.
	back := &zgfxBitWriter{}
	back.match(uint32(len(stored)), 5)
	add(zgfxStream(back.segment()), stored[:5])

	// A multipart stream, which carries several segments and their sizes.
	one := &zgfxBitWriter{}
	for _, c := range []byte("first part, ") {
		one.literal(c)
	}
	two := &zgfxBitWriter{}
	for _, c := range []byte("second part") {
		two.literal(c)
	}
	add(zgfxMultipart([][]byte{one.segment(), two.segment()}, len("first part, second part")),
		[]byte("first part, second part"))

	// The round trip inputs, so the reference checks the same data.
	for _, c := range zgfxRoundTripCases() {
		add(zgfxEncode(c.data), c.data)
	}

	return pairs
}

// TestZGFXWriteReferenceInputs writes the streams and their expected plaintext
// for scripts/gen-zgfx-vectors.sh to feed to the reference decoder. It is
// skipped unless GRDP_ZGFX_VECTOR_DIR is set.
func TestZGFXWriteReferenceInputs(t *testing.T) {
	dir := os.Getenv("GRDP_ZGFX_VECTOR_DIR")
	if dir == "" {
		t.Skip("set GRDP_ZGFX_VECTOR_DIR to regenerate the ZGFX reference vectors")
	}

	pairs := zgfxVectorPairs()
	streams := make([][]byte, len(pairs))
	plains := make([][]byte, len(pairs))
	for i, p := range pairs {
		streams[i], plains[i] = p[0], p[1]
	}

	writeZGFXRecords(t, filepath.Join(dir, "streams.bin"), streams)
	writeZGFXRecords(t, filepath.Join(dir, "plain.bin"), plains)
	t.Logf("wrote %d streams to %s", len(streams), dir)
}

// TestZGFXMatchesReferenceDecoder decodes the vectors published by
// scripts/gen-zgfx-vectors.sh and requires byte for byte agreement with what
// libfreerdp produced.
func TestZGFXMatchesReferenceDecoder(t *testing.T) {
	raw, err := os.ReadFile("testdata/zgfx_vectors.bin")
	if err != nil {
		t.Skip("missing testdata/zgfx_vectors.bin; run scripts/gen-zgfx-vectors.sh")
	}

	pairs, err := parseZGFXVectors(raw)
	if err != nil {
		t.Fatalf("parsing the vector file: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatal("the vector file holds no streams")
	}

	// One decoder, fed in order: the history is shared between messages.
	z := NewZGFX()
	for i, p := range pairs {
		got, err := z.Decompress(p[0])
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if !bytes.Equal(got, p[1]) {
			t.Errorf("stream %d (%d bytes in, %d bytes out): differs from the reference output of %d bytes",
				i, len(p[0]), len(got), len(p[1]))
		}
	}
}

func writeZGFXRecords(t *testing.T, path string, records [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	write := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		buf.Write(b[:])
	}
	write(uint32(len(records)))
	for _, r := range records {
		write(uint32(len(r)))
		buf.Write(r)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func parseZGFXVectors(raw []byte) ([][2][]byte, error) {
	if len(raw) < 8 || string(raw[:4]) != "ZGFX" {
		return nil, fmt.Errorf("not a ZGFX vector file")
	}
	count := binary.LittleEndian.Uint32(raw[4:8])
	raw = raw[8:]

	read := func() ([]byte, error) {
		if len(raw) < 4 {
			return nil, fmt.Errorf("truncated")
		}
		n := binary.LittleEndian.Uint32(raw[:4])
		raw = raw[4:]
		if uint32(len(raw)) < n {
			return nil, fmt.Errorf("truncated")
		}
		out := raw[:n]
		raw = raw[n:]
		return out, nil
	}

	out := make([][2][]byte, 0, count)
	for i := uint32(0); i < count; i++ {
		stream, err := read()
		if err != nil {
			return nil, err
		}
		want, err := read()
		if err != nil {
			return nil, err
		}
		out = append(out, [2][]byte{stream, want})
	}
	return out, nil
}
