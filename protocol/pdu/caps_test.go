package pdu

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/adfnekc/grdp/codec"
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

// The RemoteFX capability container is a self describing nested structure, so a
// byte level test is the only sane way to keep it honest, and the entropy coder
// declared in it is what removes the guess that mattered.
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
		0x29, 0x00, // codecPropertiesLength = 41
		// TS_RFX_CLNT_CAPS_CONTAINER
		0x29, 0x00, 0x00, 0x00, // length, the whole container
		0x01, 0x00, 0x00, 0x00, // captureFlags, CARDP_CAPS_CAPTURE_NON_CAC
		0x1D, 0x00, 0x00, 0x00, // capsLength, the blocks that follow
		// TS_RFX_CAPS
		0xC0, 0xCB, // blockType CBT_CAPS
		0x08, 0x00, 0x00, 0x00, // blockLen
		0x01, 0x00, // numCapsets
		// TS_RFX_CAPSET
		0xC1, 0xCB, // blockType CBT_CAPSET
		0x15, 0x00, 0x00, 0x00, // blockLen
		0x01,       // codecId
		0xC0, 0xCF, // capsetType CLY_CAPSET
		0x01, 0x00, // numIcaps
		0x08, 0x00, // icapLen
		// TS_RFX_ICAP, RLGR1 only. The missing second one is the point: the
		// entropy coder a frame used is not recorded in the frame, and guessing
		// wrong decodes without error to a mean pixel error of 75 out of 255.
		0x00, 0x01, // version
		0x40, 0x00, // tileSize
		0x00, // flags
		0x01, // colConvBits
		0x01, // transformBits
		0x01, // entropyBits, RLGR1
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got  %x\nwant %x", buf.Bytes(), want)
	}
}

// Four numbers have to agree with each other and with the actual bytes: the codec
// entry length, the caps length, the two block lengths and the ICAP count. A hand
// written layout can pass a byte comparison while one of them is wrong, so this
// parses the same bytes back and checks the arithmetic instead.
func TestRFXContainerLengthsAgree(t *testing.T) {
	entry := NewRemoteFXCapability().SupportedBitmapCodecs.Array[0]
	p := entry.Properties

	if entry.PropertiesLength != uint16(len(p)) {
		t.Fatalf("codecPropertiesLength is %d but the container is %d bytes",
			entry.PropertiesLength, len(p))
	}

	// Where each field sits in TS_RFX_CLNT_CAPS_CONTAINER, so the reads below say
	// what they read rather than relying on a driftable counter.
	const (
		offLength    = 0  // length, and then the two blocks
		offCapsLen   = 8  // capsLength, counting the two blocks
		offCapsBType = 12 // TS_RFX_CAPS blockType
		offCapsBLen  = 14 // TS_RFX_CAPS blockLen
		offCapsets   = 20 // TS_RFX_CAPSET blockType
		offCapsBSet  = 22 // TS_RFX_CAPSET blockLen
		offCodecID   = 26 // codecId
		offCapsetTyp = 27 // capsetType
		offNumIcaps  = 29 // numIcaps
		offICAPLen   = 31 // icapLen
	)
	u16 := func(off int) uint16 { return binary.LittleEndian.Uint16(p[off : off+2]) }
	u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(p[off : off+4]) }

	if got, want := u32(offLength), uint32(len(p)); got != want {
		t.Errorf("container length field is %d, want %d", got, want)
	}
	capsBlockLen := u32(offCapsBLen)
	capsetBlockLen := u32(offCapsBSet)
	numIcaps := int(u16(offNumIcaps))
	if u16(offCapsBType) != rfxBlockCaps {
		t.Errorf("first block type is 0x%04x, want TS_RFX_CAPS (0x%04x)", u16(offCapsBType), rfxBlockCaps)
	}
	if u16(offCapsets) != rfxBlockCapset {
		t.Errorf("second block type is 0x%04x, want TS_RFX_CAPSET (0x%04x)", u16(offCapsets), rfxBlockCapset)
	}
	if p[offCodecID] != 1 {
		t.Errorf("codecId is %d, want the mandatory 1", p[offCodecID])
	}
	if u16(offCapsetTyp) != rfxCapsetType {
		t.Errorf("capsetType is 0x%04x, want CLY_CAPSET (0x%04x)", u16(offCapsetTyp), rfxCapsetType)
	}
	if u16(offICAPLen) != rfxICAPLen {
		t.Errorf("icapLen is %d, want %d", u16(offICAPLen), rfxICAPLen)
	}
	if got, want := capsBlockLen, uint32(rfxCapsHeaderLen); got != want {
		t.Errorf("TS_RFX_CAPS blockLen is %d, want %d", got, want)
	}
	if got, want := capsetBlockLen, uint32(rfxCapsetHeaderLen+numIcaps*rfxICAPLen); got != want {
		t.Errorf("TS_RFX_CAPSET blockLen is %d, want %d", got, want)
	}
	// capsLength counts the two blocks that follow it.
	if got, want := u32(offCapsLen), capsBlockLen+capsetBlockLen; got != want {
		t.Errorf("capsLength is %d, want %d", got, want)
	}
	// The header declares the ICAP count, and the bytes after the ICAP list have
	// to account for exactly that, no more and no fewer.
	if got, want := rfxICAPListStart+numIcaps*rfxICAPLen, len(p); got != want {
		t.Errorf("%d ICAPs declared but the container ends at %d, want %d", numIcaps, got, want)
	}
}

// The declared entropy coder is the safety property: the decoder cannot tell which
// one produced a frame, and the client's own default is RLGR1. Advertising RLGR3
// alongside it would let a server pick the one that decodes the wrong way.
func TestRFXContainerDeclaresOnlyTheEntropyCoderWeDecode(t *testing.T) {
	p := NewRemoteFXCapability().SupportedBitmapCodecs.Array[0].Properties
	numIcaps := int(binary.LittleEndian.Uint16(p[29:31]))
	if numIcaps != 1 {
		t.Fatalf("got %d ICAPs, want 1", numIcaps)
	}
	// The one ICAP's entropy byte is the last of its eight.
	entropy := p[rfxICAPListStart+rfxICAPLen-1]
	if entropy != rfxEntropyRLGR1 {
		t.Errorf("entropy is 0x%02x, want RLGR1 (0x%02x): the client decodes with codec.RFXMode, which defaults to RLGR1",
			entropy, rfxEntropyRLGR1)
	}
	if codec.RFXMode != codec.RLGR1 {
		t.Errorf("codec.RFXMode is %d but the capability declares RLGR1", codec.RFXMode)
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
