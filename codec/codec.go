package codec

import "fmt"

// Bitmap codec ids (MS-RDPBCGR 2.2.9.2.1.1, TS_BITMAP_DATA_EX.codecID).
const (
	CodecIDNone          = 0x00 // data is not encoded
	CodecIDNSCODEC       = 0x01
	CodecIDRemoteFX      = 0x03
	CodecIDImageRemoteFX = 0x04
	CodecIDX264          = 0x05
	CodecIDX264Image     = 0x06
)

// Decompress decodes a codec-encoded bitmap payload. codecID comes from
// TS_BITMAP_DATA_EX, or from the header of a cache bitmap revision 3 or EGFX
// surface command. A codec id of none means the payload is already raw pixels
// and is returned unchanged.
//
// The result is BGRA unless the codec documents otherwise. Callers that only
// need to display the pixels should treat a nil error as "bpp is now 32".
func Decompress(codecID uint8, data []byte, width, height, bpp int) ([]byte, error) {
	switch codecID {
	case CodecIDNone:
		return data, nil
	case CodecIDNSCODEC:
		return NSCodec(data, width, height)
	case CodecIDRemoteFX:
		return DecodeRFX(data, width, height, RFXMode)
	default:
		return nil, fmt.Errorf("codec: id 0x%02x: %w", codecID, ErrUnsupported)
	}
}

// RFXMode is the entropy coder used for RemoteFX payloads. RemoteFX data does
// not say which coder it used, so this has to match what was negotiated: a
// client advertises both ICAPs and the server picks one. RLGR1 is the safer
// default because every server supports it.
//
// Change it before decoding if the session negotiated RLGR3.
var RFXMode = RLGR1
