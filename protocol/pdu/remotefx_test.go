package pdu

import (
	"bytes"
	"testing"

	"github.com/lunixbochs/struc"
)

// newCodecsClient returns a client with the capability set the connect sequence
// builds, which is all the RemoteFX switch needs to read and write. The layer is
// built by hand rather than with NewClient because a switch and the map it edits
// are all the tests here exercise, and NewClient wants a live transport.
func newCodecsClient() *Client {
	c := &Client{PDULayer: &PDULayer{}}
	c.clientCapabilities = map[CapsType]Capability{
		CAPSETTYPE_BITMAP_CODECS: NewNSCodecCapability(),
	}
	return c
}

// newBareClient returns a client whose capability set has not been built yet,
// which is the state during Login before the connect sequence reaches the caps.
func newBareClient() *Client {
	return &Client{PDULayer: &PDULayer{}}
}

func codecsCapability(c *Client) *BitmapCodecsCapability {
	return c.clientCapabilities[CAPSETTYPE_BITMAP_CODECS].(*BitmapCodecsCapability)
}

// The default never advertises RemoteFX, and the reason is not caution about the
// wire format: the capability is conforming and the decoder is verified. A server
// offered it may put the session's screen traffic through RemoteFX instead of
// bitmap updates, and no session in that shape has been run here. So the
// advertised set has to stay at NSCodec until a caller asks.
func TestDefaultCapabilitiesAdvertiseOnlyNSCodec(t *testing.T) {
	c := newCodecsClient()
	got := codecsCapability(c)
	if got.SupportedBitmapCodecs.Count != 1 {
		t.Fatalf("got %d codecs advertised by default, want 1: %v", got.SupportedBitmapCodecs.Count, got)
	}
	if got.SupportedBitmapCodecs.Array[0].ID != RDPCodecIDNSCodec {
		t.Errorf("the default codec is 0x%02x, want NSCodec (0x%02x)",
			got.SupportedBitmapCodecs.Array[0].ID, RDPCodecIDNSCodec)
	}
}

// Server side capability parsing has to see the same set a server would see, so
// this checks what actually goes into the confirm active PDU rather than the
// struct fields.
func TestSetRemoteFxSupportAdvertisesBothCodecs(t *testing.T) {
	c := newCodecsClient()
	c.SetRemoteFxSupport(true)

	var buf bytes.Buffer
	if err := struc.Pack(&buf, codecsCapability(c)); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	got := codecsCapability(c)
	if got.SupportedBitmapCodecs.Count != 2 {
		t.Fatalf("got %d codecs advertised, want 2", got.SupportedBitmapCodecs.Count)
	}
	if got.SupportedBitmapCodecs.Array[0].ID != RDPCodecIDNSCodec ||
		got.SupportedBitmapCodecs.Array[1].ID != RDPCodecIDRemoteFX {
		t.Errorf("codec ids are %#x and %#x, want NSCodec then RemoteFX",
			got.SupportedBitmapCodecs.Array[0].ID, got.SupportedBitmapCodecs.Array[1].ID)
	}

	// NSCodec must survive: withdrawing a verified codec to gain an unverified
	// one would mean a server that answers with NSCodec has nowhere to draw.
	wire := buf.Bytes()
	if !bytes.Contains(wire, codecGUIDNSCodec[:]) {
		t.Error("NSCodec is missing from the advertised capability set")
	}
	if !bytes.Contains(wire, codecGUIDRemoteFX[:]) {
		t.Error("RemoteFX is missing from the advertised capability set")
	}
}

// Turning it off has to restore the default rather than leave a capability a
// caller no longer wants, and the wire bytes have to say so.
func TestSetRemoteFxSupportOffRestoresNSCodec(t *testing.T) {
	c := newCodecsClient()
	c.SetRemoteFxSupport(true)
	c.SetRemoteFxSupport(false)

	got := codecsCapability(c)
	if got.SupportedBitmapCodecs.Count != 1 ||
		got.SupportedBitmapCodecs.Array[0].ID != RDPCodecIDNSCodec {
		t.Fatalf("after disabling, got %d codecs, want NSCodec alone: %v",
			got.SupportedBitmapCodecs.Count, got.SupportedBitmapCodecs.Array)
	}

	var buf bytes.Buffer
	if err := struc.Pack(&buf, got); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if bytes.Contains(buf.Bytes(), codecGUIDRemoteFX[:]) {
		t.Error("RemoteFX is still advertised after disabling")
	}
}

// The switch is called during login, but a client built before its capability set
// exists must not take the process down: the guard exists so that a caller can
// set the flag in either order.
func TestSetRemoteFxSupportBeforeCapabilitiesExistIsInert(t *testing.T) {
	c := newBareClient()
	c.SetRemoteFxSupport(true)
	if c.clientCapabilities != nil {
		t.Fatal("a client with no capability set should not grow one")
	}
}
