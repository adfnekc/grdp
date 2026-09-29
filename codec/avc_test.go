package codec

import (
	"bytes"
	"errors"
	"testing"
)

// avcRegion is the test-side spelling of a region rectangle.
type avcRegion struct{ left, top, right, bottom uint16 }

func le32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// metablock builds an RDPGFX_H264_METABLOCK: the region count, then the
// rectangles, then the quantisation pairs.
func metablock(regions []avcRegion, quants []AVCQuantQuality) []byte {
	out := le32(uint32(len(regions)))
	for _, r := range regions {
		out = append(out, byte(r.left), byte(r.left>>8), byte(r.top), byte(r.top>>8),
			byte(r.right), byte(r.right>>8), byte(r.bottom), byte(r.bottom>>8))
	}
	for _, q := range quants {
		out = append(out, q.QP|(q.R<<6)|(q.P<<7), q.Quality)
	}
	return out
}

func TestParseAVC420(t *testing.T) {
	regions := []avcRegion{{1, 2, 5, 6}, {10, 20, 30, 40}}
	quants := []AVCQuantQuality{
		{QP: 0x2A, R: 1, P: 0, Quality: 0x10},
		{QP: 0x3F, R: 0, P: 1, Quality: 0x20},
	}
	bitstream := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x99}
	data := append(metablock(regions, quants), bitstream...)

	au, err := ParseAVC420(data)
	if err != nil {
		t.Fatalf("ParseAVC420: %v", err)
	}
	if au.LC != 0 {
		t.Errorf("LC = %d, want 0", au.LC)
	}
	if len(au.Streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(au.Streams))
	}
	s := au.Streams[0]
	wantRegions := []RegionRect{{1, 2, 5, 6}, {10, 20, 30, 40}}
	if len(s.Regions) != len(wantRegions) {
		t.Fatalf("got %d regions, want %d", len(s.Regions), len(wantRegions))
	}
	for i, want := range wantRegions {
		if s.Regions[i] != want {
			t.Errorf("region %d = %+v, want %+v", i, s.Regions[i], want)
		}
	}
	wantQuants := []AVCQuantQuality{
		{QP: 0x2A, R: 1, P: 0, Quality: 0x10},
		{QP: 0x3F, R: 0, P: 1, Quality: 0x20},
	}
	if len(s.Quant) != len(wantQuants) {
		t.Fatalf("got %d quant entries, want %d", len(s.Quant), len(wantQuants))
	}
	for i, want := range wantQuants {
		if s.Quant[i] != want {
			t.Errorf("quant %d = %+v, want %+v", i, s.Quant[i], want)
		}
	}
	// The bitstream is everything after the metablock, opaque.
	if !bytes.Equal(s.Data, bitstream) {
		t.Errorf("bitstream = % x, want % x", s.Data, bitstream)
	}
}

// An AVC420 message whose region count is a different length than the list is
// nonsense and must be refused, not read as if the list were shorter.
func TestParseAVC420TruncatedRegions(t *testing.T) {
	// Two regions declared, one present.
	data := le32(2)
	data = append(data, metablock([]avcRegion{{1, 2, 3, 4}}, nil)[4:]...)
	if _, err := ParseAVC420(data); !errors.Is(err, errTruncated) {
		t.Fatalf("got err %v, want errTruncated", err)
	}
}

// The region count is bounded before it is used as an allocation size: a four
// byte message claiming a billion regions must be refused rather than
// allocating for them all. Without the bound this is where the parser would
// spend (or fail to get) 80 gigabytes.
func TestParseAVC420HugeRegionCountIsBounded(t *testing.T) {
	if _, err := ParseAVC420(le32(0x40000000)); !errors.Is(err, errTruncated) {
		t.Fatalf("got err %v, want errTruncated", err)
	}
	if _, err := ParseAVC420(le32(0xFFFFFFFF)); !errors.Is(err, errTruncated) {
		t.Fatalf("got err %v, want errTruncated", err)
	}
}

func TestParseAVC420Short(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty": nil,
		"one":   {0},
		"three": {0, 0, 0},
	} {
		if _, err := ParseAVC420(data); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// A zero region count needs only the four byte count: there is no region or
	// quantisation to read, and the rest of the message is the bitstream.
	au, err := ParseAVC420([]byte{0, 0, 0, 0, 7})
	if err != nil {
		t.Fatalf("zero regions: %v", err)
	}
	if len(au.Streams) != 1 || len(au.Streams[0].Regions) != 0 ||
		!bytes.Equal(au.Streams[0].Data, []byte{7}) {
		t.Fatalf("zero regions parsed as %+v", au)
	}
}

func avc444(header uint32, streams ...[]byte) []byte {
	out := le32(header)
	for _, s := range streams {
		out = append(out, s...)
	}
	return out
}

func TestParseAVC444TwoStreams(t *testing.T) {
	mb0 := metablock([]avcRegion{{0, 0, 64, 64}}, []AVCQuantQuality{{QP: 0x10, Quality: 0x30}})
	d0 := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0xAA, 0xBB}
	mb1 := metablock([]avcRegion{{1, 1, 3, 3}}, []AVCQuantQuality{{QP: 0x20, Quality: 0x40}})
	d1 := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xCC}

	// cbAvc420EncodedBitstream1 sizes the first sub-stream without its four
	// byte header.
	cb := uint32(len(mb0) + len(d0))
	payload := avc444(cb, append(mb0, d0...), append(mb1, d1...))

	au, err := ParseAVC444(payload)
	if err != nil {
		t.Fatalf("ParseAVC444: %v", err)
	}
	if au.LC != 0 {
		t.Errorf("LC = %d, want 0", au.LC)
	}
	if len(au.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(au.Streams))
	}
	if got := au.Streams[0].Regions; len(got) != 1 || got[0] != (RegionRect{0, 0, 64, 64}) {
		t.Errorf("stream 1 regions = %+v", got)
	}
	if !bytes.Equal(au.Streams[0].Data, d0) {
		t.Errorf("stream 1 data = % x, want % x", au.Streams[0].Data, d0)
	}
	if got := au.Streams[1].Regions; len(got) != 1 || got[0] != (RegionRect{1, 1, 3, 3}) {
		t.Errorf("stream 2 regions = %+v", got)
	}
	if !bytes.Equal(au.Streams[1].Data, d1) {
		t.Errorf("stream 2 data = % x, want % x", au.Streams[1].Data, d1)
	}
}

// LC 1 and LC 2 carry a single sub-stream that runs to the end of the message,
// and cbAvc420EncodedBitstream1 is not used to bound it.
func TestParseAVC444SingleStream(t *testing.T) {
	mb0 := metablock([]avcRegion{{2, 2, 8, 8}}, []AVCQuantQuality{{QP: 0x05, Quality: 0x06}})
	d0 := []byte{0x00, 0x00, 0x01, 0x65, 0x11}

	for _, lc := range []uint32{1, 2} {
		// A misleading cb value must be ignored when there is one stream.
		payload := avc444((lc<<30)|0x00000001, append(mb0, d0...))
		au, err := ParseAVC444(payload)
		if err != nil {
			t.Fatalf("LC=%d: %v", lc, err)
		}
		if au.LC != uint8(lc) {
			t.Errorf("LC = %d, want %d", au.LC, lc)
		}
		if len(au.Streams) != 1 {
			t.Fatalf("LC=%d: got %d streams, want 1", lc, len(au.Streams))
		}
		if !bytes.Equal(au.Streams[0].Data, d0) {
			t.Errorf("LC=%d: data = % x, want % x", lc, au.Streams[0].Data, d0)
		}
	}
}

// LC 3 is reserved and names no stream layout at all.
func TestParseAVC444ReservedLC(t *testing.T) {
	payload := avc444(3<<30, metablock(nil, nil))
	if _, err := ParseAVC444(payload); err == nil {
		t.Fatal("LC 3 should be refused")
	}
}

func TestParseAVC444Short(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":    nil,
		"one":      {0},
		"three":    {0, 0, 0},
		"no meta":  le32(0),
		"no meta2": avc444(0, metablock(nil, nil)), // LC 0 needs a second stream
	} {
		if _, err := ParseAVC444(data); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// cbAvc420EncodedBitstream1 can lie in either direction; both must be refused
// rather than reading before or past the message.
func TestParseAVC444BadStream1Size(t *testing.T) {
	mb0 := metablock([]avcRegion{{0, 0, 1, 1}}, nil) // 14 bytes

	// cb smaller than the metablock it is meant to bound.
	smaller := avc444(uint32(len(mb0)-1), append(mb0, 1, 2, 3, 4, 5))
	if _, err := ParseAVC444(smaller); err == nil {
		t.Error("a cb smaller than the metablock should be refused")
	}

	// cb larger than the bytes that follow.
	larger := avc444(uint32(len(mb0)+100), append(mb0, 1, 2, 3, 4, 5))
	if _, err := ParseAVC444(larger); err == nil {
		t.Error("a cb past the end of the message should be refused")
	}
}

// Fuzzing the parsers is cheap and reaches the hostile inputs a hand written
// test cannot enumerate. Neither parser may panic, read out of bounds or
// allocate without bound, and a successfully parsed unit must stay consistent
// with the message it came from.
func FuzzParseAVC(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0})
	f.Add(append(le32(1), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	f.Add(avc444(0, metablock([]avcRegion{{0, 0, 1, 1}}, nil), metablock(nil, nil)))
	f.Add(avc444(1<<30, metablock([]avcRegion{{0, 0, 1, 1}}, nil)))

	f.Fuzz(func(t *testing.T, data []byte) {
		if au, err := ParseAVC420(data); err == nil {
			if len(au.Streams) != 1 {
				t.Fatalf("AVC420 produced %d streams", len(au.Streams))
			}
			s := au.Streams[0]
			if len(s.Regions) != len(s.Quant) {
				t.Fatalf("%d regions but %d quant entries", len(s.Regions), len(s.Quant))
			}
			if len(s.Data)+4+len(s.Regions)*10 > len(data) {
				t.Fatalf("stream claims %d regions from a %d byte message", len(s.Regions), len(data))
			}
		}
		if au, err := ParseAVC444(data); err == nil {
			if au.LC > 2 {
				t.Fatalf("LC = %d", au.LC)
			}
			if au.LC == 0 && len(au.Streams) != 2 {
				t.Fatalf("LC 0 produced %d streams", len(au.Streams))
			}
			total := 0
			for _, s := range au.Streams {
				if len(s.Regions) != len(s.Quant) {
					t.Fatalf("%d regions but %d quant entries", len(s.Regions), len(s.Quant))
				}
				total += len(s.Regions)*10 + len(s.Data)
			}
			// The two metablocks and the bitstreams can never exceed the
			// message that carried them.
			if total+4 > len(data) {
				t.Fatalf("streams claim %d bytes from a %d byte message", total, len(data))
			}
		}
	})
}
