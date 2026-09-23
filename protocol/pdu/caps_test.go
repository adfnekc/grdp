package pdu

import (
	"bytes"
	"testing"

	"github.com/lunixbochs/struc"
)

// The Bitmap Codecs capability is written with the server's codec list also
// being parsed, so an encoding mistake here would be easy to miss until a real
// server rejected the connection. Pin the exact bytes instead.
func TestNewNSCodecCapabilitySerializes(t *testing.T) {
	var buf bytes.Buffer
	if err := struc.Pack(&buf, NewNSCodecCapability()); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	want := []byte{
		0x01, // codec count
		// CODEC_GUID_NSCODEC
		0xB9, 0x1B, 0x8D, 0xCA,
		0x0F, 0x00,
		0x4F, 0x15,
		0x58, 0x9F, 0xAE, 0x2D, 0x1A, 0x87, 0xE2, 0xD6,
		0x01,       // codecID
		0x03, 0x00, // codecPropertiesLength
		0x00, 0x00, 0x01, // TS_NSCODEC_CAPABILITYSET
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got  %x\nwant %x", buf.Bytes(), want)
	}
}

func TestBitmapCodecsCapabilityType(t *testing.T) {
	if got := NewNSCodecCapability().Type(); got != CAPSETTYPE_BITMAP_CODECS {
		t.Fatalf("got capability type 0x%04x, want 0x%04x", got, CAPSETTYPE_BITMAP_CODECS)
	}
}

// The RemoteFX capability container is a 49 byte structure with several
// self describing nested blocks, so a byte level test is the only sane way to
// keep it honest.
func TestNewRemoteFXCapabilitySerializes(t *testing.T) {
	var buf bytes.Buffer
	if err := struc.Pack(&buf, NewRemoteFXCapability()); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	want := []byte{
		0x01, // codec count
		// CODEC_GUID_REMOTEFX
		0x12, 0x2F, 0x77, 0x76,
		0x72, 0xBD,
		0x63, 0x44,
		0xAF, 0xB3, 0xB7, 0x3C, 0x9C, 0x6F, 0x78, 0x86,
		0x03,       // codecID
		0x31, 0x00, // codecPropertiesLength = 49
		// TS_RFX_CLNT_CAPS_CONTAINER
		0x31, 0x00, 0x00, 0x00, // length
		0x01, 0x00, 0x00, 0x00, // captureFlags
		0x25, 0x00, 0x00, 0x00, // capsLength
		// TS_RFX_CAPS
		0xC0, 0xCB, // blockType CBT_CAPS
		0x08, 0x00, 0x00, 0x00, // blockLen
		0x01, 0x00, // numCapsets
		// TS_RFX_CAPSET
		0xC1, 0xCB, // blockType CBT_CAPSET
		0x1D, 0x00, 0x00, 0x00, // blockLen
		0x01,       // codecId
		0xC0, 0xCF, // capsetType CLY_CAPSET
		0x02, 0x00, // numIcaps
		0x08, 0x00, // icapLen
		// TS_RFX_ICAP for RLGR1
		0x00, 0x01, // version
		0x40, 0x00, // tileSize
		0x00, // flags
		0x01, // colConvBits
		0x01, // transformBits
		0x01, // entropyBits
		// TS_RFX_ICAP for RLGR3
		0x00, 0x01,
		0x40, 0x00,
		0x00,
		0x01,
		0x01,
		0x04,
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got  %x\nwant %x", buf.Bytes(), want)
	}
}

func TestNewBitmapCodecsCapabilityCombines(t *testing.T) {
	c := NewBitmapCodecsCapability(RDPCodecIDNSCodec, RDPCodecIDRemoteFX)
	if c.SupportedBitmapCodecs.Count != 2 {
		t.Fatalf("got count %d, want 2", c.SupportedBitmapCodecs.Count)
	}
	if len(c.SupportedBitmapCodecs.Array) != 2 {
		t.Fatalf("got %d entries, want 2", len(c.SupportedBitmapCodecs.Array))
	}
	// An unknown codec id is ignored rather than producing an empty entry.
	if got := NewBitmapCodecsCapability(0x7F); got.SupportedBitmapCodecs.Count != 0 {
		t.Fatalf("got count %d, want 0", got.SupportedBitmapCodecs.Count)
	}
}
