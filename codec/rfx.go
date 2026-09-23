package codec

import (
	"encoding/binary"
	"fmt"
)

// RFX (RemoteFX) block types (MS-RDPRFX, and rfx_constants.h in FreeRDP).
const (
	rfxSync          = 0xCCC0
	rfxCodecVersions = 0xCCC1
	rfxChannels      = 0xCCC2
	rfxContext       = 0xCCC3
	rfxFrameBegin    = 0xCCC4
	rfxFrameEnd      = 0xCCC5
	rfxRegion        = 0xCCC6
	rfxExtension     = 0xCCC7

	cbtRegion  = 0xCAC1
	cbtTileset = 0xCAC2
	cbtTile    = 0xCAC3
)

// rfxTileSize is the fixed RemoteFX tile edge, in pixels.
const rfxTileSize = 64

// rfxCoefficients is the number of DWT coefficients per tile component.
const rfxCoefficients = rfxTileSize * rfxTileSize

// RLGRMode selects the entropy coder used inside a tileset.
type RLGRMode int

const (
	// RLGR1 is the simpler, Golomb-Rice-only mode.
	RLGR1 RLGRMode = iota
	// RLGR3 additionally splits each coefficient into two values.
	RLGR3
)

// RLGR constants (MS-RDPRFX 3.1.8).
const (
	rlgrKPMAX = 80 // maximum kp / krp
	rlgrLSGR  = 3  // shift converting kp to k
	rlgrUPGR  = 4  // kp increase after a zero run in run-length mode
	rlgrDNGR  = 6  // kp decrease after a nonzero symbol in run-length mode
	rlgrUQGR  = 3  // kp increase for a nonzero symbol in Golomb-Rice mode
	rlgrDQGR  = 3  // kp decrease for a zero symbol in Golomb-Rice mode
)

// rfxRect is a region rectangle, part of TS_RFX_REGION.
type rfxRect struct {
	x, y, width, height int
}

// rfxTile holds one decoded tile's location and its three entropy coded
// components.
type rfxTile struct {
	quantIdxY, quantIdxCb, quantIdxCr uint8
	xIdx, yIdx                        int
	yData, cbData, crData             []byte
}

// rfxData is everything a message contributes: the region to update, the
// quantisation tables and the tiles.
type rfxData struct {
	rects  []rfxRect
	quants []uint32 // numQuant * 10 nibble values
	tiles  []rfxTile
}

// DecodeRFX decodes a RemoteFX message (MS-RDPRFX) and returns the pixels as
// BGRA, top-down, matching the other decoders in this package.
//
// width and height describe the destination surface; tiles are clipped to it
// and to the rectangles the message declares.
func DecodeRFX(data []byte, width, height int, mode RLGRMode) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("codec: rfx: invalid size %dx%d", width, height)
	}
	r, err := parseRFX(data)
	if err != nil {
		return nil, err
	}
	if len(r.quants) == 0 {
		return nil, fmt.Errorf("codec: rfx: no quantisation table")
	}

	out := make([]byte, width*height*4)
	tileBuf := make([]int16, rfxCoefficients)
	tileRGBA := make([]byte, rfxTileSize*rfxTileSize*4)

	for _, t := range r.tiles {
		yq := quantAt(r.quants, int(t.quantIdxY))
		cbq := quantAt(r.quants, int(t.quantIdxCb))
		crq := quantAt(r.quants, int(t.quantIdxCr))
		if yq == nil || cbq == nil || crq == nil {
			return nil, fmt.Errorf("codec: rfx: quantisation index out of range")
		}

		yb := make([]int16, rfxCoefficients)
		cbb := make([]int16, rfxCoefficients)
		crb := make([]int16, rfxCoefficients)
		decodeComponent(t.yData, yq, yb, mode)
		decodeComponent(t.cbData, cbq, cbb, mode)
		decodeComponent(t.crData, crq, crb, mode)

		yCbCrToBGRA(yb, cbb, crb, tileRGBA)

		tileX, tileY := t.xIdx*rfxTileSize, t.yIdx*rfxTileSize
		blitTile(out, width, height, tileX, tileY, tileRGBA, r.rects)
		_ = tileBuf
	}

	return out, nil
}

// quantAt returns the ten quantisation nibbles for one table entry.
func quantAt(quants []uint32, idx int) []uint32 {
	if idx < 0 || (idx+1)*10 > len(quants) {
		return nil
	}
	return quants[idx*10 : (idx+1)*10]
}

// parseRFX walks the block list of an RFX message.
func parseRFX(data []byte) (*rfxData, error) {
	r := &rfxData{}
	off := 0

	for off+6 <= len(data) {
		blockType := int(binary.LittleEndian.Uint16(data[off:]))
		blockLen := int(binary.LittleEndian.Uint32(data[off+2:]))
		if blockLen < 6 || off+blockLen > len(data) {
			return nil, fmt.Errorf("codec: rfx: block 0x%04x has bad length %d at offset %d",
				blockType, blockLen, off)
		}
		payload := data[off+6 : off+blockLen]
		// Block types from WBT_CONTEXT through WBT_EXTENSION carry an extra
		// RFX_CODEC_CHANNELT header: codecId must be 0x01, channelId is 0xFF
		// for WBT_CONTEXT and 0x00 otherwise. Everything below expects the
		// sub-stream without it.
		if blockType >= rfxContext && blockType <= rfxExtension {
			if len(payload) < 2 {
				return nil, fmt.Errorf("codec: rfx: block 0x%04x: %w", blockType, errTruncated)
			}
			if payload[0] != 0x01 {
				return nil, fmt.Errorf("codec: rfx: block 0x%04x: bad codecId 0x%02x", blockType, payload[0])
			}
			payload = payload[2:]
		}

		switch blockType {
		case rfxRegion:
			rects, err := parseRFXRegion(payload)
			if err != nil {
				return nil, err
			}
			r.rects = rects
		case rfxExtension:
			if err := parseRFXTileset(payload, r); err != nil {
				return nil, err
			}
		case rfxSync, rfxCodecVersions, rfxChannels, rfxContext, rfxFrameBegin, rfxFrameEnd:
			// Informational; the decoder does not need them.
		default:
			return nil, fmt.Errorf("codec: rfx: unknown block type 0x%04x", blockType)
		}

		off += blockLen
	}

	if off != len(data) {
		return nil, fmt.Errorf("codec: rfx: %d trailing bytes", len(data)-off)
	}
	return r, nil
}

// parseRFXRegion reads TS_RFX_REGION: flags, a rectangle list and the tileset
// marker that follows it.
func parseRFXRegion(p []byte) ([]rfxRect, error) {
	if len(p) < 3 {
		return nil, fmt.Errorf("codec: rfx: region: %w", errTruncated)
	}
	numRects := int(binary.LittleEndian.Uint16(p[1:]))
	if numRects == 0 {
		// A zero count means "the whole surface"; the caller clips anyway.
		return nil, nil
	}
	if 3+numRects*8 > len(p) {
		return nil, fmt.Errorf("codec: rfx: region: %d rects need more data: %w", numRects, errTruncated)
	}
	rects := make([]rfxRect, 0, numRects)
	for i := 0; i < numRects; i++ {
		b := p[3+i*8:]
		rects = append(rects, rfxRect{
			x:      int(binary.LittleEndian.Uint16(b[0:])),
			y:      int(binary.LittleEndian.Uint16(b[2:])),
			width:  int(binary.LittleEndian.Uint16(b[4:])),
			height: int(binary.LittleEndian.Uint16(b[6:])),
		})
	}
	return rects, nil
}

// parseRFXTileset reads the CBT_TILESET carried inside a WBT_EXTENSION block:
// the quantisation tables followed by the tiles.
func parseRFXTileset(p []byte, r *rfxData) error {
	if len(p) < 14 {
		return fmt.Errorf("codec: rfx: tileset: %w", errTruncated)
	}
	subtype := binary.LittleEndian.Uint16(p)
	if subtype != cbtTileset {
		return fmt.Errorf("codec: rfx: tileset: bad subtype 0x%04x", subtype)
	}
	// Header: subtype(2) idx(2) properties(2) numQuant(1) tileSize(1)
	// numTiles(2) tilesDataSize(4).
	numQuant := int(p[6])
	if numQuant < 1 {
		return fmt.Errorf("codec: rfx: tileset: no quantisation values")
	}
	numTiles := int(binary.LittleEndian.Uint16(p[8:]))
	if numTiles < 1 {
		return nil
	}

	off := 14
	if off+numQuant*5 > len(p) {
		return fmt.Errorf("codec: rfx: tileset: quantisation table: %w", errTruncated)
	}
	for i := 0; i < numQuant; i++ {
		for j := 0; j < 5; j++ {
			b := p[off+i*5+j]
			r.quants = append(r.quants, uint32(b&0x0F), uint32(b>>4))
		}
	}
	off += numQuant * 5

	for i := 0; i < numTiles; i++ {
		if off+6 > len(p) {
			return fmt.Errorf("codec: rfx: tile %d: %w", i, errTruncated)
		}
		blockType := binary.LittleEndian.Uint16(p[off:])
		blockLen := int(binary.LittleEndian.Uint32(p[off+2:]))
		if blockType != cbtTile {
			return fmt.Errorf("codec: rfx: tile %d: bad block type 0x%04x", i, blockType)
		}
		if blockLen < 19 || off+blockLen > len(p) {
			return fmt.Errorf("codec: rfx: tile %d: bad length %d: %w", i, blockLen, errTruncated)
		}
		t := p[off+6 : off+blockLen]
		if len(t) < 13 {
			return fmt.Errorf("codec: rfx: tile %d: %w", i, errTruncated)
		}
		tile := rfxTile{
			quantIdxY:  t[0],
			quantIdxCb: t[1],
			quantIdxCr: t[2],
			xIdx:       int(binary.LittleEndian.Uint16(t[3:])),
			yIdx:       int(binary.LittleEndian.Uint16(t[5:])),
		}
		yLen := int(binary.LittleEndian.Uint16(t[7:]))
		cbLen := int(binary.LittleEndian.Uint16(t[9:]))
		crLen := int(binary.LittleEndian.Uint16(t[11:]))
		need := 13 + yLen + cbLen + crLen
		if need > len(t) {
			return fmt.Errorf("codec: rfx: tile %d: components need %d bytes, have %d: %w",
				i, need, len(t), errTruncated)
		}
		tile.yData = t[13 : 13+yLen]
		tile.cbData = t[13+yLen : 13+yLen+cbLen]
		tile.crData = t[13+yLen+cbLen : need]
		r.tiles = append(r.tiles, tile)

		off += blockLen
	}
	return nil
}

// decodeComponent runs the full per-component pipeline: entropy decoding, the
// differential reconstruction of the DC band, dequantisation and the inverse
// wavelet transform.
func decodeComponent(data []byte, quants []uint32, buf []int16, mode RLGRMode) {
	rlgrDecode(mode, data, buf)

	// The LL3 band holds differentially coded DC coefficients.
	ll3 := buf[4032:4096]
	for i := 1; i < len(ll3); i++ {
		ll3[i] += ll3[i-1]
	}

	dequantise(buf, quants)
	dwt2DDecode(buf)
}

// dequantise scales each subband back up. The band order is fixed by the
// wavelet layout.
func dequantise(buf []int16, q []uint32) {
	bands := []struct {
		off, size int
		factor    uint32
	}{
		{0, 1024, q[8]},    // HL1
		{1024, 1024, q[7]}, // LH1
		{2048, 1024, q[9]}, // HH1
		{3072, 256, q[5]},  // HL2
		{3328, 256, q[4]},  // LH2
		{3584, 256, q[6]},  // HH2
		{3840, 64, q[2]},   // HL3
		{3904, 64, q[1]},   // LH3
		{3968, 64, q[3]},   // HH3
		{4032, 64, q[0]},   // LL3
	}
	for _, b := range bands {
		if b.factor == 0 {
			continue
		}
		shift := b.factor - 1
		if shift > 15 {
			shift = 15
		}
		for i := 0; i < b.size; i++ {
			buf[b.off+i] <<= shift
		}
	}
}

// dwt2DDecode performs the three level inverse 5/3 wavelet transform in place.
// Each level reconstructs a subband group into the start of that group.
func dwt2DDecode(buf []int16) {
	dwt2DDecodeBlock(buf[3840:], 8)
	dwt2DDecodeBlock(buf[3072:], 16)
	dwt2DDecodeBlock(buf[0:], 32)
}

// dwt2DDecodeBlock reconstructs one level. The four subbands are stored as
// HL, LH, HH, LL; subbandWidth is the width of each of them.
func dwt2DDecodeBlock(buf []int16, subbandWidth int) {
	totalWidth := subbandWidth << 1
	idwt := make([]int16, 4*subbandWidth*subbandWidth)

	ll := buf[subbandWidth*subbandWidth*3:]
	hl := buf
	lh := buf[subbandWidth*subbandWidth:]
	hh := buf[subbandWidth*subbandWidth*2:]

	// Horizontal pass: interleave the low and high bands into idwt, with the
	// low band first and the high band after it.
	lDst := idwt
	hDst := idwt[subbandWidth*totalWidth:]

	for y := 0; y < subbandWidth; y++ {
		lRow := ll[y*subbandWidth:]
		hRow := hl[y*subbandWidth:]
		lhRow := lh[y*subbandWidth:]
		hhRow := hh[y*subbandWidth:]
		lOut := lDst[y*totalWidth:]
		hOut := hDst[y*totalWidth:]

		lOut[0] = lRow[0] - ((hRow[0] + hRow[0] + 1) >> 1)
		hOut[0] = lhRow[0] - ((hhRow[0] + hhRow[0] + 1) >> 1)

		for n := 1; n < subbandWidth; n++ {
			x := n << 1
			lOut[x] = lRow[n] - ((hRow[n-1] + hRow[n] + 1) >> 1)
			hOut[x] = lhRow[n] - ((hhRow[n-1] + hhRow[n] + 1) >> 1)
		}
		for n := 0; n < subbandWidth-1; n++ {
			x := n << 1
			lOut[x+1] = (hRow[n] << 1) + ((lOut[x] + lOut[x+2]) >> 1)
			hOut[x+1] = (hhRow[n] << 1) + ((hOut[x] + hOut[x+2]) >> 1)
		}
		n := subbandWidth - 1
		x := n << 1
		lOut[x+1] = (hRow[n] << 1) + lOut[x]
		hOut[x+1] = (hhRow[n] << 1) + hOut[x]
	}

	// Vertical pass: the result lands back in buf, overwriting the level.
	for x := 0; x < totalWidth; x++ {
		l := x
		h := x + subbandWidth*totalWidth
		dst := x

		buf[dst] = idwt[l] - ((idwt[h]*2 + 1) >> 1)

		for n := 1; n < subbandWidth; n++ {
			l += totalWidth
			h += totalWidth

			buf[dst+2*totalWidth] = idwt[l] - ((idwt[h-totalWidth] + idwt[h] + 1) >> 1)
			buf[dst+totalWidth] = (idwt[h-totalWidth] << 1) + ((buf[dst] + buf[dst+2*totalWidth]) >> 1)
			dst += 2 * totalWidth
		}
		buf[dst+totalWidth] = (idwt[h] << 1) + buf[dst]
	}
}

// yCbCrToBGRA converts the three reconstructed components of a tile into BGRA
// pixels using the irreversible colour transform from MS-RDPRFX 3.1.9. The
// fixed point constants and the two stage shift match FreeRDP's BGRX
// primitive, which is what a real client uses for this pixel format.
func yCbCrToBGRA(y, cb, cr []int16, dst []byte) {
	const divisor = 16
	// FreeRDP computes these as float(1.402525f * 65536) and truncates, so the
	// single precision rounding is part of the expected output. The multiply is
	// forced to runtime because Go rejects truncating a constant conversion.
	scale := func(f float32) int64 { return int64(f * float32(1<<divisor)) }
	crR := scale(1.402525)
	crG := scale(0.714401)
	cbG := scale(0.343730)
	cbB := scale(1.769905)

	for i := 0; i < rfxCoefficients; i++ {
		yv := int32((uint32(y[i] + 4096)) << divisor)
		cbv := int64(cb[i])
		crv := int64(cr[i])

		r := int16((crv*crR + int64(yv)) >> divisor)
		g := int16((int64(yv) - cbv*cbG - crv*crG) >> divisor)
		b := int16((cbv*cbB + int64(yv)) >> divisor)

		dst[i*4+0] = clamp8(b >> 5)
		dst[i*4+1] = clamp8(g >> 5)
		dst[i*4+2] = clamp8(r >> 5)
		dst[i*4+3] = 0xFF
	}
}

// blitTile copies one decoded tile into the destination, clipped to the
// surface and, when the message declared a region, to its rectangles.
func blitTile(dst []byte, width, height, tileX, tileY int, tile []byte, rects []rfxRect) {
	for y := 0; y < rfxTileSize; y++ {
		dy := tileY + y
		if dy < 0 || dy >= height {
			continue
		}
		for x := 0; x < rfxTileSize; x++ {
			dx := tileX + x
			if dx < 0 || dx >= width {
				continue
			}
			if !inRects(dx, dy, rects) {
				continue
			}
			si := (y*rfxTileSize + x) * 4
			di := (dy*width + dx) * 4
			copy(dst[di:di+4], tile[si:si+4])
		}
	}
}

// inRects reports whether a pixel falls in any of the region rectangles. An
// empty rectangle list means the whole surface.
func inRects(x, y int, rects []rfxRect) bool {
	if len(rects) == 0 {
		return true
	}
	for _, r := range rects {
		if x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height {
			return true
		}
	}
	return false
}
