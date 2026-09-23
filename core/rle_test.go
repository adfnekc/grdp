// rle_test.go
package core

import (
	"testing"
)

// truncatedRLE is a real-world sample that ends mid-stream. It is kept as a
// robustness corpus: Decompress must never panic on truncated/malformed input.
var truncatedRLE = []byte{
	192, 44, 200, 8, 132, 200, 8, 200, 8, 200, 8, 200, 8, 0, 19, 132, 232, 8, 12, 50, 142, 66, 77, 58, 208, 59, 225, 25, 1, 0, 0, 0, 0, 0, 0, 0, 132, 139, 33, 142, 66, 142, 66, 142, 66, 208, 59, 4, 43, 1, 0, 0, 0, 0, 0, 0, 0, 132, 203, 41, 142, 66, 142, 66, 142, 66, 208, 59, 96, 0, 1, 0, 0, 0, 0, 0, 0, 0, 132, 9, 17, 142, 66, 142, 66, 142, 66, 208, 59, 230, 27, 1, 0, 0, 0, 0, 0, 0, 0, 132, 200, 8, 9, 17, 139, 33, 74, 25, 243, 133, 14, 200, 8, 132, 200, 8, 200, 8, 200, 8, 200, 8,
}

// TestDecompressTruncatedInput verifies that a truncated RLE stream neither
// panics nor returns a wrongly-sized buffer.
func TestDecompressTruncatedInput(t *testing.T) {
	const (
		width  = 64
		height = 64
		bpp    = 3
	)
	out := Decompress(truncatedRLE, width, height, bpp)
	if len(out) != width*height*bpp {
		t.Fatalf("decompressed size = %d, want %d", len(out), width*height*bpp)
	}
}

// TestDecompressEmptyInput covers the empty-input edge case.
func TestDecompressEmptyInput(t *testing.T) {
	out := Decompress(nil, 8, 8, 3)
	if len(out) != 8*8*3 {
		t.Fatalf("decompressed size = %d, want %d", len(out), 8*8*3)
	}
}
