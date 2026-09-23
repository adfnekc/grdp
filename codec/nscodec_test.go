package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestDecodeNSPlaneRLE(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{
			// Adjacent distinct bytes are literals, so a plane with no
			// repeats is stored verbatim.
			name: "all literal",
			in:   []byte{1, 2, 3, 4, 5, 6, 7, 8},
			want: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		},
		{
			// A run is a byte repeated immediately, then a count biased by
			// two. The trailing four bytes are always verbatim, so the run
			// only covers the first four pixels here.
			name: "short run plus trailing literals",
			in:   []byte{9, 9, 2, 9, 9, 9, 9},
			want: []byte{9, 9, 9, 9, 9, 9, 9, 9},
		},
		{
			// A count byte of 0xFF means a four byte little endian run
			// length follows.
			name: "long run",
			in:   append([]byte{7, 7, 0xFF, 0x2E, 0x01, 0x00, 0x00}, 8, 8, 8, 8),
			want: append(bytes.Repeat([]byte{7}, 302), 8, 8, 8, 8),
		},
		{
			// left == 5 is the one position where a literal is forced so
			// that four bytes remain for the verbatim tail.
			name: "forced literal at five remaining",
			in:   []byte{1, 2, 3, 4, 5, 6, 7, 8},
			want: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := make([]byte, len(tt.want))
			if err := decodeNSPlaneRLE(tt.in, out); err != nil {
				t.Fatalf("decodeNSPlaneRLE: %v", err)
			}
			if !bytes.Equal(out, tt.want) {
				t.Fatalf("got  %v\nwant %v", out, tt.want)
			}
		})
	}
}

func TestDecodeNSPlaneRLEErrors(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		size int
		want error
	}{
		{name: "empty input", in: nil, size: 8, want: errTruncated},
		{name: "run past end of output", in: []byte{1, 1, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, size: 8, want: errOverflow},
		{name: "missing trailing bytes", in: []byte{1, 2, 3, 4}, size: 8, want: errTruncated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := make([]byte, tt.size)
			err := decodeNSPlaneRLE(tt.in, out)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got err %v, want %v", err, tt.want)
			}
		})
	}
}

// nscHeader builds a TS_NSC_DATA header for a test vector.
func nscHeader(counts [4]uint32, colorLoss, chroma byte) []byte {
	h := make([]byte, 20)
	for i, c := range counts {
		binary.LittleEndian.PutUint32(h[i*4:], c)
	}
	h[16] = colorLoss
	h[17] = chroma
	return h
}

func TestNSCodecSinglePixel(t *testing.T) {
	// Y/Co/Cg/A all fit in one byte each, so the planes are stored
	// uncompressed (plane length == original length).
	data := append(nscHeader([4]uint32{1, 1, 1, 1}, 1, 0), 100, 10, 5, 255)

	got, err := NSCodec(data, 1, 1)
	if err != nil {
		t.Fatalf("NSCodec: %v", err)
	}
	// B = y - co - cg, G = y + cg, R = y + co - cg.
	want := []byte{85, 105, 105, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNSCodecZeroPlanes(t *testing.T) {
	// A plane length of zero means a constant plane: opaque alpha and
	// all-ones colour planes.
	data := nscHeader([4]uint32{0, 0, 0, 0}, 1, 0)

	got, err := NSCodec(data, 1, 1)
	if err != nil {
		t.Fatalf("NSCodec: %v", err)
	}
	// co and cg become 0xFF, which is -1 once sign extended.
	want := []byte{255, 254, 255, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNSCodecColorLoss(t *testing.T) {
	// Colour loss level 3 shifts the chroma planes up by two bits before
	// the signed byte truncation.
	data := append(nscHeader([4]uint32{1, 1, 1, 1}, 3, 0), 100, 4, 2, 255)

	got, err := NSCodec(data, 1, 1)
	if err != nil {
		t.Fatalf("NSCodec: %v", err)
	}
	// co = 4<<2 = 16, cg = 2<<2 = 8.
	want := []byte{76, 108, 108, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNSCodecColorLossNegative(t *testing.T) {
	// 0x40 << 2 = 0x100, which truncates to a signed byte of zero. This is
	// why the shift happens before the int8 conversion.
	data := append(nscHeader([4]uint32{1, 1, 1, 1}, 3, 0), 100, 0x40, 0, 255)

	got, err := NSCodec(data, 1, 1)
	if err != nil {
		t.Fatalf("NSCodec: %v", err)
	}
	want := []byte{100, 100, 100, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNSCodecChromaSubsampling(t *testing.T) {
	// 2x2 with chroma subsampling: the luma plane is padded to eight
	// columns, the chroma planes hold one sample per two pixels.
	yPlane := []byte{10, 20, 0, 0, 0, 0, 0, 0, 30, 40, 0, 0, 0, 0, 0, 0}
	coPlane := []byte{1, 2, 0, 0}
	cgPlane := []byte{3, 4, 0, 0}
	aPlane := []byte{255, 255, 255, 255}

	data := nscHeader([4]uint32{16, 4, 4, 4}, 1, 1)
	data = append(data, yPlane...)
	data = append(data, coPlane...)
	data = append(data, cgPlane...)
	data = append(data, aPlane...)

	got, err := NSCodec(data, 2, 2)
	if err != nil {
		t.Fatalf("NSCodec: %v", err)
	}
	want := []byte{
		6, 13, 8, 255, // (0,0): y=10 co=1 cg=3
		16, 23, 18, 255, // (1,0): y=20, same chroma sample
		26, 33, 28, 255, // (0,1): y=30, chroma row 0
		36, 43, 38, 255, // (1,1): y=40
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestNSCodecRunLengthPlane(t *testing.T) {
	// A run-length encoded plane must be smaller than the original to take
	// the RLE path, and must be decoded to exactly the original size. The
	// last four bytes are always verbatim, so the run covers only the first
	// four of the eight pixels.
	yPlane := []byte{9, 9, 2, 9, 9, 9, 9} // run of 4, then four verbatim
	data := nscHeader([4]uint32{uint32(len(yPlane)), 8, 8, 8}, 1, 0)
	data = append(data, yPlane...)
	data = append(data, make([]byte, 8)...)              // Co = 0
	data = append(data, make([]byte, 8)...)              // Cg = 0
	data = append(data, bytes.Repeat([]byte{255}, 8)...) // A = opaque

	got, err := NSCodec(data, 8, 1)
	if err != nil {
		t.Fatalf("NSCodec: %v", err)
	}
	if len(got) != 8*4 {
		t.Fatalf("got %d bytes, want %d", len(got), 8*4)
	}
	for x := 0; x < 8; x++ {
		want := []byte{9, 9, 9, 255}
		if !bytes.Equal(got[x*4:x*4+4], want) {
			t.Fatalf("pixel %d: got %v, want %v", x, got[x*4:x*4+4], want)
		}
	}
}

func TestNSCodecErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		w, h int
		want error
	}{
		{name: "short header", data: make([]byte, 19), w: 1, h: 1, want: errTruncated},
		{
			name: "bad colour loss level",
			data: nscHeader([4]uint32{0, 0, 0, 0}, 0, 0),
			w:    1, h: 1,
			want: nil, // checked separately below
		},
		{
			name: "plane data shorter than declared",
			data: nscHeader([4]uint32{4, 0, 0, 0}, 1, 0),
			w:    1, h: 1,
			want: errTruncated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := NSCodec(tt.data, tt.w, tt.h)
			if tt.name == "bad colour loss level" {
				if err == nil {
					t.Fatalf("expected an error, got output %v", out)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("got err %v, want %v", err, tt.want)
			}
		})
	}

	if _, err := NSCodec(nil, 0, 0); err == nil {
		t.Fatal("expected an error for a zero sized bitmap")
	}
}
