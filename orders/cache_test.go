package orders

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/adfnekc/grdp/codec"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// codecVector loads a payload or a reference decode from the codec package's
// testdata. Those vectors are produced by libfreerdp's own codecs, so a match
// says this cache used the codec correctly rather than that two copies of our
// code agree.
func codecVector(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../codec/testdata/" + name)
	if err != nil {
		t.Skipf("missing codec test vector %s; run scripts/gen-codec-vectors.sh", name)
	}
	return b
}

// nscHeader builds a TS_NSC_DATA header: the four plane byte counts, the
// colour loss level and the chroma subsampling level.
func nscHeader(counts [4]uint32, colorLoss byte) []byte {
	h := make([]byte, 20)
	for i, n := range counts {
		binary.LittleEndian.PutUint32(h[i*4:], n)
	}
	h[16] = colorLoss
	return h
}

// nscOnePixel builds a one by one NSCodec payload whose four planes are one
// byte each. A one byte plane is stored rather than run-length encoded, so the
// payload is the header, Y, Co, Cg and A and nothing else.
func nscOnePixel(y, co, cg, a byte) []byte {
	return append(nscHeader([4]uint32{1, 1, 1, 1}, 1), y, co, cg, a)
}

// A revision 3 cache entry carries a codec id and encoded data, and it used to
// be dropped. It must now decode, and to the pixels the codec actually means:
// NSCodec reconstructs B = y - co - cg, G = y + cg, R = y + co - cg.
func TestCacheDecodesSyntheticNSCodecEntry(t *testing.T) {
	c := NewCache()
	c.Put(&pdu.CacheBitmap{
		CacheID: 1, CacheIndex: 2, Width: 1, Height: 1, Bpp: 32,
		CodecID: codec.CodecIDNSCODEC,
		Data:    nscOnePixel(100, 10, 5, 0xff),
	})

	e := c.Get(1, 2)
	if e == nil {
		t.Fatal("a decodable revision 3 entry was dropped")
	}
	if e.Width != 1 || e.Height != 1 {
		t.Fatalf("entry is %dx%d, want 1x1", e.Width, e.Height)
	}
	if want := []byte{85, 105, 105, 0xff}; !bytes.Equal(e.Pixels, want) {
		t.Fatalf("decoded %v, want BGRA %v", e.Pixels, want)
	}
}

// The captured payloads are what a real server actually sends, which hand built
// bytes cannot stand in for. This checks the whole Put path against the pixels
// libfreerdp's NS decoder produced for the same payload.
func TestCacheDecodesCapturedNSCodecEntry(t *testing.T) {
	encoded := codecVector(t, "nsc_64x64_loss1_sub0.bin")
	want := codecVector(t, "nsc_dec_64x64_loss1_sub0.bin")

	c := NewCache()
	c.Put(&pdu.CacheBitmap{
		CacheID: 0, CacheIndex: 3, Width: 64, Height: 64, Bpp: 32,
		CodecID: codec.CodecIDNSCODEC,
		Data:    encoded,
	})

	e := c.Get(0, 3)
	if e == nil {
		t.Fatal("a captured NSCodec entry was dropped")
	}
	if !bytes.Equal(e.Pixels, want) {
		t.Fatalf("decoded %d bytes that differ from the libfreerdp decoder output", len(e.Pixels))
	}
}

// RemoteFX data does not say which entropy coder produced it, so a cache must
// use the mode the session negotiated rather than the codec.RFXMode global.
// This leaves that global at its RLGR1 default and asks the cache for RLGR3: if
// the cache read the global it would decode the RLGR3 vector as RLGR1, which
// cannot match the RLGR3 reference.
func TestCacheRemoteFXUsesItsOwnModeNotTheGlobal(t *testing.T) {
	encoded := codecVector(t, "rfx_64x64_rlgr3.bin")
	want := codecVector(t, "rfx_dec_64x64_rlgr3.bin")

	old := codec.RFXMode
	codec.RFXMode = codec.RLGR1
	defer func() { codec.RFXMode = old }()

	c := NewCache()
	c.RFXMode = codec.RLGR3
	c.Put(&pdu.CacheBitmap{
		CacheID: 1, CacheIndex: 0, Width: 64, Height: 64, Bpp: 32,
		CodecID: codec.CodecIDRemoteFX,
		Data:    encoded,
	})

	e := c.Get(1, 0)
	if e == nil {
		t.Fatal("a captured RemoteFX entry was dropped")
	}
	if !bytes.Equal(e.Pixels, want) {
		t.Fatalf("decoded %d bytes that differ from the libfreerdp decoder output", len(e.Pixels))
	}
}

// A cache that has not been told otherwise must decode RemoteFX as RLGR1, and
// it must do so without consulting codec.RFXMode even when that global has been
// set to the other mode by another session.
func TestCacheRemoteFXDefaultsToRLGR1(t *testing.T) {
	encoded := codecVector(t, "rfx_64x64_rlgr1.bin")
	want := codecVector(t, "rfx_dec_64x64_rlgr1.bin")

	old := codec.RFXMode
	codec.RFXMode = codec.RLGR3
	defer func() { codec.RFXMode = old }()

	c := NewCache()
	if c.RFXMode != codec.RLGR1 {
		t.Fatalf("a new cache defaults to mode %d, want RLGR1", c.RFXMode)
	}
	c.Put(&pdu.CacheBitmap{
		CacheID: 1, CacheIndex: 1, Width: 64, Height: 64, Bpp: 32,
		CodecID: codec.CodecIDRemoteFX,
		Data:    encoded,
	})

	e := c.Get(1, 1)
	if e == nil {
		t.Fatal("a captured RemoteFX entry was dropped")
	}
	if !bytes.Equal(e.Pixels, want) {
		t.Fatal("the default mode did not decode the RLGR1 vector")
	}
}

// A codec id the decoder does not implement, or a payload that will not decode,
// leaves the slot empty rather than putting rubbish on screen.
func TestCacheDropsUndecodableCodecEntries(t *testing.T) {
	c := NewCache()

	// 0x05 is X264, which this package does not decode.
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 1, Width: 64, Height: 64, Bpp: 32,
		CodecID: 0x05, Data: []byte{1, 2, 3}})
	// NSCodec with a header that stops short.
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 2, Width: 64, Height: 64, Bpp: 32,
		CodecID: codec.CodecIDNSCODEC, Data: []byte{1, 2, 3}})
	// RemoteFX whose block list is nonsense.
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 3, Width: 64, Height: 64, Bpp: 32,
		CodecID: codec.CodecIDRemoteFX, Data: []byte{1, 2, 3}})
	// A codec id with no data at all.
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 4, Width: 64, Height: 64, Bpp: 32,
		CodecID: codec.CodecIDNSCODEC})

	if c.Len() != 0 {
		t.Errorf("cache holds %d undecodable entries, want 0", c.Len())
	}
}

// The geometry of a codec entry comes off the wire and the decoder allocates
// width by height by four before it looks at the payload, so a cell past the
// limit has to be refused before the decode. The payload is a valid NSCodec
// stream: zero length planes are constants, so without the bound it decodes to a
// full 2048 by 2049 cell and is stored, which is what the test catches.
func TestCacheRefusesOversizedCodecEntry(t *testing.T) {
	c := NewCache()
	c.Put(&pdu.CacheBitmap{CacheID: 1, CacheIndex: 1, Width: 2048, Height: 2049, Bpp: 32,
		CodecID: codec.CodecIDNSCODEC, Data: nscHeader([4]uint32{}, 1)})

	if c.Len() != 0 {
		t.Errorf("a %d byte cell was decoded and kept", 2048*2049*4)
	}
}

// The limit is inclusive: a cell of exactly the largest permitted size is not
// refused, and one pixel more is.
func TestEntrySizeBoundary(t *testing.T) {
	if entryTooLarge(2048, 2048) {
		t.Error("a cell exactly at the size limit was refused")
	}
	if !entryTooLarge(2048, 2049) {
		t.Error("a cell one row over the size limit was accepted")
	}
	if !entryTooLarge(maxEntryDimension+1, 1) {
		t.Error("a cell over the dimension limit was accepted")
	}
}
