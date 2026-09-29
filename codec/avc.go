package codec

import (
	"encoding/binary"
	"fmt"
)

// AVC420 and AVC444 are the H.264 surface codecs of the graphics pipeline
// extension. This file parses their messages but does not decode them: the
// framing is fixed, while the H.264 bitstream itself is left to a decoder the
// caller supplies. The layouts follow FreeRDP's
// channels/rdpgfx/client/rdpgfx_codec.c (rdpgfx_read_h264_metablock,
// rdpgfx_decode_AVC420 and rdpgfx_decode_AVC444) and the structures in
// include/freerdp/channels/rdpgfx.h, not the prose of MS-RDPEGFX.

// RegionRect is a rectangle in surface coordinates, as carried by the AVC420
// and AVC444 region lists. It is the same 16 bit RDPGFX_RECT16 the other
// surface commands use, so a region is relative to the surface, not to the
// update's destination rectangle.
type RegionRect struct {
	Left, Top, Right, Bottom uint16
}

// AVCQuantQuality is the per-region quantisation an AVC420 stream carries. The
// wire field qpVal packs the quantisation parameter into its low six bits and
// two flags above it; qualityVal is a separate byte.
type AVCQuantQuality struct {
	// QP is the quantisation parameter: bits 0 to 5 of qpVal.
	QP uint8
	// R is bit 6 and P is bit 7 of qpVal.
	R, P uint8
	// Quality is the separate qualityVal byte.
	Quality uint8
}

// AVCStream is one H.264 sub-stream: the region list it updates, the
// quantisation for each region, and the bitstream itself.
type AVCStream struct {
	// Regions is the region rectangle list, one entry per coded region.
	Regions []RegionRect
	// Quant carries one entry per region, parallel to Regions.
	Quant []AVCQuantQuality
	// Data is the H.264 bitstream exactly as the server sent it. It is an
	// opaque Annex B stream, with no per-NAL length framing: FreeRDP hands
	// these bytes straight to its H.264 decoder without scanning them, so the
	// start codes are the decoder's business, not this parser's. The length is
	// bounded by the message that carried it.
	Data []byte
}

// AVCAccessUnit is one parsed AVC420 or AVC444 surface update.
//
// AVC420 carries a single stream. AVC444 carries either two streams (LC == 0:
// luma first, then a chroma layer, the chroma being the lower resolution 4:2:0
// sub-layer) or one (LC == 1: luma only, LC == 2: chroma only). Streams is in
// wire order, so for LC == 2 Streams[0] is the chroma layer.
type AVCAccessUnit struct {
	// LC is the AVC444 Luma/Chroma selector: the top two bits of
	// cbAvc420EncodedBitstream1. It is 0 for AVC420, where there is only one
	// stream and no selector.
	LC uint8
	// Streams holds the sub-streams in wire order.
	Streams []AVCStream
}

// parseAVCMetablock reads the RDPGFX_H264_METABLOCK at the start of data: a
// uint32 region count, that many 8 byte rectangles, then that many 2 byte
// quantisation entries. It returns the stream and how many bytes it consumed,
// or an error if a field cannot be read.
//
// The region count is bounded against the bytes actually present before it is
// used as a length: each region costs 8 bytes of rectangle plus 2 bytes of
// quantisation, so a count larger than the remaining bytes divided by ten is
// necessarily truncated. Without that bound a four byte message claiming four
// billion regions would allocate for all of them.
func parseAVCMetablock(data []byte) (AVCStream, int, error) {
	var s AVCStream
	if len(data) < 4 {
		return s, 0, fmt.Errorf("AVC metablock: %w", errTruncated)
	}
	n := binary.LittleEndian.Uint32(data)
	if uint64(n) > uint64(len(data)-4)/10 {
		return s, 0, fmt.Errorf("AVC metablock: %d regions need %d bytes, have %d: %w",
			n, 4+uint64(n)*10, len(data), errTruncated)
	}

	at := 4
	s.Regions = make([]RegionRect, n)
	for i := range s.Regions {
		s.Regions[i] = RegionRect{
			Left:   binary.LittleEndian.Uint16(data[at:]),
			Top:    binary.LittleEndian.Uint16(data[at+2:]),
			Right:  binary.LittleEndian.Uint16(data[at+4:]),
			Bottom: binary.LittleEndian.Uint16(data[at+6:]),
		}
		at += 8
	}
	s.Quant = make([]AVCQuantQuality, n)
	for i := range s.Quant {
		qp := data[at]
		s.Quant[i] = AVCQuantQuality{
			QP:      qp & 0x3F,
			R:       (qp >> 6) & 1,
			P:       (qp >> 7) & 1,
			Quality: data[at+1],
		}
		at += 2
	}
	return s, at, nil
}

// ParseAVC420 parses the bitmap data of a WireToSurface command carrying
// RDPGFX_CODECID_AVC420 (MS-RDPEGFX 2.2.4.4). The result carries one stream:
// the region list, the per-region quantisation and the bitstream, which runs to
// the end of the message.
func ParseAVC420(data []byte) (*AVCAccessUnit, error) {
	s, at, err := parseAVCMetablock(data)
	if err != nil {
		return nil, fmt.Errorf("AVC420: %w", err)
	}
	s.Data = data[at:]
	return &AVCAccessUnit{Streams: []AVCStream{s}}, nil
}

// ParseAVC444 parses the bitmap data of a WireToSurface command carrying
// RDPGFX_CODECID_AVC444 (0x0E) or AVC444v2 (0x0F), whose framing is identical
// (MS-RDPEGFX 2.2.4.5).
//
// The message starts with a uint32 whose top two bits are the LC selector and
// whose low 30 bits, cbAvc420EncodedBitstream1, size the first sub-stream. A
// second sub-stream follows only when LC is 0.
func ParseAVC444(data []byte) (*AVCAccessUnit, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("AVC444: %w", errTruncated)
	}
	header := binary.LittleEndian.Uint32(data)
	lc := uint8((header >> 30) & 0x03)
	cb := header & 0x3FFFFFFF
	if lc == 0x03 {
		return nil, fmt.Errorf("AVC444: reserved LC selector %d", lc)
	}

	// The first sub-stream always follows the four byte header. at is relative
	// to data[4:], so the absolute position is at+4.
	s0, at, err := parseAVCMetablock(data[4:])
	if err != nil {
		return nil, fmt.Errorf("AVC444: stream 1: %w", err)
	}
	at += 4

	au := &AVCAccessUnit{LC: lc}

	if lc != 0 {
		// Only one sub-stream is present and it runs to the end of the
		// message.
		s0.Data = data[at:]
		au.Streams = []AVCStream{s0}
		return au, nil
	}

	// Both sub-streams are present. cbAvc420EncodedBitstream1 is the size of
	// the first sub-stream without its own four byte header, so subtracting
	// the metablock leaves the bitstream length. FreeRDP computes the same
	// value as cb - pos2 + pos1.
	metaLen := at - 4
	if uint64(cb) < uint64(metaLen) {
		return nil, fmt.Errorf("AVC444: cbAvc420EncodedBitstream1 %d is smaller than its metablock %d: %w",
			cb, metaLen, errTruncated)
	}
	streamLen := int(cb) - metaLen
	if uint64(at)+uint64(streamLen) > uint64(len(data)) {
		return nil, fmt.Errorf("AVC444: stream 1 declares %d bytes, have %d: %w",
			streamLen, len(data)-at, errTruncated)
	}
	s0.Data = data[at : at+streamLen]
	au.Streams = []AVCStream{s0}

	// The second sub-stream, the chroma layer, follows the first.
	s1, at1, err := parseAVCMetablock(data[at+streamLen:])
	if err != nil {
		return nil, fmt.Errorf("AVC444: stream 2: %w", err)
	}
	s1.Data = data[at+streamLen+at1:]
	au.Streams = append(au.Streams, s1)
	return au, nil
}
