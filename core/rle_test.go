package core

import (
	"testing"
)

// truncatedRLE is a real-world sample that ends mid-stream. It is kept as a
// robustness corpus: Decompress must never panic or spin on truncated input.
var truncatedRLE = []byte{
	192, 44, 200, 8, 132, 200, 8, 200, 8, 200, 8, 200, 8, 0, 19, 132, 232, 8, 12, 50, 142, 66, 77, 58, 208, 59, 225, 25, 1, 0, 0, 0, 0, 0, 0, 0, 132, 139, 33, 142, 66, 142, 66, 142, 66, 208, 59, 4, 43, 1, 0, 0, 0, 0, 0, 0, 0, 132, 203, 41, 142, 66, 142, 66, 142, 66, 208, 59, 96, 0, 1, 0, 0, 0, 0, 0, 0, 0, 132, 9, 17, 142, 66, 142, 66, 142, 66, 208, 59, 230, 27, 1, 0, 0, 0, 0, 0, 0, 0, 132, 200, 8, 9, 17, 139, 33, 74, 25, 243, 133, 14, 200, 8, 132, 200, 8, 200, 8, 200, 8, 200, 8,
}

// TestDecompressTruncatedInput verifies that a truncated RLE stream neither
// panics nor returns a wrongly-sized buffer.
//
// No error is expected, and that is not an oversight: the 8, 16 and 24 bit
// formats are terminated by the buffer ending, so a stream that ends early is
// indistinguishable from one that ended on purpose. Only the interleaved form,
// which carries an explicit size, can tell.
func TestDecompressTruncatedInput(t *testing.T) {
	const (
		width  = 64
		height = 64
		bpp    = 3
	)
	out, err := Decompress(truncatedRLE, width, height, bpp)
	if len(out) != width*height*bpp {
		t.Fatalf("decompressed size = %d, want %d", len(out), width*height*bpp)
	}
	if err != nil {
		t.Errorf("the 24 bit format cannot detect this, so no error was expected, got %v", err)
	}
}

// TestDecompressEmptyInput covers the empty-input edge case.
func TestDecompressEmptyInput(t *testing.T) {
	out, err := Decompress(nil, 8, 8, 3)
	if len(out) != 8*8*3 {
		t.Fatalf("decompressed size = %d, want %d", len(out), 8*8*3)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestDecompressInterleavedStopsWhenInputRunsOut is the regression test for a
// spin.
//
// Interleaved decoding reads a code and then a run. Once the input is gone,
// every read returns zero, and a zero code means a run of zero pixels: the
// inner loop advances neither the input nor the output and never finishes. A
// four byte payload like this one used to run for minutes rather than return,
// and a server chooses the payload, so it was a way to hang a client.
func TestDecompressInterleavedStopsWhenInputRunsOut(t *testing.T) {
	// 0x10 selects the interleaved form, then nothing but padding.
	for _, raw := range [][]byte{
		{0x10},
		{0x10, 0x00},
		{0x10, 0x00, 0x00, 0x00},
		{0x10, 0xf2, 0x11},
	} {
		out, err := Decompress(raw, 16, 16, 4)
		if len(out) != 16*16*4 {
			t.Errorf("%x: got %d bytes, want %d", raw, len(out), 16*16*4)
		}
		if err == nil {
			t.Errorf("%x: truncated input should be reported as an error", raw)
		}
	}
}

// A run longer than the line must not walk past the end of the buffer.
func TestDecompressOversizedRunStaysInBounds(t *testing.T) {
	// Interleaved form, then a code whose run length is far wider than the
	// four pixel line, repeated for each plane.
	raw := []byte{0x10}
	for i := 0; i < 8; i++ {
		raw = append(raw, 0xff, 0x00, 0x00, 0x00)
	}
	out, err := Decompress(raw, 4, 4, 4)
	if len(out) != 4*4*4 {
		t.Fatalf("got %d bytes, want %d", len(out), 4*4*4)
	}
	_ = err // Malformed either way; the point is that it comes back at all.
}

func TestDecompressRejectsBadGeometry(t *testing.T) {
	cases := []struct {
		name    string
		w, h, B int
	}{
		{"zero width", 0, 4, 4},
		{"negative height", 4, -1, 4},
		{"no bytes per pixel", 4, 4, 0},
		{"too many bytes per pixel", 4, 4, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out, err := Decompress([]byte{1, 2, 3, 4}, c.w, c.h, c.B); err == nil {
				t.Errorf("got %d bytes and no error", len(out))
			}
		})
	}
}

// The decoder is fed by a server, so malformed input must not be able to make it
// spin or read out of bounds.
func FuzzDecompress(f *testing.F) {
	f.Add(truncatedRLE, 64, 64, 3)
	f.Add([]byte{0x10, 0x00, 0x00, 0x00}, 16, 16, 4)
	f.Add([]byte{0x10, 0xff, 0xff}, 4, 4, 4)
	f.Add([]byte{}, 8, 8, 1)
	f.Add([]byte{0x01, 0x02}, 2, 2, 2)

	f.Fuzz(func(t *testing.T, data []byte, width, height, bpp int) {
		// Keep the geometry small: the allocator would be the only thing under
		// test otherwise.
		if width <= 0 || height <= 0 || bpp < 1 || bpp > 4 {
			return
		}
		if width > 256 || height > 256 {
			return
		}
		out, _ := Decompress(data, width, height, bpp)
		if len(out) != width*height*bpp {
			t.Fatalf("got %d bytes, want %d", len(out), width*height*bpp)
		}
	})
}
