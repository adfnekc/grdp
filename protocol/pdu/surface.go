package pdu

import (
	"bytes"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
)

// Surface command types (MS-RDPBCGR 2.2.9.2.1.1). Surface commands are only
// ever sent over the fast path, as FASTPATH_UPDATETYPE_SURFCMDS.
const (
	CMDTYPE_SET_SURFACE_BITS    = 0x0001
	CMDTYPE_FRAME_MARKER        = 0x0004
	CMDTYPE_STREAM_SURFACE_BITS = 0x0006
)

// TS_BITMAP_DATA_EX flags.
const EX_COMPRESSED_BITMAP_HEADER_PRESENT = 0x01

// Frame marker actions.
const (
	SURFACECMD_FRAMEACTION_BEGIN = 0x0000
	SURFACECMD_FRAMEACTION_END   = 0x0001
)

// CompressedBitmapHeaderEx is TS_COMPRESSED_BITMAP_HEADER_EX, an optional
// per-frame timestamp header attached to extended bitmap data.
type CompressedBitmapHeaderEx struct {
	HighUniqueID   uint32
	LowUniqueID    uint32
	TmMilliseconds uint64
	TmSeconds      uint64
}

// BitmapDataEx is TS_BITMAP_DATA_EX (MS-RDPBCGR 2.2.9.2.1.1). Unlike
// TS_BITMAP_DATA it carries an explicit codecID, which is how the NSCodec and
// RemoteFX bitmap codecs reach the client.
type BitmapDataEx struct {
	Bpp              uint8
	Flags            uint8
	CodecID          uint8
	Width            uint16
	Height           uint16
	BitmapDataLength uint32
	Header           *CompressedBitmapHeaderEx
	BitmapData       []byte
}

// Unpack reads the fixed 12 byte header and the payload.
func (b *BitmapDataEx) Unpack(r io.Reader) error {
	var err error
	if b.Bpp, err = core.ReadUInt8(r); err != nil {
		return err
	}
	if b.Flags, err = core.ReadUInt8(r); err != nil {
		return err
	}
	if _, err = core.ReadUInt8(r); err != nil { // reserved
		return err
	}
	if b.CodecID, err = core.ReadUInt8(r); err != nil {
		return err
	}
	if b.Width, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if b.Height, err = core.ReadUint16LE(r); err != nil {
		return err
	}
	if b.BitmapDataLength, err = core.ReadUInt32LE(r); err != nil {
		return err
	}

	if b.Flags&EX_COMPRESSED_BITMAP_HEADER_PRESENT != 0 {
		h := &CompressedBitmapHeaderEx{}
		if h.HighUniqueID, err = core.ReadUInt32LE(r); err != nil {
			return err
		}
		if h.LowUniqueID, err = core.ReadUInt32LE(r); err != nil {
			return err
		}
		// The two timestamp fields are 64 bit but only 53 bits are ever
		// meaningful, so four bytes of precision are plenty here.
		for _, dst := range []*uint64{&h.TmMilliseconds, &h.TmSeconds} {
			var lo, hi uint32
			if lo, err = core.ReadUInt32LE(r); err != nil {
				return err
			}
			if hi, err = core.ReadUInt32LE(r); err != nil {
				return err
			}
			*dst = uint64(lo) | uint64(hi)<<32
		}
		b.Header = h
	}

	b.BitmapData, err = core.ReadBytes(int(b.BitmapDataLength), r)
	return err
}

// SurfaceBitsCommand is a CMDTYPE_SET_SURFACE_BITS or
// CMDTYPE_STREAM_SURFACE_BITS command. The destination rectangle is inclusive
// of the left and top edges and exclusive of the right and bottom edges.
type SurfaceBitsCommand struct {
	CmdType    uint16
	DestLeft   uint16
	DestTop    uint16
	DestRight  uint16
	DestBottom uint16
	Bitmap     BitmapDataEx
}

// FrameMarkerCommand is a CMDTYPE_FRAME_MARKER command, which brackets a
// group of surface bits commands.
type FrameMarkerCommand struct {
	FrameAction uint16
	FrameID     uint32
}

// SurfaceCommand is one entry of a surface commands update.
type SurfaceCommand struct {
	Type   uint16
	Bits   *SurfaceBitsCommand
	Marker *FrameMarkerCommand
}

// SurfaceCommandsPDU is the payload of FASTPATH_UPDATETYPE_SURFCMDS
// (MS-RDPBCGR 2.2.9.2.1). It is a sequence of self describing commands.
type SurfaceCommandsPDU struct {
	Commands []SurfaceCommand
}

func (*SurfaceCommandsPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_SURFCMDS
}

func (f *SurfaceCommandsPDU) Unpack(r io.Reader) error {
	// The payload is fully consumed by walking the command list, so the loop
	// is bounded by the reader's remaining length rather than an error.
	br, ok := r.(*bytes.Reader)
	if !ok {
		return fmt.Errorf("pdu: surface commands need a *bytes.Reader, got %T", r)
	}

	for br.Len() >= 2 {
		cmdType, err := core.ReadUint16LE(br)
		if err != nil {
			return err
		}

		switch cmdType {
		case CMDTYPE_SET_SURFACE_BITS, CMDTYPE_STREAM_SURFACE_BITS:
			c := &SurfaceBitsCommand{CmdType: cmdType}
			if c.DestLeft, err = core.ReadUint16LE(br); err != nil {
				return err
			}
			if c.DestTop, err = core.ReadUint16LE(br); err != nil {
				return err
			}
			if c.DestRight, err = core.ReadUint16LE(br); err != nil {
				return err
			}
			if c.DestBottom, err = core.ReadUint16LE(br); err != nil {
				return err
			}
			if err = c.Bitmap.Unpack(br); err != nil {
				return fmt.Errorf("pdu: surface bits command: %w", err)
			}
			f.Commands = append(f.Commands, SurfaceCommand{Type: cmdType, Bits: c})

		case CMDTYPE_FRAME_MARKER:
			m := &FrameMarkerCommand{}
			if m.FrameAction, err = core.ReadUint16LE(br); err != nil {
				return err
			}
			if m.FrameID, err = core.ReadUInt32LE(br); err != nil {
				return err
			}
			f.Commands = append(f.Commands, SurfaceCommand{Type: cmdType, Marker: m})

		default:
			// An unknown command means the remaining bytes cannot be
			// interpreted, so stop rather than emit garbage.
			return fmt.Errorf("pdu: unknown surface command type 0x%04x", cmdType)
		}
	}

	return nil
}
