package pdu

import (
	"bytes"
	"testing"
)

// The bitmap cache is what makes orders work: secondary orders fill it and
// MEMBLT reads it back. These cover decoding one cache entry, which is the part
// that can be checked without a server.

func TestCachePixelsUncompressed(t *testing.T) {
	// 2x1 at 32bpp, stored rather than compressed.
	raw := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	got := cachePixels(raw, 2, 1, 32, false)
	if !bytes.Equal(got, raw) {
		t.Fatalf("got %v, want %v", got, raw)
	}
	// It has to be a copy: the caller keeps these bytes after the order buffer
	// has been reused.
	got[0] = 99
	if raw[0] == 99 {
		t.Error("the stored bitmap was aliased rather than copied")
	}
}

func TestCachePixelsRejectsImpossibleShape(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		bpp  int
		raw  []byte
	}{
		{"zero width", 0, 4, 32, []byte{1, 2, 3, 4}},
		{"negative height", 4, -1, 32, []byte{1, 2, 3, 4}},
		{"bpp is not a whole byte", 2, 2, 15, []byte{1, 2, 3, 4}},
		{"too short for its size", 4, 4, 32, []byte{1, 2, 3, 4}},
		{"no data at all", 4, 4, 32, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cachePixels(c.raw, c.w, c.h, c.bpp, false); got != nil {
				t.Errorf("got %d bytes, want nil", len(got))
			}
		})
	}
}

// The compressed path is deliberately not unit tested here: it is the same
// decoder bitmap updates use, and its own tests cover it. What is worth
// recording is that it is not safe on malformed input, see the note in
// docs/windows-verification.md.
