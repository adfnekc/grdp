// Package codec implements the bitmap codecs that RDP servers use to encode
// bitmap update and surface command payloads.
//
// A compressed bitmap update whose flags say the payload is codec-encoded
// begins with a one byte codec id (MS-RDPBCGR 2.2.9.1.1.3.1.2.2). Codec id
// zero means the payload is plain RLE; the other ids select an implementation
// in this package.
package codec

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrUnsupported reports a codec id this package cannot decode yet.
var ErrUnsupported = errors.New("codec: unsupported bitmap codec")

// errTruncated and errOverflow describe malformed compressed input. They are
// kept separate from ErrUnsupported so a caller can tell "cannot decode this
// codec" from "this payload is corrupt".
var (
	errTruncated = errors.New("codec: truncated input")
	errOverflow  = errors.New("codec: output overflow")
)

// roundUp rounds n up to the next multiple of m.
func roundUp(n, m int) int {
	if m <= 0 {
		return n
	}
	return (n + m - 1) / m * m
}

// clamp8 saturates a signed component to an 8 bit colour value.
func clamp8(v int16) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 0xFF:
		return 0xFF
	default:
		return uint8(v)
	}
}

// NSCodec decodes a bitmap compressed with NSCodec (MS-RDPNSC).
//
// The encoded form is a TS_NSC_DATA header followed by four run-length encoded
// planes: Y, Co, Cg and A. Colour is reconstructed with the YCoCg transform.
// The result is BGRA, 4 bytes per pixel, rows top to bottom, which is the
// layout the bitmap update path uses for 32bpp data.
func NSCodec(data []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("codec: nscodec: invalid size %dx%d", width, height)
	}
	// TS_NSC_DATA: 4 plane byte counts, colour loss level, chroma
	// subsampling level and 2 reserved bytes.
	if len(data) < 20 {
		return nil, fmt.Errorf("codec: nscodec: header: %w", errTruncated)
	}
	var planeByteCount [4]uint32
	var total uint32
	for i := 0; i < 4; i++ {
		planeByteCount[i] = binary.LittleEndian.Uint32(data[i*4:])
		total += planeByteCount[i]
	}
	colorLossLevel := data[16]
	chromaSubsampling := data[17]

	if colorLossLevel < 1 || colorLossLevel > 7 {
		return nil, fmt.Errorf("codec: nscodec: colour loss level %d out of range [1,7]", colorLossLevel)
	}
	planes := data[20:]
	if uint32(len(planes)) < total {
		return nil, fmt.Errorf("codec: nscodec: plane data: want %d bytes, have %d: %w",
			total, len(planes), errTruncated)
	}

	// The luma plane is padded to a multiple of 8 columns; with chroma
	// subsampling the chroma planes are also padded to a multiple of 2 rows
	// and stored at half resolution.
	tempWidth := roundUp(width, 8)
	tempHeight := roundUp(height, 2)
	orgByteCount := [4]int{width * height, width * height, width * height, width * height}
	if chromaSubsampling != 0 {
		orgByteCount[0] = tempWidth * height
		orgByteCount[1] = (tempWidth >> 1) * (tempHeight >> 1)
		orgByteCount[2] = orgByteCount[1]
	}

	planeCap := tempWidth * tempHeight
	if planeCap < width*height {
		planeCap = width * height
	}

	var decoded [4][]byte
	in := planes
	for i := 0; i < 4; i++ {
		planeLen := int(planeByteCount[i])
		if planeLen > len(in) {
			return nil, fmt.Errorf("codec: nscodec: plane %d: want %d bytes, have %d: %w",
				i, planeLen, len(in), errTruncated)
		}
		out := make([]byte, planeCap)
		switch {
		case planeLen == 0:
			// An absent plane is a constant. The alpha plane is fully
			// opaque, the colour planes are all ones.
			for j := 0; j < orgByteCount[i]; j++ {
				out[j] = 0xFF
			}
		case planeLen < orgByteCount[i]:
			if err := decodeNSPlaneRLE(in[:planeLen], out[:orgByteCount[i]]); err != nil {
				return nil, fmt.Errorf("codec: nscodec: plane %d: %w", i, err)
			}
		default:
			// Stored uncompressed.
			copy(out[:orgByteCount[i]], in[:orgByteCount[i]])
		}
		decoded[i] = out
		in = in[planeLen:]
	}

	return nscToBGRA(decoded, width, height, colorLossLevel, chromaSubsampling), nil
}

// decodeNSPlaneRLE expands one NSCodec plane. The scheme is the run-length
// encoding shared with interleaved RLE (MS-RDPNSC 2.2.2.1): a literal is one
// byte, and a run starts when a byte is immediately repeated, followed by
// either a one byte count biased by two or, when that count byte is 0xFF, a
// four byte little endian count. The final four bytes are always literal.
func decodeNSPlaneRLE(in, out []byte) error {
	left := len(out)
	ip, op := 0, 0

	for left > 4 {
		if ip >= len(in) {
			return errTruncated
		}
		v := in[ip]
		ip++

		if left == 5 {
			// Only one literal and the trailing four bytes remain.
			out[op] = v
			op++
			left--
			continue
		}
		if ip >= len(in) {
			return errTruncated
		}
		if v != in[ip] {
			out[op] = v
			op++
			left--
			continue
		}

		ip++ // consume the repeated byte
		var run int
		if ip >= len(in) {
			return errTruncated
		}
		if in[ip] < 0xFF {
			run = int(in[ip]) + 2
			ip++
		} else {
			if ip+5 > len(in) {
				return errTruncated
			}
			ip++ // consume the 0xFF marker
			run = int(binary.LittleEndian.Uint32(in[ip:]))
			ip += 4
		}
		if run > left || run > len(out)-op {
			return errOverflow
		}
		for i := 0; i < run; i++ {
			out[op+i] = v
		}
		op += run
		left -= run
	}

	if left != 4 {
		return errOverflow
	}
	if ip+4 > len(in) {
		return errTruncated
	}
	copy(out[op:op+4], in[ip:ip+4])
	return nil
}

// nscToBGRA converts the decoded Y, Co, Cg and A planes to BGRA pixels.
func nscToBGRA(planes [4][]byte, width, height int, colorLossLevel, chromaSubsampling uint8) []byte {
	// Colour loss recovery: the chroma planes were scaled down by the
	// encoder, so shift them back up and truncate to a signed byte.
	shift := uint(colorLossLevel - 1)

	rowWidth := width
	if chromaSubsampling != 0 {
		rowWidth = roundUp(width, 8) >> 1
	}

	out := make([]byte, 4*width*height)
	pos := 0
	for y := 0; y < height; y++ {
		var yRow, coRow, cgRow []byte
		if chromaSubsampling != 0 {
			yRow = planes[0][y*roundUp(width, 8):]
			coRow = planes[1][(y>>1)*rowWidth:]
			cgRow = planes[2][(y>>1)*rowWidth:]
		} else {
			yRow = planes[0][y*width:]
			coRow = planes[1][y*width:]
			cgRow = planes[2][y*width:]
		}
		aRow := planes[3][y*width:]

		for x := 0; x < width; x++ {
			// With chroma subsampling the chroma samples are shared by
			// pairs of horizontally adjacent pixels.
			cx := x
			if chromaSubsampling != 0 {
				cx = x >> 1
			}

			yv := int16(yRow[x])
			co := int16(int8(uint8(int16(coRow[cx]) << shift)))
			cg := int16(int8(uint8(int16(cgRow[cx]) << shift)))

			out[pos+0] = clamp8(yv - co - cg) // B
			out[pos+1] = clamp8(yv + cg)      // G
			out[pos+2] = clamp8(yv + co - cg) // R
			out[pos+3] = aRow[x]
			pos += 4
		}
	}
	return out
}
