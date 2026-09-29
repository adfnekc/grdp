package rdpgfx

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/adfnekc/grdp/codec"
)

// stubAVCDecoder records every access unit it is handed and returns a
// position dependent BGRA image, so a misplaced region shows up as the wrong
// pixel rather than merely as "something decoded".
type stubAVCDecoder struct {
	supports map[uint16]bool
	calls    []stubAVCCall
	err      error
	short    bool
}

type stubAVCCall struct {
	codecID uint16
	au      *codec.AVCAccessUnit
	width   int
	height  int
}

func (d *stubAVCDecoder) SupportsAVCCodec(codecID uint16) bool { return d.supports[codecID] }

func (d *stubAVCDecoder) DecodeAVC(codecID uint16, au *codec.AVCAccessUnit, width, height int) ([]byte, error) {
	d.calls = append(d.calls, stubAVCCall{codecID: codecID, au: au, width: width, height: height})
	if d.err != nil {
		return nil, d.err
	}
	if d.short {
		return make([]byte, 4), nil
	}
	out := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := (y*width + x) * 4
			out[i+0] = byte(x)
			out[i+1] = byte(y)
			out[i+2] = byte(x + y)
			out[i+3] = 0xFF
		}
	}
	return out, nil
}

// avcPatternPixel is the BGRA pixel the stub returns at (x, y).
func avcPatternPixel(x, y int) []byte { return []byte{byte(x), byte(y), byte(x + y), 0xFF} }

// avcMeta builds an RDPGFX_H264_METABLOCK: the region count, the rectangles,
// then the quantisation pairs.
func avcMeta(regions [][4]uint16, quants [][2]byte) []byte {
	out := u32(uint32(len(regions)))
	for _, r := range regions {
		out = append(out, rect16(r[0], r[1], r[2], r[3])...)
	}
	for _, q := range quants {
		out = append(out, q[0], q[1])
	}
	return out
}

// wireAVC feeds one WireToSurface1 PDU carrying an AVC payload and returns the
// dispatch error, if any.
func wireAVC(t *testing.T, c *GfxClient, surfaceID, codecID, left, top, right, bottom uint16, payload []byte) error {
	t.Helper()
	body := append(append(append([]byte{}, u16(surfaceID)...), u16(codecID)...), 0x20)
	body = append(body, rect16(left, top, right, bottom)...)
	body = append(body, u32(uint32(len(payload)))...)
	body = append(body, payload...)
	return c.handle(headerBuf{cmdID: cmdWireToSurface1}, body)
}

// wantRegions asserts that a parsed stream carries exactly the given regions
// and bitstream.
func wantRegions(t *testing.T, name string, s codec.AVCStream, regions [][4]uint16, data []byte) {
	t.Helper()
	if len(s.Regions) != len(regions) {
		t.Fatalf("%s: got %d regions, want %d", name, len(s.Regions), len(regions))
	}
	for i, r := range regions {
		got := s.Regions[i]
		want := codec.RegionRect{Left: r[0], Top: r[1], Right: r[2], Bottom: r[3]}
		if got != want {
			t.Errorf("%s: region %d = %+v, want %+v", name, i, got, want)
		}
	}
	if !bytes.Equal(s.Data, data) {
		t.Errorf("%s: bitstream = % x, want % x", name, s.Data, data)
	}
}

// TestAVC420RoutesThroughDecoder checks the whole AVC420 path: the framing is
// parsed, the decoder is called with the surface geometry, and only the region
// rectangles land on the surface.
func TestAVC420RoutesThroughDecoder(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 8, 8)
	dec := &stubAVCDecoder{supports: map[uint16]bool{codecAVC420: true}}
	c.SetDecoder(dec)

	regions := [][4]uint16{{1, 2, 3, 4}, {4, 5, 6, 6}}
	// 0xAA = 1010_1010: P set, R clear, QP 0x2A. 0x7F: R set, P clear, QP 0x3F.
	quants := [][2]byte{{0xAA, 0x11}, {0x7F, 0x22}}
	bitstream := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	payload := append(avcMeta(regions, quants), bitstream...)

	if err := wireAVC(t, c, 1, codecAVC420, 0, 0, 8, 8, payload); err != nil {
		t.Fatalf("wire to surface: %v", err)
	}

	if len(dec.calls) != 1 {
		t.Fatalf("decoder called %d times, want 1", len(dec.calls))
	}
	call := dec.calls[0]
	if call.codecID != codecAVC420 {
		t.Errorf("codecID = 0x%04x, want 0x%04x", call.codecID, codecAVC420)
	}
	// Geometry is the surface, not the destination rectangle: an AVC region is
	// relative to the surface, and FreeRDP decodes at the surface size.
	if call.width != 8 || call.height != 8 {
		t.Errorf("geometry = %dx%d, want 8x8", call.width, call.height)
	}
	if call.au.LC != 0 || len(call.au.Streams) != 1 {
		t.Fatalf("LC = %d, %d streams, want 0 and 1", call.au.LC, len(call.au.Streams))
	}
	s := call.au.Streams[0]
	wantRegions(t, "AVC420", s, regions, bitstream)
	if len(s.Quant) != 2 {
		t.Fatalf("got %d quant entries, want 2", len(s.Quant))
	}
	if q := s.Quant[0]; q.QP != 0x2A || q.R != 0 || q.P != 1 || q.Quality != 0x11 {
		t.Errorf("quant[0] = %+v, want {QP:0x2A R:0 P:1 Quality:0x11}", q)
	}
	if q := s.Quant[1]; q.QP != 0x3F || q.R != 1 || q.P != 0 || q.Quality != 0x22 {
		t.Errorf("quant[1] = %+v, want {QP:0x3F R:1 P:0 Quality:0x22}", q)
	}

	// Every pixel inside a region is the decoder's pixel; every pixel outside
	// is untouched, so a region off by one is visible.
	got := c.surfaces[1].Pixels()
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			inRegion := (x >= 1 && x < 3 && y >= 2 && y < 4) || (x >= 4 && x < 6 && y >= 5 && y < 6)
			i := (y*8 + x) * 4
			if inRegion {
				if !bytes.Equal(got[i:i+4], avcPatternPixel(x, y)) {
					t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got[i:i+4], avcPatternPixel(x, y))
				}
			} else if got[i] != 0 || got[i+1] != 0 || got[i+2] != 0 {
				t.Errorf("pixel (%d,%d) outside the regions was written: %v", x, y, got[i:i+4])
			}
		}
	}
}

// TestAVC444RoutesThroughDecoder covers both sub-streams: the luma metablock
// ends where cbAvc420EncodedBitstream1 says it does, and the chroma metablock
// follows, each updating its own regions.
func TestAVC444RoutesThroughDecoder(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 8, 8)
	dec := &stubAVCDecoder{supports: map[uint16]bool{codecAVC444: true}}
	c.SetDecoder(dec)

	mb0 := avcMeta([][4]uint16{{0, 0, 4, 4}}, [][2]byte{{0x10, 0x01}})
	d0 := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0xAA}
	mb1 := avcMeta([][4]uint16{{4, 4, 8, 8}}, [][2]byte{{0x20, 0x02}})
	d1 := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xBB}

	cb := uint32(len(mb0) + len(d0))
	payload := u32(cb)
	payload = append(payload, mb0...)
	payload = append(payload, d0...)
	payload = append(payload, mb1...)
	payload = append(payload, d1...)

	if err := wireAVC(t, c, 1, codecAVC444, 0, 0, 8, 8, payload); err != nil {
		t.Fatalf("wire to surface: %v", err)
	}

	if len(dec.calls) != 1 {
		t.Fatalf("decoder called %d times, want 1", len(dec.calls))
	}
	call := dec.calls[0]
	if call.codecID != codecAVC444 {
		t.Errorf("codecID = 0x%04x, want 0x%04x", call.codecID, codecAVC444)
	}
	if call.au.LC != 0 || len(call.au.Streams) != 2 {
		t.Fatalf("LC = %d, %d streams, want 0 and 2", call.au.LC, len(call.au.Streams))
	}
	wantRegions(t, "luma", call.au.Streams[0], [][4]uint16{{0, 0, 4, 4}}, d0)
	wantRegions(t, "chroma", call.au.Streams[1], [][4]uint16{{4, 4, 8, 8}}, d1)

	got := c.surfaces[1].Pixels()
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			inRegion := (x < 4 && y < 4) || (x >= 4 && y >= 4)
			i := (y*8 + x) * 4
			if inRegion {
				if !bytes.Equal(got[i:i+4], avcPatternPixel(x, y)) {
					t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got[i:i+4], avcPatternPixel(x, y))
				}
			} else if got[i] != 0 || got[i+1] != 0 || got[i+2] != 0 {
				t.Errorf("pixel (%d,%d) outside the regions was written: %v", x, y, got[i:i+4])
			}
		}
	}
}

// An AVC444 message with one sub-stream (LC 1 or 2) must route as a single
// stream, with the bitstream running to the end of the message.
func TestAVC444SingleStreamRoutes(t *testing.T) {
	c, _ := newTestClient(t)
	newSurface(t, c, 1, 4, 4)
	dec := &stubAVCDecoder{supports: map[uint16]bool{codecAVC444v2: true}}
	c.SetDecoder(dec)

	mb := avcMeta([][4]uint16{{1, 1, 3, 3}}, [][2]byte{{0x05, 0x06}})
	data := []byte{0x00, 0x00, 0x01, 0x65, 0x11}
	payload := u32((1 << 30) | 0x1234) // LC 1; cb is unused with one stream
	payload = append(payload, mb...)
	payload = append(payload, data...)

	if err := wireAVC(t, c, 1, codecAVC444v2, 0, 0, 4, 4, payload); err != nil {
		t.Fatalf("wire to surface: %v", err)
	}
	if len(dec.calls) != 1 {
		t.Fatalf("decoder called %d times, want 1", len(dec.calls))
	}
	call := dec.calls[0]
	if call.codecID != codecAVC444v2 || call.au.LC != 1 || len(call.au.Streams) != 1 {
		t.Fatalf("codecID 0x%04x LC %d streams %d", call.codecID, call.au.LC, len(call.au.Streams))
	}
	wantRegions(t, "single", call.au.Streams[0], [][4]uint16{{1, 1, 3, 3}}, data)
}

// A truncated AVC message must be refused before the decoder sees it, and the
// surface must be left alone: a parser that reads nothing turns the rest of the
// batch into nonsense.
func TestAVCTruncatedMessageRefused(t *testing.T) {
	// Two regions declared, one present.
	truncated := u32(2)
	truncated = append(truncated, rect16(0, 0, 1, 1)...)
	truncated = append(truncated, 0x10, 0x01)

	c, _ := newTestClient(t)
	newSurface(t, c, 1, 4, 4)
	dec := &stubAVCDecoder{supports: map[uint16]bool{codecAVC420: true, codecAVC444: true}}
	c.SetDecoder(dec)

	for _, codecID := range []uint16{codecAVC420, codecAVC444} {
		err := wireAVC(t, c, 1, codecID, 0, 0, 4, 4, truncated)
		if err == nil {
			t.Errorf("codec 0x%04x: a truncated message was accepted", codecID)
		}
		if len(dec.calls) != 0 {
			t.Fatalf("codec 0x%04x: decoder was called for a malformed message", codecID)
		}
	}

	for i, v := range c.surfaces[1].Pixels() {
		if v != 0 {
			t.Fatalf("surface byte %d was modified: %d", i, v)
		}
	}
}

// Without a decoder, an AVC update is declined with the same unsupported-codec
// error any other undecodeable codec gets, and nothing is drawn.
func TestAVCWithoutDecoderIsDeclined(t *testing.T) {
	c, _ := newTestClient(t) // no SetDecoder
	newSurface(t, c, 1, 4, 4)

	payload := append(avcMeta([][4]uint16{{0, 0, 2, 2}}, [][2]byte{{0x10, 0x01}}), 1, 2, 3)
	err := wireAVC(t, c, 1, codecAVC420, 0, 0, 4, 4, payload)
	if err == nil || !strings.Contains(err.Error(), "unsupported RDPGFX codec 0x000b") {
		t.Fatalf("got err %v, want an unsupported-codec error for 0x000b", err)
	}
	for i, v := range c.surfaces[1].Pixels() {
		if v != 0 {
			t.Fatalf("surface byte %d was modified: %d", i, v)
		}
	}
}

// A decoder that returns an error, or too few bytes, must fail the update
// rather than half-drawing it.
func TestAVCDecoderFailureIsRefused(t *testing.T) {
	payload := append(avcMeta([][4]uint16{{0, 0, 2, 2}}, [][2]byte{{0x10, 0x01}}), 1, 2, 3)

	for name, dec := range map[string]*stubAVCDecoder{
		"error":   {err: errStubDecode},
		"too few": {short: true},
	} {
		dec.supports = map[uint16]bool{codecAVC420: true}
		c, _ := newTestClient(t)
		newSurface(t, c, 1, 4, 4)
		c.SetDecoder(dec)

		if err := wireAVC(t, c, 1, codecAVC420, 0, 0, 4, 4, payload); err == nil {
			t.Errorf("%s: decoder failure was not propagated", name)
		}
		for i, v := range c.surfaces[1].Pixels() {
			if v != 0 {
				t.Fatalf("%s: surface byte %d was modified: %d", name, i, v)
			}
		}
	}
}

var errStubDecode = &avcStubError{}

type avcStubError struct{}

func (*avcStubError) Error() string { return "stub decoder failure" }

// advertise runs OnOpen on a fresh client with the given decoder and returns
// the capability PDU it sent.
func advertise(t *testing.T, dec AVCDecoder) []byte {
	t.Helper()
	c := NewGfxClient()
	var sent [][]byte
	c.SetSender(func(_ uint32, data []byte) error {
		sent = append(sent, data)
		return nil
	})
	if dec != nil {
		c.SetDecoder(dec)
	}
	c.OnOpen(1)
	if len(sent) != 1 {
		t.Fatalf("got %d PDUs, want 1 capabilities advertise", len(sent))
	}
	return sent[0]
}

type capsetEntry struct{ version, flags uint32 }

func capsetsOf(p []byte) []capsetEntry {
	count := int(binary.LittleEndian.Uint16(p[8:]))
	out := make([]capsetEntry, 0, count)
	off := 10
	for i := 0; i < count; i++ {
		out = append(out, capsetEntry{
			version: binary.LittleEndian.Uint32(p[off:]),
			flags:   binary.LittleEndian.Uint32(p[off+8:]),
		})
		off += capsetBase + 4
	}
	return out
}

// With no decoder, nothing about H.264 may be offered: the AVC420 flag must be
// clear and no 10.0 set may appear, or a server would send codecs the client
// cannot decode.
func TestAVCNothingAdvertisedWithoutDecoder(t *testing.T) {
	sets := capsetsOf(advertise(t, nil))
	if len(sets) != 2 {
		t.Fatalf("got %d capsets, want 2", len(sets))
	}
	for _, s := range sets {
		if s.flags&capsFlagAVC420Enabled != 0 {
			t.Errorf("capset 0x%08x offers AVC420 with no decoder", s.version)
		}
		if s.version == CapsVersion10 {
			t.Errorf("a 10.0 capset was offered with no AVC444 decoder")
		}
	}
}

// A decoder that supports AVC420 turns on the 8.1 AVC420 flag and nothing else.
func TestAVC420AdvertisedWithDecoder(t *testing.T) {
	dec := &stubAVCDecoder{supports: map[uint16]bool{codecAVC420: true}}
	sets := capsetsOf(advertise(t, dec))
	if len(sets) != 2 {
		t.Fatalf("got %d capsets, want 2", len(sets))
	}
	if sets[0].version != CapsVersion8 || sets[0].flags&capsFlagAVC420Enabled != 0 {
		t.Errorf("8.0 capset = %+v, want no AVC420", sets[0])
	}
	if sets[1].version != CapsVersion81 || sets[1].flags&capsFlagAVC420Enabled == 0 {
		t.Errorf("8.1 capset = %+v, want the AVC420 flag", sets[1])
	}
}

// A decoder that can decode AVC444 also needs a 10.0 capability set, which is
// the only version whose flags control AVC444.
func TestAVC444AdvertisedWithDecoder(t *testing.T) {
	for _, codecID := range []uint16{codecAVC444, codecAVC444v2} {
		dec := &stubAVCDecoder{supports: map[uint16]bool{codecID: true}}
		sets := capsetsOf(advertise(t, dec))
		if len(sets) != 3 {
			t.Fatalf("codec 0x%04x: got %d capsets, want 3", codecID, len(sets))
		}
		last := sets[len(sets)-1]
		if last.version != CapsVersion10 {
			t.Errorf("codec 0x%04x: last capset version = 0x%08x, want 0x%08x", codecID, last.version, CapsVersion10)
		}
		if last.flags&capsFlagThinClient == 0 {
			t.Errorf("codec 0x%04x: 10.0 capset lost THINCLIENT, so the server may use RemoteFX Progressive", codecID)
		}
	}
}
