package codec

import (
	"encoding/binary"
	"fmt"
)

// ZGFX is the bulk compression that wraps EGFX graphics data (MS-RDPEGFX
// 3.1.8). The graphics channel does not carry raw RDPGFX PDUs: every message is
// a ZGFX stream, whether or not it is actually compressed.
//
// The format is LZ77 with a fixed token table. A stream is one or more
// segments; each segment is decoded independently but they share one history
// buffer, which also persists across messages for the life of the channel. A
// decoder therefore has to be kept for as long as the connection lasts, and it
// has to be fed every message in order.
type ZGFX struct {
	history      []byte
	historyIndex int
}

const (
	// zgfxSegmentSingle and zgfxSegmentMultipart are the stream descriptors.
	zgfxSegmentSingle    = 0xE0
	zgfxSegmentMultipart = 0xE1
	// zgfxPacketCompressed is the flag, in a segment's first byte, that says
	// the segment body is token encoded rather than stored.
	zgfxPacketCompressed = 0x20
	// zgfxHistorySize is the history buffer size, and therefore the largest
	// match distance.
	zgfxHistorySize = 2500000
	// zgfxMaxSegment is the most any one segment may decode to.
	zgfxMaxSegment = 65536
)

// NewZGFX returns a decoder ready for a new channel. The history is the only
// state, and it is large, so keep one per connection rather than per message.
func NewZGFX() *ZGFX {
	return &ZGFX{history: make([]byte, zgfxHistorySize)}
}

// zgfxToken is one entry of the fixed token table. A token is recognised by its
// prefix, read most significant bit first, and is either a literal (tokenType 0,
// the byte is valueBase plus the following valueBits bits) or a match
// (tokenType 1, the distance is valueBase plus the following valueBits bits).
type zgfxToken struct {
	prefixLength uint32
	prefixCode   uint32
	valueBits    uint32
	tokenType    uint32
	valueBase    uint32
}

// zgfxTokens is MS-RDPEGFX 3.1.8.1.2's token table, in the order the reference
// implementation scans it: by increasing prefix length, so the running prefix
// only ever grows.
var zgfxTokens = [...]zgfxToken{
	{1, 0, 8, 0, 0},
	{5, 17, 5, 1, 0},
	{5, 18, 7, 1, 32},
	{5, 19, 9, 1, 160},
	{5, 20, 10, 1, 672},
	{5, 21, 12, 1, 1696},
	{5, 24, 0, 0, 0x00},
	{5, 25, 0, 0, 0x01},
	{6, 44, 14, 1, 5792},
	{6, 45, 15, 1, 22176},
	{6, 52, 0, 0, 0x02},
	{6, 53, 0, 0, 0x03},
	{6, 54, 0, 0, 0xFF},
	{7, 92, 18, 1, 54944},
	{7, 93, 20, 1, 317088},
	{7, 110, 0, 0, 0x04},
	{7, 111, 0, 0, 0x05},
	{7, 112, 0, 0, 0x06},
	{7, 113, 0, 0, 0x07},
	{7, 114, 0, 0, 0x08},
	{7, 115, 0, 0, 0x09},
	{7, 116, 0, 0, 0x0A},
	{7, 117, 0, 0, 0x0B},
	{7, 118, 0, 0, 0x3A},
	{7, 119, 0, 0, 0x3B},
	{7, 120, 0, 0, 0x3C},
	{7, 121, 0, 0, 0x3D},
	{7, 122, 0, 0, 0x3E},
	{7, 123, 0, 0, 0x3F},
	{7, 124, 0, 0, 0x40},
	{7, 125, 0, 0, 0x80},
	{8, 188, 20, 1, 1365664},
	{8, 189, 21, 1, 2414240},
	{8, 252, 0, 0, 0x0C},
	{8, 253, 0, 0, 0x38},
	{8, 254, 0, 0, 0x39},
	{8, 255, 0, 0, 0x66},
	{9, 380, 22, 1, 4511392},
	{9, 381, 23, 1, 8705696},
	{9, 382, 24, 1, 17094304},
}

// Decompress decodes one ZGFX stream. The receiver must be the same one used
// for every previous stream on the channel, because matches may reach back into
// the history of earlier messages.
func (z *ZGFX) Decompress(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("zgfx: empty stream")
	}

	switch src[0] {
	case zgfxSegmentSingle:
		return z.segment(src[1:])

	case zgfxSegmentMultipart:
		if len(src) < 7 {
			return nil, fmt.Errorf("zgfx: multipart header needs 7 bytes, got %d", len(src))
		}
		segmentCount := int(binary.LittleEndian.Uint16(src[1:3]))
		uncompressedSize := int(binary.LittleEndian.Uint32(src[3:7]))

		rest := src[7:]
		// The header's size is only checked once every segment has decoded. It
		// must not be trusted for the allocation: a hostile stream can claim a
		// gigabyte and have nothing behind it.
		hint := uncompressedSize
		if hint > zgfxMaxSegment {
			hint = zgfxMaxSegment
		}
		out := make([]byte, 0, hint)
		for i := 0; i < segmentCount; i++ {
			if len(rest) < 4 {
				return nil, fmt.Errorf("zgfx: multipart segment %d has no size", i)
			}
			size := int(binary.LittleEndian.Uint32(rest[:4]))
			rest = rest[4:]
			if size > len(rest) {
				return nil, fmt.Errorf("zgfx: multipart segment %d wants %d bytes, %d left", i, size, len(rest))
			}
			seg, err := z.segment(rest[:size])
			if err != nil {
				return nil, err
			}
			rest = rest[size:]
			out = append(out, seg...)
		}

		// The header's size is authoritative: a mismatch means we and the
		// server disagree about the stream, which is worth reporting rather
		// than passing on to the graphics parser.
		if len(out) != uncompressedSize {
			return nil, fmt.Errorf("zgfx: multipart decoded %d bytes, header says %d", len(out), uncompressedSize)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("zgfx: unknown stream descriptor 0x%02x", src[0])
	}
}

// segment decodes one segment: a flags byte followed by the body.
func (z *ZGFX) segment(seg []byte) ([]byte, error) {
	if len(seg) < 2 {
		return nil, fmt.Errorf("zgfx: segment needs 2 bytes, got %d", len(seg))
	}

	flags := seg[0]
	body := seg[1:]

	if flags&zgfxPacketCompressed == 0 {
		// Stored. The body is the output verbatim, but it still counts
		// towards the history for later matches.
		if len(body) > zgfxMaxSegment {
			return nil, fmt.Errorf("zgfx: stored segment is %d bytes", len(body))
		}
		out := make([]byte, len(body))
		copy(out, body)
		z.historyWrite(body)
		return out, nil
	}

	// In a compressed segment the last byte is not data: it gives the number
	// of padding bits at the end of the one before it, which is how the encoder
	// rounds the stream up to a whole number of bytes.
	data := body[:len(body)-1]
	available := 8 * uint32(len(data))
	padding := uint32(body[len(body)-1])
	if padding > available {
		return nil, fmt.Errorf("zgfx: padding %d exceeds the %d bits available", padding, available)
	}

	b := zgfxBits{data: data, remaining: available - padding}
	out := make([]byte, 0, zgfxMaxSegment)

	for b.remaining > 0 && !b.overrun {
		// Scan the table for the token whose prefix matches the next bits.
		var prefix, have uint32
		matched := false
		for i := range zgfxTokens {
			tok := &zgfxTokens[i]
			for have < tok.prefixLength {
				prefix = prefix<<1 | b.bits(1)
				have++
			}
			if prefix != tok.prefixCode {
				continue
			}
			matched = true

			if tok.tokenType == 0 {
				c := byte(tok.valueBase + b.bits(tok.valueBits))
				if len(out) >= zgfxMaxSegment {
					return nil, fmt.Errorf("zgfx: segment output exceeds %d bytes", zgfxMaxSegment)
				}
				out = append(out, c)
				z.historyPush(c)
				break
			}

			distance := tok.valueBase + b.bits(tok.valueBits)
			if distance == 0 {
				// An unencoded run: the bits are zeroed out to the next byte
				// boundary and the bytes are copied straight from the stream.
				count := b.bits(15)
				b.align()
				if count > uint32(zgfxMaxSegment-len(out)) {
					return nil, fmt.Errorf("zgfx: unencoded run of %d does not fit the segment", count)
				}
				if count > b.remaining/8 {
					return nil, fmt.Errorf("zgfx: unencoded run of %d exceeds the %d bits left", count, b.remaining)
				}
				if count > uint32(len(b.data)-b.pos) {
					return nil, fmt.Errorf("zgfx: unencoded run of %d exceeds the %d bytes left", count, len(b.data)-b.pos)
				}
				run := b.data[b.pos : b.pos+int(count)]
				out = append(out, run...)
				z.historyWrite(run)
				b.pos += int(count)
				b.remaining -= 8 * count
				break
			}

			// A match. The length is coded in stages: a single bit picks 3 or
			// the generic form, which doubles along a unary prefix and then
			// adds the bits that follow.
			var count uint32
			if b.bits(1) == 0 {
				count = 3
			} else {
				count = 4
				extra := uint32(2)
				for b.bits(1) == 1 {
					count *= 2
					extra++
				}
				count += b.bits(extra)
			}
			if count > uint32(zgfxMaxSegment-len(out)) {
				return nil, fmt.Errorf("zgfx: match of %d does not fit the segment", count)
			}

			start := len(out)
			out = append(out, make([]byte, count)...)
			if err := z.match(int(distance), out[start:]); err != nil {
				return nil, err
			}
			z.historyWrite(out[start:])
			break
		}

		if !matched {
			return nil, fmt.Errorf("zgfx: no token matches prefix %09b after %d bytes", prefix, len(out))
		}
	}

	if b.overrun {
		return nil, fmt.Errorf("zgfx: segment ended mid-token, decoded %d bytes", len(out))
	}

	return out, nil
}

// match fills dst from history at the given distance. A match may overlap the
// bytes it is producing, so after the first distance bytes the source is the
// output itself, repeated.
func (z *ZGFX) match(distance int, dst []byte) error {
	if distance <= 0 || distance > len(z.history) {
		return fmt.Errorf("zgfx: match distance %d is outside the history", distance)
	}

	count := len(dst)
	index := ((z.historyIndex-distance)%len(z.history) + len(z.history)) % len(z.history)

	// The opening run comes from the history proper, up to one distance's
	// worth, wrapping at the end of the ring.
	first := count
	if first > distance {
		first = distance
	}
	n := copy(dst[:first], z.history[index:])
	if n < first {
		copy(dst[n:first], z.history[:first-n])
	}

	// Everything after that repeats what has just been produced. Copying the
	// run that already exists, doubling each time, reproduces the period.
	for produced := first; produced < count; {
		n := produced
		if n > count-produced {
			n = count - produced
		}
		copy(dst[produced:produced+n], dst[:n])
		produced += n
	}
	return nil
}

func (z *ZGFX) historyPush(b byte) {
	z.history[z.historyIndex] = b
	if z.historyIndex++; z.historyIndex == len(z.history) {
		z.historyIndex = 0
	}
}

// historyWrite appends to the ring. A write larger than the ring keeps only the
// newest bytes, and moves the index past the part that was dropped, so that a
// later match sees the same window the encoder did.
func (z *ZGFX) historyWrite(src []byte) {
	size := len(z.history)
	if len(src) == 0 {
		return
	}
	if len(src) > size {
		residue := len(src) - size
		src = src[residue:]
		z.historyIndex = (z.historyIndex + residue) % size
	}
	if z.historyIndex+len(src) <= size {
		copy(z.history[z.historyIndex:], src)
		if z.historyIndex += len(src); z.historyIndex == size {
			z.historyIndex = 0
		}
		return
	}
	front := size - z.historyIndex
	copy(z.history[z.historyIndex:], src[:front])
	copy(z.history, src[front:])
	z.historyIndex = len(src) - front
}

// zgfxBits reads the segment's bits, most significant bit first, as MS-RDPEGFX
// specifies. It stops at the end of the data rather than reading the padding
// byte, and tracks the bit budget separately so the loop can stop when the
// encoder's padding begins.
//
// At most 31 bits are ever held, so the accumulator cannot overflow.
type zgfxBits struct {
	data      []byte
	pos       int
	cur       uint32
	nCur      uint32
	remaining uint32
	overrun   bool
}

func (b *zgfxBits) bits(n uint32) uint32 {
	// Nothing in the table asks for more than 24 bits: that is the widest value
	// field, and the length and run fields are narrower still. A corrupt stream
	// can ask for far more, which would spin pulling in padding to satisfy a
	// count no encoder ever wrote, so treat it as the end of the stream.
	if n > 24 {
		b.overrun = true
		b.remaining = 0
		return 0
	}

	for b.nCur < n {
		b.cur <<= 8
		if b.pos < len(b.data) {
			b.cur += uint32(b.data[b.pos])
			b.pos++
		}
		b.nCur += 8
	}

	// A truncated stream asks for more bits than the encoder wrote. Stop
	// rather than letting the budget wrap, which would keep the decoder
	// looping over rubbish until the loop counter ran out.
	if n > b.remaining {
		b.overrun = true
		b.remaining = 0
	} else {
		b.remaining -= n
	}

	b.nCur -= n
	v := b.cur >> b.nCur
	b.cur &= 1<<b.nCur - 1
	return v
}

// align drops the partially consumed byte. Only the bits of the current byte can
// be left over, never a whole unread byte, so the byte position stays correct.
func (b *zgfxBits) align() {
	b.remaining -= b.nCur
	b.nCur = 0
	b.cur = 0
}
