package pdu

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildSurfaceBits appends a CMDTYPE_SET_SURFACE_BITS command to b.
func buildSurfaceBits(b *bytes.Buffer, left, top, right, bottom byte, bmp *bytes.Buffer) {
	binary.Write(b, binary.LittleEndian, uint16(CMDTYPE_SET_SURFACE_BITS))
	binary.Write(b, binary.LittleEndian, uint16(left))
	binary.Write(b, binary.LittleEndian, uint16(top))
	binary.Write(b, binary.LittleEndian, uint16(right))
	binary.Write(b, binary.LittleEndian, uint16(bottom))
	b.Write(bmp.Bytes())
}

func buildBitmapDataEx(bpp, flags, codecID byte, width, height uint16, data []byte, withExHeader bool) *bytes.Buffer {
	b := &bytes.Buffer{}
	b.WriteByte(bpp)
	b.WriteByte(flags)
	b.WriteByte(0) // reserved
	b.WriteByte(codecID)
	binary.Write(b, binary.LittleEndian, width)
	binary.Write(b, binary.LittleEndian, height)
	binary.Write(b, binary.LittleEndian, uint32(len(data)))
	if withExHeader {
		binary.Write(b, binary.LittleEndian, uint32(0x11223344))
		binary.Write(b, binary.LittleEndian, uint32(0x55667788))
		binary.Write(b, binary.LittleEndian, uint64(1234))
		binary.Write(b, binary.LittleEndian, uint64(5678))
	}
	b.Write(data)
	return b
}

func TestSurfaceCommandsPDUUnpack(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	buf := &bytes.Buffer{}
	// A frame marker brackets the update.
	binary.Write(buf, binary.LittleEndian, uint16(CMDTYPE_FRAME_MARKER))
	binary.Write(buf, binary.LittleEndian, uint16(SURFACECMD_FRAMEACTION_BEGIN))
	binary.Write(buf, binary.LittleEndian, uint32(0x2A))
	buildSurfaceBits(buf, 0, 0, 16, 16, buildBitmapDataEx(32, 0, 1, 16, 16, payload, false))
	binary.Write(buf, binary.LittleEndian, uint16(CMDTYPE_FRAME_MARKER))
	binary.Write(buf, binary.LittleEndian, uint16(SURFACECMD_FRAMEACTION_END))
	binary.Write(buf, binary.LittleEndian, uint32(0x2A))

	var p SurfaceCommandsPDU
	if err := p.Unpack(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(p.Commands) != 3 {
		t.Fatalf("got %d commands, want 3", len(p.Commands))
	}

	begin := p.Commands[0].Marker
	if begin == nil || begin.FrameAction != SURFACECMD_FRAMEACTION_BEGIN || begin.FrameID != 0x2A {
		t.Fatalf("bad begin marker: %+v", begin)
	}

	bits := p.Commands[1].Bits
	if bits == nil {
		t.Fatal("expected a surface bits command")
	}
	if bits.CmdType != CMDTYPE_SET_SURFACE_BITS {
		t.Fatalf("got cmdType 0x%04x", bits.CmdType)
	}
	if bits.DestLeft != 0 || bits.DestTop != 0 || bits.DestRight != 16 || bits.DestBottom != 16 {
		t.Fatalf("bad destination rectangle: %+v", bits)
	}
	if bits.Bitmap.Bpp != 32 || bits.Bitmap.CodecID != 1 || bits.Bitmap.Width != 16 || bits.Bitmap.Height != 16 {
		t.Fatalf("bad bitmap header: %+v", bits.Bitmap)
	}
	if !bytes.Equal(bits.Bitmap.BitmapData, payload) {
		t.Fatalf("got payload %x, want %x", bits.Bitmap.BitmapData, payload)
	}

	end := p.Commands[2].Marker
	if end == nil || end.FrameAction != SURFACECMD_FRAMEACTION_END || end.FrameID != 0x2A {
		t.Fatalf("bad end marker: %+v", end)
	}
}

func TestSurfaceCommandsPDUExHeader(t *testing.T) {
	buf := &bytes.Buffer{}
	buildSurfaceBits(buf, 4, 8, 20, 24,
		buildBitmapDataEx(24, EX_COMPRESSED_BITMAP_HEADER_PRESENT, 3, 16, 16, []byte{1, 2, 3}, true))

	var p SurfaceCommandsPDU
	if err := p.Unpack(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(p.Commands) != 1 {
		t.Fatalf("got %d commands, want 1", len(p.Commands))
	}
	bmp := p.Commands[0].Bits.Bitmap
	if bmp.Header == nil {
		t.Fatal("expected the extended bitmap header to be parsed")
	}
	if bmp.Header.HighUniqueID != 0x11223344 || bmp.Header.LowUniqueID != 0x55667788 {
		t.Fatalf("bad unique ids: %+v", bmp.Header)
	}
	if bmp.Header.TmMilliseconds != 1234 || bmp.Header.TmSeconds != 5678 {
		t.Fatalf("bad timestamps: %+v", bmp.Header)
	}
	if !bytes.Equal(bmp.BitmapData, []byte{1, 2, 3}) {
		t.Fatalf("got payload %x", bmp.BitmapData)
	}
}

func TestSurfaceCommandsPDUUnknownCommand(t *testing.T) {
	buf := &bytes.Buffer{}
	binary.Write(buf, binary.LittleEndian, uint16(0x00FF))

	var p SurfaceCommandsPDU
	if err := p.Unpack(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected an error for an unknown command type")
	}
}

func TestSurfaceCommandsPDUTruncated(t *testing.T) {
	buf := &bytes.Buffer{}
	binary.Write(buf, binary.LittleEndian, uint16(CMDTYPE_SET_SURFACE_BITS))
	binary.Write(buf, binary.LittleEndian, uint16(0)) // only part of the rectangle

	var p SurfaceCommandsPDU
	if err := p.Unpack(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected an error for a truncated command")
	}
}
