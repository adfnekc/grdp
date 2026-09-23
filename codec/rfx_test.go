package codec

import (
	"bytes"
	"testing"
)

// Decoding RemoteFX has a lot of places to get subtly wrong: the entropy
// decoder, the differential reconstruction of the DC band, dequantisation, the
// inverse wavelet transform and the colour transform. Anything less than a
// byte exact match against a reference decoder is a bug, so these tests demand
// exact equality rather than a tolerance.

func TestRFXAgainstReferenceDecoder(t *testing.T) {
	tests := []struct {
		encoded string
		raw     string
		w, h    int
		mode    RLGRMode
	}{
		{"rfx_64x64_rlgr1.bin", "raw_64x64.bin", 64, 64, RLGR1},
		{"rfx_64x64_rlgr3.bin", "raw_64x64.bin", 64, 64, RLGR3},
		// Two tiles wide, so this also covers tile placement.
		{"rfx_128x64_rlgr3.bin", "raw_128x64.bin", 128, 64, RLGR3},
	}

	for _, tt := range tests {
		t.Run(tt.encoded, func(t *testing.T) {
			encoded := loadVector(t, tt.encoded)
			ref := loadVector(t, "rfx_dec_"+tt.encoded[len("rfx_"):])

			got, err := DecodeRFX(encoded, tt.w, tt.h, tt.mode)
			if err != nil {
				t.Fatalf("DecodeRFX: %v", err)
			}
			if !bytes.Equal(got, ref) {
				_, meanDiff := pixelError(t, got, ref, tt.w, tt.h)
				t.Fatalf("output differs from the reference decoder (mean %d)", meanDiff)
			}

			// Report how far the (lossy) codec round trip landed from the
			// original, for context only.
			orig := loadVector(t, tt.raw)
			maxDiff, meanDiff := pixelError(t, got, orig, tt.w, tt.h)
			t.Logf("identical to reference decoder; max error vs original %d, mean %d",
				maxDiff, meanDiff)
		})
	}
}

func TestDecodeRFXErrors(t *testing.T) {
	good := loadVector(t, "rfx_64x64_rlgr3.bin")

	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"truncated header", good[:4]},
		{"truncated block", good[:20]},
		{"trailing bytes", append(append([]byte{}, good...), 0xFF)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeRFX(tt.data, 64, 64, RLGR3); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	if _, err := DecodeRFX(good, 0, 0, RLGR3); err == nil {
		t.Fatal("expected an error for a zero sized surface")
	}
}

func TestParseRFXBlocks(t *testing.T) {
	// The block list must parse exactly, with no bytes left over, which
	// pins the block framing including the extra codec/channel header that
	// WBT_CONTEXT through WBT_EXTENSION carry.
	r, err := parseRFX(loadVector(t, "rfx_64x64_rlgr3.bin"))
	if err != nil {
		t.Fatalf("parseRFX: %v", err)
	}
	if len(r.rects) != 1 {
		t.Fatalf("got %d rects, want 1", len(r.rects))
	}
	if r.rects[0] != (rfxRect{x: 0, y: 0, width: 64, height: 64}) {
		t.Fatalf("got rect %+v", r.rects[0])
	}
	if len(r.tiles) != 1 {
		t.Fatalf("got %d tiles, want 1", len(r.tiles))
	}
	if len(r.quants) != 10 {
		t.Fatalf("got %d quantisation values, want 10", len(r.quants))
	}
	if r.tiles[0].xIdx != 0 || r.tiles[0].yIdx != 0 {
		t.Fatalf("got tile index %d,%d", r.tiles[0].xIdx, r.tiles[0].yIdx)
	}
}

func TestParseRFXTwoTiles(t *testing.T) {
	r, err := parseRFX(loadVector(t, "rfx_128x64_rlgr3.bin"))
	if err != nil {
		t.Fatalf("parseRFX: %v", err)
	}
	if len(r.tiles) != 2 {
		t.Fatalf("got %d tiles, want 2", len(r.tiles))
	}
	if r.tiles[0].xIdx != 0 || r.tiles[1].xIdx != 1 {
		t.Fatalf("unexpected tile indices %d and %d", r.tiles[0].xIdx, r.tiles[1].xIdx)
	}
}

func TestInRects(t *testing.T) {
	rects := []rfxRect{{x: 10, y: 20, width: 5, height: 5}}
	tests := []struct {
		x, y int
		want bool
	}{
		{10, 20, true},
		{14, 24, true},
		{15, 25, false},
		{9, 20, false},
	}
	for _, tt := range tests {
		if got := inRects(tt.x, tt.y, rects); got != tt.want {
			t.Errorf("inRects(%d,%d) = %v, want %v", tt.x, tt.y, got, tt.want)
		}
	}
	if !inRects(999, 999, nil) {
		t.Error("an empty rectangle list should accept everything")
	}
}
