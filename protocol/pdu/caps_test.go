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
