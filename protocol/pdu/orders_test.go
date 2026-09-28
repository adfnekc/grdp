package pdu

import (
	"bytes"
	"hash/fnv"
	"os"
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

// The captured order below is one Cache Bitmap Revision 2 order taken off a real
// Windows server, exactly as it arrived after the order header.
//
// It is here because the fields in this order are not fixed width: bitmapWidth,
// bitmapHeight and cacheIndex use the compact two byte form and bitmapLength the
// compact four byte form (MS-RDPEGDI). Reading them as fixed width shifts
// everything after them, and the symptom is not an obviously broken parse but
// one wrong byte at the front of the bitmap, which decodes to nothing at all.
// That is precisely what happened, and hand written test bytes had the same
// mistake as the parser.
func TestCacheBitmapV2RealWindowsOrder(t *testing.T) {
	body, err := os.ReadFile("testdata/cache_v2_order.bin")
	if err != nil {
		t.Skipf("missing testdata/cache_v2_order.bin: %v", err)
	}
	if len(body) != 522 {
		t.Fatalf("fixture is %d bytes, expected 522", len(body))
	}

	// From the order header: cacheId 2, 32bpp, and the flags
	// HEIGHT_SAME_AS_WIDTH, NO_BITMAP_COMPRESSION_HDR and DO_NOT_CACHE set.
	const flags = 0x0cb2
	var sec Secondary
	sec.updateCacheBitmapV2Order(bytes.NewReader(body), true, flags)

	cb := sec.CacheBitmap
	if cb == nil {
		t.Fatal("the captured order produced no cache bitmap")
	}
	if cb.Width != 64 || cb.Height != 64 || cb.Bpp != 32 {
		t.Fatalf("geometry is %dx%d at %d bpp, want 64x64 at 32", cb.Width, cb.Height, cb.Bpp)
	}
	if cb.CacheIndex != 0x7FFF {
		// DO_NOT_CACHE maps the index to the waiting list.
		t.Errorf("cache index is %d, want 0x7FFF", cb.CacheIndex)
	}
	if len(cb.Pixels) != 64*64*4 {
		t.Fatalf("decoded %d bytes, want %d", len(cb.Pixels), 64*64*4)
	}

	// A checksum pins the pixels down: a one byte shift in the fields above
	// produces a different image, and "it decoded to something" would not
	// notice.
	nonzero := 0
	h := fnv.New32a()
	h.Write(cb.Pixels)
	for _, v := range cb.Pixels {
		if v != 0 {
			nonzero++
		}
	}
	// The checksum pins the exact pixels. A field read one byte short shifts the
	// bitmap and changes them, which is the failure this fixture exists for, and
	// "it decoded to something" would not see it.
	if nonzero != 8192 {
		t.Errorf("decoded bitmap has %d non-zero bytes, want 8192", nonzero)
	}
	if got, want := h.Sum32(), uint32(0xe3ef1dc5); got != want {
		t.Errorf("decoded bitmap checksum is %#08x, want %#08x", got, want)
	}
}
