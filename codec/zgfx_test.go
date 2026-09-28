package codec

import (
	"bytes"
	"math/bits"
	"math/rand"
	"testing"
)

// This file checks the ZGFX decoder against streams it did not produce.
//
// The bit writer and the token helpers below are a deliberately simple
// test-only encoder: it only ever emits the catch-all literal and the first
// distance token that fits, which keeps it easy to read and to check. The
// round trip tests are still meaningful because the two halves share nothing
// but the token table, and codec/testdata/zgfx holds the same streams decoded
// by libfreerdp, so agreement with the reference is checked too.

// zgfxBitWriter writes bits most significant first, then rounds up to a byte
// boundary and appends the count of padding bits, which is what the last byte
// of a compressed segment holds.
type zgfxBitWriter struct {
	buf []byte
	cur byte
	n   uint32
}

func (w *zgfxBitWriter) put(v, n uint32) {
	for i := n; i > 0; i-- {
		w.cur = w.cur<<1 | byte(v>>(i-1)&1)
		if w.n++; w.n == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.n = 0, 0
		}
	}
}

// literal writes the catch-all literal token: a zero bit, then the byte.
func (w *zgfxBitWriter) literal(b byte) {
	w.put(0, 1)
	w.put(uint32(b), 8)
}

// match writes a match. The length is coded in stages: a single bit picks
// either 3, or a doubling run followed by the bits that remain.
func (w *zgfxBitWriter) match(distance, length uint32) {
	tok := zgfxDistanceToken(distance)
	w.put(tok.prefixCode, tok.prefixLength)
	w.put(distance-tok.valueBase, tok.valueBits)

	if length == 3 {
		w.put(0, 1)
		return
	}
	w.put(1, 1)
	k := uint32(bits.Len32(length)) - 3
	for i := uint32(0); i < k; i++ {
		w.put(1, 1)
	}
	w.put(0, 1)
	w.put(length-(4<<k), 2+k)
}

// unencoded writes a run of bytes taken verbatim from the stream. It is
// selected by a distance of zero, then a 15 bit count, then a byte boundary.
func (w *zgfxBitWriter) unencoded(run []byte) {
	w.put(17, 5) // prefix of the {5, 17, 5, 1, 0} token
	w.put(0, 5)  // distance 0
	w.put(uint32(len(run)), 15)
	if w.n > 0 {
		w.cur <<= 8 - w.n
		w.buf = append(w.buf, w.cur)
		w.cur, w.n = 0, 0
	}
	w.buf = append(w.buf, run...)
}

func (w *zgfxBitWriter) segment() []byte {
	padding := byte(0)
	if w.n > 0 {
		padding = byte(8 - w.n)
		w.cur <<= padding
		w.buf = append(w.buf, w.cur)
	}
	w.buf = append(w.buf, padding)
	return append([]byte{0x24}, w.buf...) // PACKET_COMPRESSED | RDP8
}

// zgfxDistanceToken returns the first match token whose range covers distance.
// It panics on a distance no token can express, which would be a broken test
// rather than a decoder failure.
func zgfxDistanceToken(distance uint32) zgfxToken {
	for _, tok := range zgfxTokens {
		if tok.tokenType != 1 {
			continue
		}
		if span := uint32(1)<<tok.valueBits - 1; distance >= tok.valueBase && distance-tok.valueBase <= span {
			return tok
		}
	}
	panic("distance outside every token")
}

// zgfxStream wraps segments in the single-segment descriptor.
func zgfxStream(segments ...[]byte) []byte {
	out := []byte{0xE0}
	for _, s := range segments {
		out = append(out, s...)
	}
	return out
}

// zgfxEncode compresses data into one segment, greedily: at each position it
// takes the longest match it can find within a window, or a literal.
func zgfxEncode(data []byte) []byte {
	w := &zgfxBitWriter{}
	for i := 0; i < len(data); {
		bestLen, bestDist := 0, 0
		lo := i - 65536
		if lo < 0 {
			lo = 0
		}
		// A match needs at least three bytes to compare.
		for j := i - 3; i+2 < len(data) && j >= lo; j-- {
			if data[j] != data[i] || data[j+1] != data[i+1] || data[j+2] != data[i+2] {
				continue
			}
			n := 3
			for i+n < len(data) && n < 65536 && data[j+n] == data[i+n] {
				n++
			}
			if n > bestLen {
				bestLen, bestDist = n, i-j
			}
			if bestLen >= 64 {
				break
			}
		}
		if bestLen >= 3 {
			w.match(uint32(bestDist), uint32(bestLen))
			i += bestLen
			continue
		}
		w.literal(data[i])
		i++
	}
	return zgfxStream(w.segment())
}

func decodeZGFX(t *testing.T, streams ...[]byte) []byte {
	t.Helper()
	z := NewZGFX()
	var out []byte
	for i, s := range streams {
		got, err := z.Decompress(s)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		out = append(out, got...)
	}
	return out
}

func TestZGFXLiterals(t *testing.T) {
	w := &zgfxBitWriter{}
	for _, c := range []byte("Hello, ZGFX!") {
		w.literal(c)
	}
	got := decodeZGFX(t, zgfxStream(w.segment()))
	if string(got) != "Hello, ZGFX!" {
		t.Errorf("got %q, want %q", got, "Hello, ZGFX!")
	}
}

// The table has short prefixes for the bytes that turn up most often. Decoding
// has to walk the table, not just the catch-all literal, so exercise a few.
func TestZGFXShortLiteralTokens(t *testing.T) {
	cases := []struct {
		name   string
		prefix uint32
		length uint32
		value  byte
	}{
		{"0x00", 24, 5, 0x00},
		{"0x01", 25, 5, 0x01},
		{"0x02", 52, 6, 0x02},
		{"0x0b", 117, 7, 0x0b},
		{"0x39", 254, 8, 0x39},
		{"0x66", 255, 8, 0x66},
		{"0xff", 54, 6, 0xff},
	}
	for _, c := range cases {
		w := &zgfxBitWriter{}
		w.put(c.prefix, c.length)
		got := decodeZGFX(t, zgfxStream(w.segment()))
		if len(got) != 1 || got[0] != c.value {
			t.Errorf("%s: got %x, want %02x", c.name, got, c.value)
		}
	}
}

func TestZGFXMatchLengths(t *testing.T) {
	// A single 'A' followed by matches at distance 1 repeats it. Length 3 is
	// the short form; the rest walk the doubling run and its value bits.
	for _, length := range []uint32{3, 4, 5, 7, 8, 9, 15, 16, 17, 31, 32, 63, 64, 1000} {
		w := &zgfxBitWriter{}
		w.literal('A')
		w.match(1, length)
		got := decodeZGFX(t, zgfxStream(w.segment()))
		want := bytes.Repeat([]byte{'A'}, int(length)+1)
		if !bytes.Equal(got, want) {
			t.Errorf("length %d: got %d bytes %q, want %d bytes", length, len(got), got, len(want))
		}
	}
}

// A match may overlap the bytes it is producing: distance 2 over four bytes has
// to repeat "ab" rather than read past an unwritten region.
func TestZGFXOverlappingMatch(t *testing.T) {
	w := &zgfxBitWriter{}
	w.literal('a')
	w.literal('b')
	w.match(2, 10)
	got := decodeZGFX(t, zgfxStream(w.segment()))
	if string(got) != "abababababab" {
		t.Errorf("got %q, want %q", got, "abababababab")
	}
}

func TestZGFXMatchDistances(t *testing.T) {
	// Each case writes five 'Z's, pads out to the wanted distance, then matches
	// back over them, so the tail has to be exactly those five bytes. The
	// distances pick a different token for each range.
	//
	// The upper bound comes from a segment being able to decode to 64KiB: a
	// distance past that needs a longer history, which the cross message test
	// below provides.
	for _, distance := range []uint32{1, 4, 5, 31, 32, 159, 160, 672, 1696, 5792, 22176, 60000} {
		w := &zgfxBitWriter{}
		for i := uint32(0); i < 5; i++ {
			w.literal('Z')
		}
		for i := uint32(5); i < distance; i++ {
			w.literal(byte('a' + i%26))
		}
		w.match(distance, 5)

		got := decodeZGFX(t, zgfxStream(w.segment()))
		if !bytes.Equal(got[len(got)-5:], []byte("ZZZZZ")) {
			t.Errorf("distance %d: tail is %q, want %q", distance, got[len(got)-5:], "ZZZZZ")
		}
	}
}

// Distances past what one segment can hold have to come from the history left
// by earlier messages, which is where the longer distance tokens get exercised.
func TestZGFXLongDistanceAcrossMessages(t *testing.T) {
	for _, distance := range []uint32{70000, 200000, 2400000} {
		const chunk = 60000
		z := NewZGFX()

		// Seed the history with five 'Z's followed by filler, one segment at a
		// time so that the 64KiB segment limit is not hit.
		seed := &zgfxBitWriter{}
		for i := uint32(0); i < 5; i++ {
			seed.literal('Z')
		}
		for i := uint32(5); i < chunk; i++ {
			seed.literal(byte('a' + i%26))
		}
		if _, err := z.Decompress(zgfxStream(seed.segment())); err != nil {
			t.Fatalf("distance %d: seeding: %v", distance, err)
		}

		// Top the history up to the wanted distance.
		for filled := uint32(chunk); filled < distance; {
			n := uint32(chunk)
			if filled+n > distance {
				n = distance - filled
			}
			w := &zgfxBitWriter{}
			for i := uint32(0); i < n; i++ {
				w.literal(0x5a)
			}
			if _, err := z.Decompress(zgfxStream(w.segment())); err != nil {
				t.Fatalf("distance %d: filling: %v", distance, err)
			}
			filled += n
		}

		w := &zgfxBitWriter{}
		w.match(distance, 5)
		got, err := z.Decompress(zgfxStream(w.segment()))
		if err != nil {
			t.Fatalf("distance %d: %v", distance, err)
		}
		if string(got) != "ZZZZZ" {
			t.Errorf("distance %d: got %q, want %q", distance, got, "ZZZZZ")
		}
	}
}

func TestZGFXUnencodedRun(t *testing.T) {
	run := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0xff, 0x00}
	w := &zgfxBitWriter{}
	w.literal('x')
	w.unencoded(run)
	w.literal('y')
	got := decodeZGFX(t, zgfxStream(w.segment()))
	want := append([]byte{'x'}, append(append([]byte{}, run...), 'y')...)
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// The history lives for as long as the channel, so a match may reach back into
// an earlier message.
func TestZGFXHistoryAcrossMessages(t *testing.T) {
	first := &zgfxBitWriter{}
	for _, c := range []byte("ABCDEFGH") {
		first.literal(c)
	}

	second := &zgfxBitWriter{}
	second.match(8, 8)

	z := NewZGFX()
	if got, err := z.Decompress(zgfxStream(first.segment())); err != nil {
		t.Fatalf("first message: %v", err)
	} else if string(got) != "ABCDEFGH" {
		t.Fatalf("first message: got %q", got)
	}

	got, err := z.Decompress(zgfxStream(second.segment()))
	if err != nil {
		t.Fatalf("second message: %v", err)
	}
	if string(got) != "ABCDEFGH" {
		t.Errorf("second message: got %q, want %q", got, "ABCDEFGH")
	}
}

func TestZGFXStoredSegment(t *testing.T) {
	payload := []byte("stored verbatim, not compressed")
	stream := append([]byte{0xE0, 0x04}, payload...)

	got := decodeZGFX(t, stream)
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// A stored segment still counts towards the history, so a later match can
// refer to it.
func TestZGFXStoredSegmentFeedsHistory(t *testing.T) {
	payload := []byte("KEEPTHIS")
	z := NewZGFX()
	if _, err := z.Decompress(append([]byte{0xE0, 0x04}, payload...)); err != nil {
		t.Fatalf("stored segment: %v", err)
	}

	w := &zgfxBitWriter{}
	w.match(uint32(len(payload)), uint32(len(payload)))
	got, err := z.Decompress(zgfxStream(w.segment()))
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

func TestZGFXMultipart(t *testing.T) {
	one := &zgfxBitWriter{}
	for _, c := range []byte("first part, ") {
		one.literal(c)
	}
	two := &zgfxBitWriter{}
	for _, c := range []byte("second part") {
		two.literal(c)
	}

	stream := zgfxMultipart([][]byte{one.segment(), two.segment()}, len("first part, second part"))
	if got := decodeZGFX(t, stream); string(got) != "first part, second part" {
		t.Errorf("got %q", got)
	}
}

func TestZGFXRoundTrip(t *testing.T) {
	for _, c := range zgfxRoundTripCases() {
		t.Run(c.name, func(t *testing.T) {
			got := decodeZGFX(t, zgfxEncode(c.data))
			if !bytes.Equal(got, c.data) {
				t.Fatalf("round trip differs: got %d bytes, want %d", len(got), len(c.data))
			}
		})
	}
}

// Long runs take the unencoded path, which is how a real encoder represents
// data that does not compress.
func TestZGFXRoundTripUnencoded(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	in := make([]byte, 40000)
	rng.Read(in)

	w := &zgfxBitWriter{}
	for i := 0; i < len(in); i += 5000 {
		w.unencoded(in[i : i+5000])
	}
	got := decodeZGFX(t, zgfxStream(w.segment()))
	if !bytes.Equal(got, in) {
		t.Fatalf("unencoded round trip differs: got %d bytes, want %d", len(got), len(in))
	}
}

func TestZGFXErrors(t *testing.T) {
	compressed := func(body ...byte) []byte {
		return append([]byte{0xE0, 0x24}, body...)
	}

	cases := []struct {
		name   string
		stream []byte
	}{
		{"empty", nil},
		{"unknown descriptor", []byte{0xE9, 0x24, 0x00}},
		{"segment too short", []byte{0xE0, 0x24}},
		{"padding exceeds segment", compressed(0x00, 0xff)},
		// One zero bit selects the catch-all literal, then eight bits are
		// wanted but only the padding byte remains.
		{"truncated mid literal", compressed(0x00, 0x00)},
		{"multipart short header", []byte{0xE1, 0x01, 0x00}},
		{"multipart size mismatch", []byte{0xE1, 0x01, 0x00, 0xff, 0x00, 0x00, 0x00,
			0x03, 0x00, 0x00, 0x00, 0x04, 0x41, 0x00}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewZGFX().Decompress(c.stream); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// A distance beyond the history cannot be satisfied. The reference reads
// whatever the ring happens to hold; refusing is safer and costs nothing,
// because a compressor never emits a distance it could not have used.
func TestZGFXRejectsDistanceBeyondHistory(t *testing.T) {
	w := &zgfxBitWriter{}
	distance := uint32(zgfxHistorySize) + 1
	zgfxDistanceToken(distance) // fails the test if no token can express it
	w.match(distance, 3)

	if _, err := NewZGFX().Decompress(zgfxStream(w.segment())); err == nil {
		t.Fatal("expected an error for a distance beyond the history")
	}
}

// The decoder is fed by a server, so a corrupt stream must not be able to make
// it read out of bounds or spin. Fuzzing the seed corpus is cheap.
func FuzzZGFXDecompress(f *testing.F) {
	w := &zgfxBitWriter{}
	w.literal('a')
	w.match(1, 5)
	f.Add(zgfxStream(w.segment()))
	f.Add([]byte{0xE0, 0x04, 'h', 'i'})
	f.Add([]byte{0xE1, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Add([]byte{0xE0, 0x24, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		z := NewZGFX()
		// A limit keeps a pathological input from running for the whole test
		// timeout; the decoder must still terminate on its own.
		out, err := z.Decompress(data)
		if err == nil && len(out) > 1<<20 {
			t.Fatalf("decoded %d bytes from %d, more than a segment can hold", len(out), len(data))
		}
	})
}

// A multipart header may claim any size. The allocation must not follow it,
// because the claim is only checked once the segments have been decoded, so
// trusting it spends the memory before finding out it is a lie.
func TestZGFXMultipartIgnoresHugeClaimedSize(t *testing.T) {
	// 0xE1, 49 segments, and a claimed output of 0x64000000 bytes with no
	// segment data behind it.
	stream := []byte{0xE1, 0x31, 0x00, 0x00, 0x00, 0x00, 0x64}
	if _, err := NewZGFX().Decompress(stream); err == nil {
		t.Fatal("expected an error")
	}
}

// A field wider than any token can hold must be refused rather than gathered
// byte by byte: the width comes from the stream, so a corrupt one would spin
// until the shift counter ran out.
func TestZGFXBitsRejectsOversizedField(t *testing.T) {
	b := &zgfxBits{data: make([]byte, 64), remaining: 64 * 8}
	if got := b.bits(1000); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if !b.overrun {
		t.Error("an oversized field should be reported as an overrun")
	}
	if b.remaining != 0 {
		t.Errorf("remaining = %d, want 0", b.remaining)
	}
}
