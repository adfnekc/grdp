package codec

import "math/bits"

// RLGR (Run-Length Golomb-Rice) entropy decoding, MS-RDPRFX 3.1.8.
//
// The decoder walks a bit stream MSB first. It alternates between a
// run-length mode, used while runs of zero coefficients are being emitted, and
// a Golomb-Rice mode for individual coefficients. Two adaptive parameters
// control the code word sizes: kp governs the run length, krp the magnitude.
// RLGR3 additionally splits each Golomb-Rice value into two coefficients.

// bitReader reads bits MSB first from a byte slice. Reading past the end
// yields zero bits, matching how the reference decoder's 32 bit accumulator is
// zero padded beyond the buffer.
type bitReader struct {
	data []byte
	pos  int // bits consumed
}

// total returns the total number of bits available.
func (b *bitReader) total() int { return len(b.data) * 8 }

// remaining returns how many bits have not been consumed yet.
func (b *bitReader) remaining() int {
	if r := b.total() - b.pos; r > 0 {
		return r
	}
	return 0
}

// peek returns the next 32 bits, MSB aligned.
func (b *bitReader) peek() uint32 {
	byteIdx := b.pos >> 3
	bitOff := uint(b.pos & 7)

	var w uint64
	// Five bytes, because the 32 bit window may start mid-byte. w holds bits
	// [byteIdx*8, byteIdx*8+40) with data[byteIdx] at the top, so the 32 bits
	// starting at bitOff are w shifted down by 8-bitOff.
	for i := 0; i < 5; i++ {
		var v byte
		if byteIdx+i < len(b.data) {
			v = b.data[byteIdx+i]
		}
		w = w<<8 | uint64(v)
	}
	return uint32(w >> (8 - bitOff))
}

// shift consumes n bits.
func (b *bitReader) shift(n int) {
	if n <= 0 {
		return
	}
	b.pos += n
	if b.pos > b.total() {
		b.pos = b.total()
	}
}

// leadingCount returns the number of leading bits of the requested polarity in
// the current window, clamped to the bits actually left.
func (b *bitReader) leadingCount(zeros bool) int {
	var n int
	if zeros {
		n = bits.LeadingZeros32(b.peek())
	} else {
		n = bits.LeadingZeros32(^b.peek())
	}
	if rem := b.remaining(); n > rem {
		n = rem
	}
	return n
}

// countLeading consumes a unary code: full groups of 32 bits are skipped while
// they are all of the requested polarity, then the remainder is shifted off.
// It returns the total number of leading bits seen. The reference decoder
// accumulates across groups, which matters for runs longer than 32 bits.
func (b *bitReader) countLeading(zeros bool) int {
	cnt := b.leadingCount(zeros)
	vk := cnt
	for cnt == 32 && b.remaining() > 0 {
		b.shift(32)
		cnt = b.leadingCount(zeros)
		vk += cnt
	}
	b.shift(vk % 32)
	return vk
}

// readBits consumes n bits and returns them as an unsigned value.
func (b *bitReader) readBits(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if n > 32 {
		n = 32
	}
	v := b.peek() >> (32 - uint(n))
	b.shift(n)
	return v
}

// rlgrDecode expands one tile component into exactly len(dst) coefficients.
// If the stream runs out early the rest are left as zero, which is what the
// reference decoder does.
func rlgrDecode(mode RLGRMode, data []byte, dst []int16) {
	if mode != RLGR1 && mode != RLGR3 {
		mode = RLGR1
	}

	br := &bitReader{data: data}

	k := 1
	kp := k << rlgrLSGR
	kr := 1
	krp := kr << rlgrLSGR

	out := 0

	for br.remaining() > 0 && out < len(dst) {
		if k != 0 {
			// Run-length mode: a run of zeros followed by one coefficient.
			run := 0

			vk := br.countLeading(true)
			if br.remaining() < 1 {
				break
			}
			br.shift(1) // the terminating one bit

			for ; vk > 0; vk-- {
				run += 1 << uint(k)
				kp += rlgrUPGR
				if kp > rlgrKPMAX {
					kp = rlgrKPMAX
				}
				k = kp >> rlgrLSGR
			}

			if br.remaining() < k {
				break
			}
			run += int(br.readBits(k))

			if br.remaining() < 1 {
				break
			}
			sign := br.readBits(1)

			vk = br.countLeading(false)
			if br.remaining() < 1 {
				break
			}
			br.shift(1)

			if br.remaining() < kr {
				break
			}
			code := br.readBits(kr) | uint32(vk)<<uint(kr)

			switch {
			case vk == 0:
				krp -= 2
				if krp < 0 {
					krp = 0
				}
				kr = krp >> rlgrLSGR
			case vk != 1:
				krp += vk
				if krp > rlgrKPMAX {
					krp = rlgrKPMAX
				}
				kr = krp >> rlgrLSGR
			}

			kp -= rlgrDNGR
			if kp < 0 {
				kp = 0
			}
			k = kp >> rlgrLSGR

			runEnd := out + run
			if runEnd > len(dst) {
				runEnd = len(dst)
			}
			for ; out < runEnd; out++ {
				dst[out] = 0
			}
			if out < len(dst) {
				if sign != 0 {
					dst[out] = -int16(code + 1)
				} else {
					dst[out] = int16(code + 1)
				}
				out++
			}
			continue
		}

		// Golomb-Rice mode.
		vk := br.countLeading(false)
		if br.remaining() < 1 {
			break
		}
		br.shift(1)

		if br.remaining() < kr {
			break
		}
		code := br.readBits(kr) | uint32(vk)<<uint(kr)

		switch {
		case vk == 0:
			krp -= 2
			if krp < 0 {
				krp = 0
			}
			kr = krp >> rlgrLSGR
		case vk != 1:
			krp += vk
			if krp > rlgrKPMAX {
				krp = rlgrKPMAX
			}
			kr = krp >> rlgrLSGR
		}

		if mode == RLGR1 {
			if code == 0 {
				kp += rlgrUQGR
				if kp > rlgrKPMAX {
					kp = rlgrKPMAX
				}
				k = kp >> rlgrLSGR
				dst[out] = 0
			} else {
				kp -= rlgrDQGR
				if kp < 0 {
					kp = 0
				}
				k = kp >> rlgrLSGR
				dst[out] = decodeSignMagnitude(code)
			}
			out++
			continue
		}

		// RLGR3: the value is split into two coefficients.
		nIdx := 0
		if code != 0 {
			nIdx = 32 - bits.LeadingZeros32(code)
		}
		if br.remaining() < nIdx {
			break
		}
		val1 := br.readBits(nIdx)
		val2 := code - val1

		switch {
		case val1 != 0 && val2 != 0:
			kp -= 2 * rlgrDQGR
			if kp < 0 {
				kp = 0
			}
			k = kp >> rlgrLSGR
		case val1 == 0 && val2 == 0:
			kp += 2 * rlgrUQGR
			if kp > rlgrKPMAX {
				kp = rlgrKPMAX
			}
			k = kp >> rlgrLSGR
		}

		if out < len(dst) {
			dst[out] = decodeSignMagnitude(val1)
			out++
		}
		if out < len(dst) {
			dst[out] = decodeSignMagnitude(val2)
			out++
		}
	}
}

// decodeSignMagnitude maps the packed representation code = 2*mag - sign onto
// a signed coefficient.
func decodeSignMagnitude(code uint32) int16 {
	if code&1 != 0 {
		return -int16((code + 1) >> 1)
	}
	return int16(code >> 1)
}
