package codec

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// The vectors in testdata are produced by scripts/gen-codec-vectors.c, which
// drives the NSCodec and RemoteFX codecs from libfreerdp. For each payload it
// also stores the pixels produced by FreeRDP's own decoder, so our decoder can
// be compared against the reference implementation rather than against the
// original image: "does my decoder agree" and "how lossy is this codec" are
// very different questions and only the first one is about our code.
//
// Regenerate with scripts/gen-codec-vectors.sh.

func loadVector(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Skipf("missing test vector %s; run scripts/gen-codec-vectors.sh", name)
	}
	return b
}

// pixelError reports the largest absolute per channel difference and the mean
// absolute difference over the RGB channels.
func pixelError(t *testing.T, got, want []byte, w, h int) (maxDiff, meanDiff int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("decoded %d bytes, want %d", len(got), len(want))
	}
	if len(got) == 0 {
		return 0, 0
	}
	stride := len(got) / (w * h)
	var sum int64
	n := 0
	for i := 0; i+3 < len(got); i += stride {
		for c := 0; c < 3; c++ {
			d := int(got[i+c]) - int(want[i+c])
			if d < 0 {
				d = -d
			}
			if d > maxDiff {
				maxDiff = d
			}
			sum += int64(d)
			n++
		}
	}
	if n > 0 {
		meanDiff = int(sum / int64(n))
	}
	return maxDiff, meanDiff
}

// checkNSCodecVector decodes a vector and checks it against both the reference
// decoder output and the original image.
func checkNSCodecVector(t *testing.T, name string, w, h, maxVsOriginal int) {
	t.Helper()
	encoded := loadVector(t, name)

	got, err := NSCodec(encoded, w, h)
	if err != nil {
		t.Fatalf("NSCodec(%s): %v", name, err)
	}
	if len(got) != w*h*4 {
		t.Fatalf("%s: decoded %d bytes, want %d", name, len(got), w*h*4)
	}

	// The reference decoder output must match exactly: any difference means
	// our RLE or YCoCg reconstruction diverges from FreeRDP.
	ref := loadVector(t, "nsc_dec_"+name[len("nsc_"):])
	if !bytes.Equal(got, ref) {
		maxDiff, meanDiff := pixelError(t, got, ref, w, h)
		t.Fatalf("%s: output differs from the reference decoder (max %d, mean %d)",
			name, maxDiff, meanDiff)
	}
	t.Logf("%s: identical to the reference decoder", name)

	// And the result must be close to what was encoded.
	orig := loadVector(t, "raw_64x64.bin")
	maxDiff, meanDiff := pixelError(t, got, orig, w, h)
	t.Logf("%s: max channel error vs original %d, mean %d", name, maxDiff, meanDiff)
	if maxDiff > maxVsOriginal {
		t.Fatalf("%s: max channel error %d exceeds %d", name, maxDiff, maxVsOriginal)
	}
}

func TestNSCodecAgainstReferenceDecoder(t *testing.T) {
	checkNSCodecVector(t, "nsc_64x64_loss1_sub0.bin", 64, 64, 8)
}

func TestNSCodecColorLoss3AgainstReferenceDecoder(t *testing.T) {
	checkNSCodecVector(t, "nsc_64x64_loss3_sub0.bin", 64, 64, 64)
}

func TestNSCodecSubsamplingAgainstReferenceDecoder(t *testing.T) {
	// Chroma subsampling halves the resolution of the colour planes, so the
	// test image's hard edge legitimately produces large per channel errors.
	// The meaningful check is the exact match against the reference decoder
	// inside checkNSCodecVector; see the log line for the actual spread.
	checkNSCodecVector(t, "nsc_64x64_loss1_sub1.bin", 64, 64, 160)
}

// TestNSCodecVectorHeader guards the assumption that the vectors really are
// NSCodec payloads with the plane layout the decoder expects.
func TestNSCodecVectorHeader(t *testing.T) {
	data := loadVector(t, "nsc_64x64_loss1_sub0.bin")
	if len(data) < 20 {
		t.Fatalf("vector too short: %d bytes", len(data))
	}
	total := uint32(0)
	for i := 0; i < 4; i++ {
		total += binary.LittleEndian.Uint32(data[i*4:])
	}
	if int(total) != len(data)-20 {
		t.Fatalf("plane counts sum to %d, payload is %d bytes", total, len(data)-20)
	}
	if data[16] != 1 || data[17] != 0 {
		t.Fatalf("unexpected colour loss %d / chroma %d", data[16], data[17])
	}
}
