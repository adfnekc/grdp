package pdu

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/glog"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/protocol/t125/gcc"
	"github.com/lunixbochs/struc"
)

// CapsType is the type field of a capability set, which is what says what the rest
// of the set means. A capability exchange is a list of these, one per feature, and
// each side reads the ones it knows and ignores the ones it does not.
type CapsType uint16

// The capability set types. The two ends do not have the same list: a client sends
// the ones describing what it can receive and do, and a server sends the ones
// describing what it will send.
const (
	CAPSTYPE_GENERAL                 CapsType = 0x0001
	CAPSTYPE_BITMAP                           = 0x0002
	CAPSTYPE_ORDER                            = 0x0003
	CAPSTYPE_BITMAPCACHE                      = 0x0004
	CAPSTYPE_CONTROL                          = 0x0005
	CAPSTYPE_ACTIVATION                       = 0x0007
	CAPSTYPE_POINTER                          = 0x0008
	CAPSTYPE_SHARE                            = 0x0009
	CAPSTYPE_COLORCACHE                       = 0x000A
	CAPSTYPE_SOUND                            = 0x000C
	CAPSTYPE_INPUT                            = 0x000D
	CAPSTYPE_FONT                             = 0x000E
	CAPSTYPE_BRUSH                            = 0x000F
	CAPSTYPE_GLYPHCACHE                       = 0x0010
	CAPSTYPE_OFFSCREENCACHE                   = 0x0011
	CAPSTYPE_BITMAPCACHE_HOSTSUPPORT          = 0x0012
	CAPSTYPE_BITMAPCACHE_REV2                 = 0x0013
	CAPSTYPE_VIRTUALCHANNEL                   = 0x0014
	CAPSTYPE_DRAWNINEGRIDCACHE                = 0x0015
	CAPSTYPE_DRAWGDIPLUS                      = 0x0016
	CAPSTYPE_RAIL                             = 0x0017
	CAPSTYPE_WINDOW                           = 0x0018
	CAPSETTYPE_COMPDESK                       = 0x0019
	CAPSETTYPE_MULTIFRAGMENTUPDATE            = 0x001A
	CAPSETTYPE_LARGE_POINTER                  = 0x001B
	CAPSETTYPE_SURFACE_COMMANDS               = 0x001C
	CAPSETTYPE_BITMAP_CODECS                  = 0x001D
	CAPSSETTYPE_FRAME_ACKNOWLEDGE             = 0x001E
)

// String names the capability type, for logs and errors. An unrecognised value is
// reported as Unknown rather than as a number, which is the common case: a server
// sends capability sets this client does not know.
func (c CapsType) String() string {
	switch c {
	case CAPSTYPE_GENERAL:
		return "CAPSTYPE_GENERAL"
	case CAPSTYPE_BITMAP:
		return "CAPSTYPE_BITMAP"
	case CAPSTYPE_ORDER:
		return "CAPSTYPE_ORDER"
	case CAPSTYPE_BITMAPCACHE:
		return "CAPSTYPE_BITMAPCACHE"
	case CAPSTYPE_CONTROL:
		return "CAPSTYPE_CONTROL"
	case CAPSTYPE_ACTIVATION:
		return "CAPSTYPE_ACTIVATION"
	case CAPSTYPE_POINTER:
		return "CAPSTYPE_POINTER"
	case CAPSTYPE_SHARE:
		return "CAPSTYPE_SHARE"
	case CAPSTYPE_COLORCACHE:
		return "CAPSTYPE_COLORCACHE"
	case CAPSTYPE_SOUND:
		return "CAPSTYPE_SOUND"
	case CAPSTYPE_INPUT:
		return "CAPSTYPE_INPUT"
	case CAPSTYPE_FONT:
		return "CAPSTYPE_FONT"
	case CAPSTYPE_BRUSH:
		return "CAPSTYPE_BRUSH"
	case CAPSTYPE_GLYPHCACHE:
		return "CAPSTYPE_GLYPHCACHE"
	case CAPSTYPE_OFFSCREENCACHE:
		return "CAPSTYPE_OFFSCREENCACHE"
	case CAPSTYPE_BITMAPCACHE_HOSTSUPPORT:
		return "CAPSTYPE_BITMAPCACHE_HOSTSUPPORT"
	case CAPSTYPE_BITMAPCACHE_REV2:
		return "CAPSTYPE_BITMAPCACHE_REV2"
	case CAPSTYPE_VIRTUALCHANNEL:
		return "CAPSTYPE_VIRTUALCHANNEL"
	case CAPSTYPE_DRAWNINEGRIDCACHE:
		return "CAPSTYPE_DRAWNINEGRIDCACHE"
	case CAPSTYPE_DRAWGDIPLUS:
		return "CAPSTYPE_DRAWGDIPLUS"
	case CAPSTYPE_RAIL:
		return "CAPSTYPE_RAIL"
	case CAPSTYPE_WINDOW:
		return "CAPSTYPE_WINDOW"
	case CAPSETTYPE_COMPDESK:
		return "CAPSETTYPE_COMPDESK"
	case CAPSETTYPE_MULTIFRAGMENTUPDATE:
		return "CAPSETTYPE_MULTIFRAGMENTUPDATE"
	case CAPSETTYPE_LARGE_POINTER:
		return "CAPSETTYPE_LARGE_POINTER"
	case CAPSETTYPE_SURFACE_COMMANDS:
		return "CAPSETTYPE_SURFACE_COMMANDS"
	case CAPSETTYPE_BITMAP_CODECS:
		return "CAPSETTYPE_BITMAP_CODECS"
	case CAPSSETTYPE_FRAME_ACKNOWLEDGE:
		return "CAPSSETTYPE_FRAME_ACKNOWLEDGE"
	}

	return "Unknown"
}

// MajorType is the operating system a client claims in its general capability. It
// is informational: the server does not change how it behaves for it.
type MajorType uint16

// The operating system families a client can claim.
const (
	OSMAJORTYPE_UNSPECIFIED MajorType = 0x0000
	OSMAJORTYPE_WINDOWS               = 0x0001
	OSMAJORTYPE_OS2                   = 0x0002
	OSMAJORTYPE_MACINTOSH             = 0x0003
	OSMAJORTYPE_UNIX                  = 0x0004
	OSMAJORTYPE_IOS                   = 0x0005
	OSMAJORTYPE_OSX                   = 0x0006
	OSMAJORTYPE_ANDROID               = 0x0007
)

// MinorType is the operating system version within the family MajorType names.
type MinorType uint16

// The operating system versions, which are only meaningful beside a MajorType: the
// same number means a different thing in each family.
const (
	OSMINORTYPE_UNSPECIFIED    MinorType = 0x0000
	OSMINORTYPE_WINDOWS_31X              = 0x0001
	OSMINORTYPE_WINDOWS_95               = 0x0002
	OSMINORTYPE_WINDOWS_NT               = 0x0003
	OSMINORTYPE_OS2_V21                  = 0x0004
	OSMINORTYPE_POWER_PC                 = 0x0005
	OSMINORTYPE_MACINTOSH                = 0x0006
	OSMINORTYPE_NATIVE_XSERVER           = 0x0007
	OSMINORTYPE_PSEUDO_XSERVER           = 0x0008
	OSMINORTYPE_WINDOWS_RT               = 0x0009
)

// The general capability's extra flags, which are what a client advertises about the
// connection itself rather than about a feature.
//
// FASTPATH_OUTPUT_SUPPORTED is the one the fast path depends on, since a server that
// is not told the client understands fast-path output will not send any.
// ENC_SALTED_CHECKSUM changes how the MAC is computed and is only meaningful with
// Standard RDP Security.
const (
	FASTPATH_OUTPUT_SUPPORTED  uint16 = 0x0001
	NO_BITMAP_COMPRESSION_HDR         = 0x0400
	LONG_CREDENTIALS_SUPPORTED        = 0x0004
	AUTORECONNECT_SUPPORTED           = 0x0008
	ENC_SALTED_CHECKSUM               = 0x0010
)

// OrderFlag is the order flags in the order capability: what the client understands
// about how orders are encoded rather than which orders it can execute, which is the
// order list beside it.
type OrderFlag uint16

// The order flags. ZEROBOUNDSDELTASSUPPORT is what allows the field flags to be
// shortened, and reading it as though it were absent is what turns a two byte flag
// byte into a wrong field length.
const (
	NEGOTIATEORDERSUPPORT   OrderFlag = 0x0002
	ZEROBOUNDSDELTASSUPPORT           = 0x0008
	COLORINDEXSUPPORT                 = 0x0020
	SOLIDPATTERNBRUSHONLY             = 0x0040
	ORDERFLAGS_EXTRA_FLAGS            = 0x0080
)

// Order is the value a client puts in the secondary order list to say it can execute
// that order. The values are the ones the wire format uses, and they are not
// contiguous: the gaps are orders that exist in the specification and are not
// supported here.
//
// See MS-RDPBCGR 2.2.7.1.3.
type Order uint8

// The order indexes the client says it supports.
const (
	TS_NEG_DSTBLT_INDEX             Order = 0x00
	TS_NEG_PATBLT_INDEX                   = 0x01
	TS_NEG_SCRBLT_INDEX                   = 0x02
	TS_NEG_MEMBLT_INDEX                   = 0x03
	TS_NEG_MEM3BLT_INDEX                  = 0x04
	TS_NEG_DRAWNINEGRID_INDEX             = 0x07
	TS_NEG_LINETO_INDEX                   = 0x08
	TS_NEG_MULTI_DRAWNINEGRID_INDEX       = 0x09
	TS_NEG_SAVEBITMAP_INDEX               = 0x0B
	TS_NEG_MULTIDSTBLT_INDEX              = 0x0F
	TS_NEG_MULTIPATBLT_INDEX              = 0x10
	TS_NEG_MULTISCRBLT_INDEX              = 0x11
	TS_NEG_MULTIOPAQUERECT_INDEX          = 0x12
	TS_NEG_FAST_INDEX_INDEX               = 0x13
	TS_NEG_POLYGON_SC_INDEX               = 0x14
	TS_NEG_POLYGON_CB_INDEX               = 0x15
	TS_NEG_POLYLINE_INDEX                 = 0x16
	TS_NEG_FAST_GLYPH_INDEX               = 0x18
	TS_NEG_ELLIPSE_SC_INDEX               = 0x19
	TS_NEG_ELLIPSE_CB_INDEX               = 0x1A
	TS_NEG_GLYPH_INDEX_INDEX              = 0x1B
)

// OrderEx is the extended order flags, which are the second order flag word and cover
// what the first one has no room for.
type OrderEx uint16

// The extended order flags. CACHE_BITMAP_REV3_SUPPORT is not set by this client:
// bitmap cache revision 3 is parsed but never used, see the note in data.go.
const (
	ORDERFLAGS_EX_CACHE_BITMAP_REV3_SUPPORT   OrderEx = 0x0002
	ORDERFLAGS_EX_ALTSEC_FRAME_MARKER_SUPPORT         = 0x0004
)

// The input capability's flags, which say what input the client can send and receive.
//
// INPUT_FLAG_FASTPATH_INPUT and INPUT_FLAG_FASTPATH_INPUT2 are what gate the fast
// path at all: a client that does not advertise them is not sent fast-path input and
// should not send any. MOUSEX is the extended mouse, which carries a button number
// rather than the fixed four buttons.
const (
	INPUT_FLAG_SCANCODES       uint16 = 0x0001
	INPUT_FLAG_MOUSEX                 = 0x0004
	INPUT_FLAG_FASTPATH_INPUT         = 0x0008
	INPUT_FLAG_UNICODE                = 0x0010
	INPUT_FLAG_FASTPATH_INPUT2        = 0x0020
	INPUT_FLAG_UNUSED1                = 0x0040
	INPUT_FLAG_UNUSED2                = 0x0080
	INPUT_FLAG_MOUSE_HWHEEL           = 0x0100
)

// BrushSupport is the brush support level in the brush capability: whether the client
// can build a brush from a colour or from a bitmap, or only use the ones the server
// makes for it.
type BrushSupport uint32

// The brush support levels.
const (
	BRUSH_DEFAULT    BrushSupport = 0x00000000
	BRUSH_COLOR_8x8               = 0x00000001
	BRUSH_COLOR_FULL              = 0x00000002
)

// GlyphSupport is the glyph support level in the glyph cache capability: whether the
// client can receive individual glyphs, whole glyph caches, or both.
type GlyphSupport uint16

// The glyph support levels.
const (
	GLYPH_SUPPORT_NONE    GlyphSupport = 0x0000
	GLYPH_SUPPORT_PARTIAL              = 0x0001
	GLYPH_SUPPORT_FULL                 = 0x0002
	GLYPH_SUPPORT_ENCODE               = 0x0003
)

// OffscreenSupportLevel is the offscreen bitmap cache capability's support level,
// which is a boolean despite the type.
type OffscreenSupportLevel uint32

// The two support levels.
const (
	OSL_FALSE OffscreenSupportLevel = 0x00000000
	OSL_TRUE                        = 0x00000001
)

// VirtualChannelCompressionFlag is the compression the client accepts on its static
// virtual channels. It is a level rather than a set of flags: the values are ordered
// and a client sends one.
type VirtualChannelCompressionFlag uint32

// The virtual channel compression levels.
const (
	VCCAPS_NO_COMPR    VirtualChannelCompressionFlag = 0x00000000
	VCCAPS_COMPR_SC                                  = 0x00000001
	VCCAPS_COMPR_CS_8K                               = 0x00000002
)

// SoundFlag is the sound capability's flags, which report whether the session plays
// beeps. It is a single bit, and the name pairs it with a value of zero rather than
// with a mask.
type SoundFlag uint16

// The sound flags.
const (
	SOUND_NONE       SoundFlag = 0x0000
	SOUND_BEEPS_FLAG           = 0x0001
)

// RailsupportLevel is the RemoteApp support level in the RemoteApp capability, which
// is a bitmask of the RAIL features the client can handle.
type RailsupportLevel uint32

// The RAIL support levels.
const (
	RAIL_LEVEL_SUPPORTED                           = 0x00000001
	RAIL_LEVEL_DOCKED_LANGBAR_SUPPORTED            = 0x00000002
	RAIL_LEVEL_SHELL_INTEGRATION_SUPPORTED         = 0x00000004
	RAIL_LEVEL_LANGUAGE_IME_SYNC_SUPPORTED         = 0x00000008
	RAIL_LEVEL_SERVER_TO_CLIENT_IME_SYNC_SUPPORTED = 0x00000010
	RAIL_LEVEL_HIDE_MINIMIZED_APPS_SUPPORTED       = 0x00000020
	RAIL_LEVEL_WINDOW_CLOAKING_SUPPORTED           = 0x00000040
	RAIL_LEVEL_HANDSHAKE_EX_SUPPORTED              = 0x00000080
)

// The input event codes, which say what an input event is. The two mouse ones share
// one structure and are told apart by the pointer flags, which is why a caller sending
// a mouse event has to say which of the two it means.
const (
	INPUT_EVENT_SYNC     = 0x0000
	INPUT_EVENT_UNUSED   = 0x0002
	INPUT_EVENT_SCANCODE = 0x0004
	INPUT_EVENT_UNICODE  = 0x0005
	INPUT_EVENT_MOUSE    = 0x8001
	INPUT_EVENT_MOUSEX   = 0x8002
)

// The pointer flags, which are the whole meaning of a pointer event: a movement
// carries MOVE, a button carries DOWN with the button, and a wheel carries the
// rotation. The values are a bitmask and the groups are not meant to be combined.
const (
	PTRFLAGS_HWHEEL  = 0x0400
	PTRFLAGS_WHEEL   = 0x0200
	PTRFLAGS_MOVE    = 0x0800
	PTRFLAGS_DOWN    = 0x8000
	PTRFLAGS_BUTTON1 = 0x1000
	PTRFLAGS_BUTTON2 = 0x2000
	PTRFLAGS_BUTTON3 = 0x4000

	// PTRFLAGS_WHEEL_NEGATIVE is the sign bit of the rotation value that travels
	// in WheelRotationMask, and not a flag to combine with a positive magnitude:
	// MS-RDPBCGR defines it as saying that the value in that field "is negative
	// and MUST be sign-extended before injection at the server". One notch down
	// is therefore 0x0188, which is the two's complement of -120, and not
	// 0x0100|0x78, which a server reads as -136.
	PTRFLAGS_WHEEL_NEGATIVE = 0x0100

	// WheelRotationMask covers the nine bit signed rotation field, so bit 8 is
	// the sign. See PTRFLAGS_WHEEL_NEGATIVE.
	WheelRotationMask = 0x01FF
)

// The toggle key states and the keyboard event flags.
//
// The four TS_SYNC values are the state of the locks as they appear in a synchronise
// event: the fast path has room for five bits and the slow path for a full word, and
// these four mean the same in both. The KBDFLAGS values are the keyboard event flags,
// where RELEASE without DOWN is what a key up looks like.
const (
	// Toggle key states, as they appear in a synchronise event. The fast path
	// has room for five bits and the slow path for a full word; the four below
	// mean the same thing in both.
	TS_SYNC_SCROLL_LOCK = 0x00000001
	TS_SYNC_NUM_LOCK    = 0x00000002
	TS_SYNC_CAPS_LOCK   = 0x00000004
	TS_SYNC_KANA_LOCK   = 0x00000008

	KBDFLAGS_EXTENDED = 0x0100
	KBDFLAGS_DOWN     = 0x4000
	KBDFLAGS_RELEASE  = 0x8000
)

// SurfaceCmdFlags is the surface commands the client advertises in its surface
// commands capability. The graphics channel needs SET_SURFACE_BITS and
// STREAM_SURFACE_BITS, and FRAME_MARKER is what a server sends between frames.
type SurfaceCmdFlags uint32

// The surface command flags.
const (
	SURFCMDS_SET_SURFACE_BITS    = 0x00000002
	SURFCMDS_FRAME_MARKER        = 0x00000010
	SURFCMDS_STREAM_SURFACE_BITS = 0x00000040
)

// Capability is one capability set. A set is written and read as a header with its
// type and length followed by the set itself, so a reader needs the type before it
// can parse the body, which is what Type provides.
//
// The type is a method rather than a field so that a set cannot disagree with the
// type it was registered under: the value comes from the Go type.
type Capability interface {
	Type() CapsType
}

// GeneralCapability is the general capability, which is about the connection rather than a feature: the protocol version, the operating system the client claims, and the compression it will accept.
//
// See MS-RDPBCGR 2.2.7.1.
type GeneralCapability struct {
	// 010018000100030000020000000015040000000000000000
	OSMajorType             MajorType `struc:"little"`
	OSMinorType             MinorType `struc:"little"`
	ProtocolVersion         uint16    `struc:"little"`
	Pad2octetsA             uint16    `struc:"little"`
	GeneralCompressionTypes uint16    `struc:"little"`
	ExtraFlags              uint16    `struc:"little"`
	UpdateCapabilityFlag    uint16    `struc:"little"`
	RemoteUnshareFlag       uint16    `struc:"little"`
	GeneralCompressionLevel uint16    `struc:"little"`
	RefreshRectSupport      uint8     `struc:"little"`
	SuppressOutputSupport   uint8     `struc:"little"`
}

// Type reports that this is the general capability set.
func (*GeneralCapability) Type() CapsType {
	return CAPSTYPE_GENERAL
}

// BitmapCapability is the bitmap capability, which advertises the colour depths the client can receive and the desktop size it wants.
//
// See MS-RDPBCGR 2.2.7.1.
type BitmapCapability struct {
	// 02001c00180001000100010000052003000000000100000001000000
	PreferredBitsPerPixel    gcc.HighColor `struc:"little"`
	Receive1BitPerPixel      uint16        `struc:"little"`
	Receive4BitsPerPixel     uint16        `struc:"little"`
	Receive8BitsPerPixel     uint16        `struc:"little"`
	DesktopWidth             uint16        `struc:"little"`
	DesktopHeight            uint16        `struc:"little"`
	Pad2octets               uint16        `struc:"little"`
	DesktopResizeFlag        uint16        `struc:"little"`
	BitmapCompressionFlag    uint16        `struc:"little"`
	HighColorFlags           uint8         `struc:"little"`
	DrawingFlags             uint8         `struc:"little"`
	MultipleRectangleSupport uint16        `struc:"little"`
	Pad2octetsB              uint16        `struc:"little"`
}

// Type reports that this is the bitmap capability set.
func (*BitmapCapability) Type() CapsType {
	return CAPSTYPE_BITMAP
}

// BitmapCacheCapability is the revision 1 bitmap cache capability, which gives the entry count and cell size of the three caches this revision has.
//
// See MS-RDPBCGR 2.2.7.1.
type BitmapCacheCapability struct {
	// 04002800000000000000000000000000000000000000000000000000000000000000000000000000
	Pad1                  uint32 `struc:"little"`
	Pad2                  uint32 `struc:"little"`
	Pad3                  uint32 `struc:"little"`
	Pad4                  uint32 `struc:"little"`
	Pad5                  uint32 `struc:"little"`
	Pad6                  uint32 `struc:"little"`
	Cache0Entries         uint16 `struc:"little"`
	Cache0MaximumCellSize uint16 `struc:"little"`
	Cache1Entries         uint16 `struc:"little"`
	Cache1MaximumCellSize uint16 `struc:"little"`
	Cache2Entries         uint16 `struc:"little"`
	Cache2MaximumCellSize uint16 `struc:"little"`
}

// Type reports that this is the bitmap cache revision 1 capability set.
func (*BitmapCacheCapability) Type() CapsType {
	return CAPSTYPE_BITMAPCACHE
}

// OrderCapability is the order capability, which lists the drawing orders the client can execute and the flags describing how it understands them.
//
// See MS-RDPBCGR 2.2.7.1.
type OrderCapability struct {
	// 030058000000000000000000000000000000000000000000010014000000010000000a0000000000000000000000000000000000000000000000000000000000000000000000000000000000008403000000000000000000
	TerminalDescriptor      [16]byte
	Pad4octetsA             uint32    `struc:"little"`
	DesktopSaveXGranularity uint16    `struc:"little"`
	DesktopSaveYGranularity uint16    `struc:"little"`
	Pad2octetsA             uint16    `struc:"little"`
	MaximumOrderLevel       uint16    `struc:"little"`
	NumberFonts             uint16    `struc:"little"`
	OrderFlags              OrderFlag `struc:"little"`
	OrderSupport            [32]byte
	TextFlags               uint16 `struc:"little"`
	OrderSupportExFlags     uint16 `struc:"little"`
	Pad4octetsB             uint32 `struc:"little"`
	DesktopSaveSize         uint32 `struc:"little"`
	Pad2octetsC             uint16 `struc:"little"`
	Pad2octetsD             uint16 `struc:"little"`
	TextANSICodePage        uint16 `struc:"little"`
	Pad2octetsE             uint16 `struc:"little"`
}

// Type reports that this is the order capability set.
func (*OrderCapability) Type() CapsType {
	return CAPSTYPE_ORDER
}

// PointerCapability is the pointer capability, which advertises colour pointers and the cache sizes.
//
// See MS-RDPBCGR 2.2.7.1.
type PointerCapability struct {
	ColorPointerFlag      uint16 `struc:"little"`
	ColorPointerCacheSize uint16 `struc:"little"`
	// old version of rdp doesn't support ...
	PointerCacheSize uint16 `struc:"little"` // only server need
}

// Type reports that this is the pointer capability set.
func (*PointerCapability) Type() CapsType {
	return CAPSTYPE_POINTER
}

// InputCapability is the input capability, which advertises fast-path input, the extended mouse and Unicode, and reports the keyboard layout.
//
// See MS-RDPBCGR 2.2.7.1.
type InputCapability struct {
	// 0d005c001500000009040000040000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000c000000
	Flags       uint16 `struc:"little"`
	Pad2octetsA uint16 `struc:"little"`
	// same value as gcc.ClientCoreSettings.kbdLayout
	KeyboardLayout gcc.KeyboardLayout `struc:"little"`
	// same value as gcc.ClientCoreSettings.keyboardType
	KeyboardType uint32 `struc:"little"`
	// same value as gcc.ClientCoreSettings.keyboardSubType
	KeyboardSubType uint32 `struc:"little"`
	// same value as gcc.ClientCoreSettings.keyboardFnKeys
	KeyboardFunctionKey uint32 `struc:"little"`
	// same value as gcc.ClientCoreSettingrrs.imeFileName
	ImeFileName [64]byte
	//need add 0c000000 in the end
}

// Type reports that this is the input capability set.
func (*InputCapability) Type() CapsType {
	return CAPSTYPE_INPUT
}

// BrushCapability is the brush capability, which says whether the client can build a coloured brush itself or needs the server to make it.
//
// See MS-RDPBCGR 2.2.7.1.
type BrushCapability struct {
	// 0f00080000000000
	SupportLevel BrushSupport `struc:"little"`
}

// Type reports that this is the brush capability set.
func (*BrushCapability) Type() CapsType {
	return CAPSTYPE_BRUSH
}

type cacheEntry struct {
	Entries         uint16 `struc:"little"`
	MaximumCellSize uint16 `struc:"little"`
}

// GlyphCapability is the glyph cache capability, which advertises the fragment cache and the glyph support level.
//
// See MS-RDPBCGR 2.2.7.1.
type GlyphCapability struct {
	// 10003400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000
	GlyphCache   [10]cacheEntry `struc:"little"`
	FragCache    uint32         `struc:"little"`
	SupportLevel GlyphSupport   `struc:"little"`
	Pad2octets   uint16         `struc:"little"`
}

// Type reports that this is the glyph cache capability set.
func (*GlyphCapability) Type() CapsType {
	return CAPSTYPE_GLYPHCACHE
}

// OffscreenBitmapCacheCapability is the offscreen bitmap cache capability, which advertises the offscreen cache's size and entry count.
//
// See MS-RDPBCGR 2.2.7.1.
type OffscreenBitmapCacheCapability struct {
	// 11000c000000000000000000
	SupportLevel OffscreenSupportLevel `struc:"little"`
	CacheSize    uint16                `struc:"little"`
	CacheEntries uint16                `struc:"little"`
}

// Type reports that this is the offscreen bitmap cache capability set.
func (*OffscreenBitmapCacheCapability) Type() CapsType {
	return CAPSTYPE_OFFSCREENCACHE
}

// BitmapCache2Capability is the revision 2 bitmap cache capability, which gives the cell counts of the five caches this revision has.
//
// See MS-RDPBCGR 2.2.7.1.
type BitmapCache2Capability struct {
	BitmapCachePersist uint16   `struc:"little"`
	Pad2octets         uint8    `struc:"little"`
	CachesNum          uint8    `struc:"little"`
	BmpC0Cells         uint32   `struc:"little"`
	BmpC1Cells         uint32   `struc:"little"`
	BmpC2Cells         uint32   `struc:"little"`
	BmpC3Cells         uint32   `struc:"little"`
	BmpC4Cells         uint32   `struc:"little"`
	Pad2octets1        [12]byte `struc:"little"`
}

// Type reports that this is the bitmap cache revision 2 capability set.
func (*BitmapCache2Capability) Type() CapsType {
	return CAPSTYPE_BITMAPCACHE_REV2
}

// VirtualChannelCapability is the virtual channel capability, which advertises compression on the static virtual channels.
//
// See MS-RDPBCGR 2.2.7.1.
type VirtualChannelCapability struct {
	// 14000c000000000000000000
	Flags       VirtualChannelCompressionFlag `struc:"little"`
	VCChunkSize uint32                        `struc:"little"` // optional
}

// Type reports that this is the virtual channel capability set.
func (*VirtualChannelCapability) Type() CapsType {
	return CAPSTYPE_VIRTUALCHANNEL
}

// SoundCapability is the sound capability, which reports whether the session plays beeps.
//
// See MS-RDPBCGR 2.2.7.1.
type SoundCapability struct {
	// 0c00080000000000
	Flags      SoundFlag `struc:"little"`
	Pad2octets uint16    `struc:"little"`
}

// Type reports that this is the sound capability set.
func (*SoundCapability) Type() CapsType {
	return CAPSTYPE_SOUND
}

// ControlCapability is the control capability, which advertises the control requests the client will accept and cooperate with.
//
// See MS-RDPBCGR 2.2.7.1.
type ControlCapability struct {
	ControlFlags     uint16 `struc:"little"`
	RemoteDetachFlag uint16 `struc:"little"`
	ControlInterest  uint16 `struc:"little"`
	DetachInterest   uint16 `struc:"little"`
}

// Type reports that this is the control capability set.
func (*ControlCapability) Type() CapsType {
	return CAPSTYPE_CONTROL
}

// WindowActivationCapability is the window activation capability, which reports the keys the client will keep to itself rather than sending to the server.
//
// See MS-RDPBCGR 2.2.7.1.
type WindowActivationCapability struct {
	HelpKeyFlag          uint16 `struc:"little"`
	HelpKeyIndexFlag     uint16 `struc:"little"`
	HelpExtendedKeyFlag  uint16 `struc:"little"`
	WindowManagerKeyFlag uint16 `struc:"little"`
}

// Type reports that this is the window activation capability set.
func (*WindowActivationCapability) Type() CapsType {
	return CAPSTYPE_ACTIVATION
}

// FontCapability is the font capability, which reports whether the client supports bitmap fonts in the order path.
//
// See MS-RDPBCGR 2.2.7.1.
type FontCapability struct {
	SupportFlags uint16 `struc:"little"`
	Pad2octets   uint16 `struc:"little"`
}

// Type reports that this is the font capability set.
func (*FontCapability) Type() CapsType {
	return CAPSTYPE_FONT
}

// ColorCacheCapability is the colour table cache capability, which gives the number of colour tables the client caches.
//
// See MS-RDPBCGR 2.2.7.1.
type ColorCacheCapability struct {
	CacheSize  uint16 `struc:"little"`
	Pad2octets uint16 `struc:"little"`
}

// Type reports that this is the colour cache capability set.
func (*ColorCacheCapability) Type() CapsType {
	return CAPSTYPE_COLORCACHE
}

// ShareCapability is the share capability, which carries the session id both sides use to label the session.
//
// See MS-RDPBCGR 2.2.7.1.
type ShareCapability struct {
	NodeId     uint16 `struc:"little"`
	Pad2octets uint16 `struc:"little"`
}

// Type reports that this is the share capability set.
func (*ShareCapability) Type() CapsType {
	return CAPSTYPE_SHARE
}

// MultiFragmentUpdate is the multi fragment update capability, which says whether the client can receive one update split across several fragments.
//
// See MS-RDPBCGR 2.2.7.1.
type MultiFragmentUpdate struct {
	// 1a00080000000000
	MaxRequestSize uint32 `struc:"little"`
}

// Type reports that this is the multi fragment update capability set.
func (*MultiFragmentUpdate) Type() CapsType {
	return CAPSETTYPE_MULTIFRAGMENTUPDATE
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegdi/52635737-d144-4f47-9c88-b48ceaf3efb4

// DrawGDIPlusCapability is the GDI+ capability, which advertises the GDI+ version and the cache the client supports.
//
// See MS-RDPBCGR 2.2.7.1.
type DrawGDIPlusCapability struct {
	SupportLevel             uint32
	GdipVersion              uint32
	CacheLevel               uint32
	GdipCacheEntries         [10]byte
	GdipCacheChunkSize       [8]byte
	GdipImageCacheProperties [6]byte
}

// Type reports that this is the GDI+ capability set.
func (*DrawGDIPlusCapability) Type() CapsType {
	return CAPSTYPE_DRAWGDIPLUS
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/86507fed-a0ee-4242-b802-237534a8f65e
type BitmapCodec struct {
	GUID             [16]byte
	ID               uint8
	PropertiesLength uint16 `struc:"little,sizeof=Properties"`
	Properties       []byte
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/408b1878-9f6e-4106-8329-1af42219ba6a
type BitmapCodecS struct {
	Count uint8 `struc:"sizeof=Array"`
	Array []BitmapCodec
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/17e80f50-d163-49de-a23b-fd6456aa472f
// BitmapCodecsCapability is the bitmap codecs capability, which lists the codecs the client can decode. It is what makes RemoteFX and NSCodec possible: a codec that is not in this list is never sent.
//
// See MS-RDPBCGR 2.2.7.1.
type BitmapCodecsCapability struct {
	SupportedBitmapCodecs BitmapCodecS // A variable-length field containing a TS_BITMAPCODECS structure (section 2.2.7.2.10.1).
}

// Bitmap codec ids are assigned by the client and referenced by
// TS_BITMAP_DATA_EX.codecID (MS-RDPBCGR 2.2.9.2.1.1).
const (
	RDPCodecIDNSCodec  = 0x01
	RDPCodecIDRemoteFX = 0x03
)

// codecGUIDNSCodec is CODEC_GUID_NSCODEC (MS-RDPBCGR 2.2.7.2.10.1.1.1),
// stored in the little endian form the wire format uses.
var codecGUIDNSCodec = [16]byte{
	0xB9, 0x1B, 0x8D, 0xCA,
	0x0F, 0x00,
	0x4F, 0x15,
	0x58, 0x9F, 0xAE, 0x2D, 0x1A, 0x87, 0xE2, 0xD6,
}

// codecGUIDRemoteFX is CODEC_GUID_REMOTEFX (MS-RDPBCGR 2.2.7.2.10.1.1.1).
var codecGUIDRemoteFX = [16]byte{
	0x12, 0x2F, 0x77, 0x76,
	0x72, 0xBD,
	0x63, 0x44,
	0xAF, 0xB3, 0xB7, 0x3C, 0x9C, 0x6F, 0x78, 0x86,
}

// TS_RFX_CAPS / TS_RFX_CAPSET block types and capability values
// (MS-RDPRFX 2.2.2.1).
const (
	rfxBlockCaps        = 0xCBC0
	rfxBlockCapset      = 0xCBC1
	rfxCapsetType       = 0xCFC0
	rfxVersion10        = 0x0100
	rfxTile64x64        = 0x0040
	rfxColConvICT       = 0x01
	rfxXformDWT53A      = 0x01
	rfxEntropyRLGR1     = 0x01
	rfxEntropyRLGR3     = 0x04
	rfxCaptureNonCAC    = 0x00000001
	rfxClientCapsLength = 49
)

// NewNSCodecCapability builds the Bitmap Codecs capability set advertising
// NSCodec. A client advertises the codec ids it can decode together with its
// decoder preferences; MS-RDPBCGR 2.2.7.2.10.1.1 says NSCodec properties carry
// a TS_NSCODEC_CAPABILITYSET from MS-RDPNSC.
func NewNSCodecCapability() *BitmapCodecsCapability {
	return NewBitmapCodecsCapability(RDPCodecIDNSCodec)
}

// NewRemoteFXCapability builds the Bitmap Codecs capability set advertising
// only RemoteFX.
//
// This has not been exercised against a server that speaks RemoteFX, so it is
// deliberately not part of the default capability set. The decoder itself is
// verified against reference data, so enabling this is what makes RemoteFX
// reachable through Set Surface Bits.
func NewRemoteFXCapability() *BitmapCodecsCapability {
	return NewBitmapCodecsCapability(RDPCodecIDRemoteFX)
}

// NewBitmapCodecsCapability builds a Bitmap Codecs capability set from the
// codec ids the client can decode.
func NewBitmapCodecsCapability(codecIDs ...uint8) *BitmapCodecsCapability {
	c := &BitmapCodecsCapability{}
	for _, id := range codecIDs {
		switch id {
		case RDPCodecIDNSCodec:
			c.SupportedBitmapCodecs.Array = append(c.SupportedBitmapCodecs.Array, BitmapCodec{
				GUID: codecGUIDNSCodec,
				ID:   RDPCodecIDNSCodec,
				// fAllowDynamicFidelity, fAllowSubsampling, colorLossLevel.
				// Chroma subsampling is refused and colour loss is kept at its
				// minimum so the decoded image stays close to lossless.
				PropertiesLength: 3,
				Properties:       []byte{0, 0, 1},
			})
		case RDPCodecIDRemoteFX:
			c.SupportedBitmapCodecs.Array = append(c.SupportedBitmapCodecs.Array, BitmapCodec{
				GUID:             codecGUIDRemoteFX,
				ID:               RDPCodecIDRemoteFX,
				PropertiesLength: rfxClientCapsLength,
				Properties:       rfxClientCapabilityContainer(),
			})
		}
	}
	c.SupportedBitmapCodecs.Count = uint8(len(c.SupportedBitmapCodecs.Array))
	return c
}

// rfxClientCapabilityContainer builds the 49 byte TS_RFX_CLNT_CAPS_CONTAINER
// that a client sends for RemoteFX: it lists the two entropy coders it can
// decode, and nothing else, since the remaining parameters are fixed.
func rfxClientCapabilityContainer() []byte {
	b := make([]byte, 0, rfxClientCapsLength)
	u16 := func(v uint16) { b = append(b, byte(v), byte(v>>8)) }
	u32 := func(v uint32) {
		b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}

	u32(rfxClientCapsLength) // length
	u32(rfxCaptureNonCAC)    // captureFlags
	u32(37)                  // capsLength
	// TS_RFX_CAPS
	u16(rfxBlockCaps)
	u32(8) // blockLen
	u16(1) // numCapsets
	// TS_RFX_CAPSET
	u16(rfxBlockCapset)
	u32(29)             // blockLen
	b = append(b, 0x01) // codecId, MUST be 0x01
	u16(rfxCapsetType)
	u16(2) // numIcaps
	u16(8) // icapLen
	// TS_RFX_ICAP, one per entropy coder.
	for _, entropy := range []byte{rfxEntropyRLGR1, rfxEntropyRLGR3} {
		u16(rfxVersion10)
		u16(rfxTile64x64)
		b = append(b, 0x00) // flags
		b = append(b, rfxColConvICT)
		b = append(b, rfxXformDWT53A)
		b = append(b, entropy)
	}
	return b
}

// Type reports that this is the bitmap codecs capability set.
func (*BitmapCodecsCapability) Type() CapsType {
	return CAPSETTYPE_BITMAP_CODECS
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/fc05c385-46c3-42cb-9ed2-c475a3990e0b
// BitmapCacheHostSupportCapability is the bitmap cache host support capability, which reports the cache revision the client uses.
//
// See MS-RDPBCGR 2.2.7.1.
type BitmapCacheHostSupportCapability struct {
	CacheVersion uint8
	Pad1         uint8
	Pad2         uint16
}

// Type reports that this is the bitmap cache host support capability set.
func (*BitmapCacheHostSupportCapability) Type() CapsType {
	return CAPSTYPE_BITMAPCACHE_HOSTSUPPORT
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/41323437-c753-460e-8108-495a6fdd68a8
// LargePointerCapability is the large pointer capability, which says whether the client can receive pointers larger than the 32 by 32 of the older format.
//
// See MS-RDPBCGR 2.2.7.1.
type LargePointerCapability struct {
	SupportFlags uint16 `struc:"little"`
}

// Type reports that this is the large pointer capability set.
func (*LargePointerCapability) Type() CapsType {
	return CAPSETTYPE_LARGE_POINTER
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdperp/36a25e21-25e1-4954-aae8-09aaf6715c79
// RemoteProgramsCapability is the RemoteApp capability, which gives the RAIL support level.
//
// See MS-RDPBCGR 2.2.7.1.
type RemoteProgramsCapability struct {
	RailSupportLevel uint32 `struc:"little"`
}

// Type reports that this is the RemoteApp capability set.
func (*RemoteProgramsCapability) Type() CapsType {
	return CAPSTYPE_RAIL
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdperp/82ec7a69-f7e3-4294-830d-666178b35d15
// WindowListCapability is the window list capability, which advertises the window list and its icon caches.
//
// See MS-RDPBCGR 2.2.7.1.
type WindowListCapability struct {
	WndSupportLevel     uint32 `struc:"little"`
	NumIconCaches       uint8
	NumIconCacheEntries uint16 `struc:"little"`
}

// Type reports that this is the window list capability set.
func (*WindowListCapability) Type() CapsType {
	return CAPSTYPE_WINDOW
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/9132002f-f133-4a0f-ba2f-2dc48f1e7f93
// DesktopCompositionCapability is the desktop composition capability, which says whether the client can receive a composed desktop.
//
// See MS-RDPBCGR 2.2.7.1.
type DesktopCompositionCapability struct {
	CompDeskSupportLevel uint16 `struc:"little"`
}

// Type reports that this is the desktop composition capability set.
func (*DesktopCompositionCapability) Type() CapsType {
	return CAPSETTYPE_COMPDESK
}

// see https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/aa953018-c0a8-4761-bb12-86586c2cd56a
// SurfaceCommandsCapability is the surface commands capability, which advertises which surface commands the client accepts. The graphics channel is not offered without this.
//
// See MS-RDPBCGR 2.2.7.1.
type SurfaceCommandsCapability struct {
	CmdFlags uint32 `struc:"little"`
	Reserved uint32 `struc:"little"`
}

// Type reports that this is the surface commands capability set.
func (*SurfaceCommandsCapability) Type() CapsType {
	return CAPSETTYPE_SURFACE_COMMANDS
}

// FrameAcknowledgeCapability is the frame acknowledge capability, which says the client will acknowledge frames and how many it expects.
//
// See MS-RDPBCGR 2.2.7.1.
type FrameAcknowledgeCapability struct {
	FrameCount uint32 `struc:"little"`
}

// Type reports that this is the frame acknowledge capability set.
func (*FrameAcknowledgeCapability) Type() CapsType {
	return CAPSSETTYPE_FRAME_ACKNOWLEDGE
}

// DrawNineGridCapability is the nine grid cache capability, which advertises the nine grid cache.
//
// See MS-RDPBCGR 2.2.7.1.
type DrawNineGridCapability struct {
	SupportLevel uint32 `struc:"little"`
	CacheSize    uint16 `struc:"little"`
	CacheEntries uint16 `struc:"little"`
}

// Type reports that this is the nine grid cache capability set.
func (*DrawNineGridCapability) Type() CapsType {
	return CAPSTYPE_DRAWNINEGRIDCACHE
}

func readCapability(r io.Reader) (Capability, error) {
	capType, err := core.ReadUint16LE(r)
	if err != nil {
		return nil, err
	}
	capLen, err := core.ReadUint16LE(r)
	if err != nil {
		return nil, err
	}
	if int(capLen) < 4 {
		return nil, errors.New(fmt.Sprintf("Capability length expected %d", capLen))
	}
	if int(capLen) == 4 {
		glog.Debugf("skipping empty capability type 0x%04x", capType)
		return nil, nil
	}

	capBytes, err := core.ReadBytes(int(capLen)-4, r)
	if err != nil {
		return nil, err
	}
	capReader := bytes.NewReader(capBytes)
	var c Capability
	glog.Debugf("Capability type 0x%04x", capType)
	switch CapsType(capType) {
	case CAPSTYPE_GENERAL:
		c = &GeneralCapability{}
	case CAPSTYPE_BITMAP:
		c = &BitmapCapability{}
	case CAPSTYPE_ORDER:
		c = &OrderCapability{}
	case CAPSTYPE_BITMAPCACHE:
		c = &BitmapCacheCapability{}
	case CAPSTYPE_POINTER:
		c = &PointerCapability{}
	case CAPSTYPE_INPUT:
		c = &InputCapability{}
	case CAPSTYPE_BRUSH:
		c = &BrushCapability{}
	case CAPSTYPE_GLYPHCACHE:
		c = &GlyphCapability{}
	case CAPSTYPE_OFFSCREENCACHE:
		c = &OffscreenBitmapCacheCapability{}
	case CAPSTYPE_VIRTUALCHANNEL:
		c = &VirtualChannelCapability{}
	case CAPSTYPE_SOUND:
		c = &SoundCapability{}
	case CAPSTYPE_CONTROL:
		c = &ControlCapability{}
	case CAPSTYPE_ACTIVATION:
		c = &WindowActivationCapability{}
	case CAPSTYPE_FONT:
		c = &FontCapability{}
	case CAPSTYPE_COLORCACHE:
		c = &ColorCacheCapability{}
	case CAPSTYPE_SHARE:
		c = &ShareCapability{}
	case CAPSETTYPE_MULTIFRAGMENTUPDATE:
		c = &MultiFragmentUpdate{}
	case CAPSTYPE_DRAWGDIPLUS:
		c = &DrawGDIPlusCapability{}
	case CAPSETTYPE_BITMAP_CODECS:
		c = &BitmapCodecsCapability{}
	case CAPSTYPE_BITMAPCACHE_HOSTSUPPORT:
		c = &BitmapCacheHostSupportCapability{}
	case CAPSETTYPE_LARGE_POINTER:
		c = &LargePointerCapability{}
	case CAPSTYPE_RAIL:
		c = &RemoteProgramsCapability{}
	case CAPSTYPE_WINDOW:
		c = &WindowListCapability{}
	case CAPSETTYPE_COMPDESK:
		c = &DesktopCompositionCapability{}
	case CAPSETTYPE_SURFACE_COMMANDS:
		c = &SurfaceCommandsCapability{}
	case CAPSSETTYPE_FRAME_ACKNOWLEDGE:
		c = &FrameAcknowledgeCapability{}
	default:
		// Unknown capability sets are skipped; the length field has already
		// been consumed so the surrounding parser stays in sync.
		glog.Debugf("skipping unsupported capability type 0x%04x", capType)
		return nil, nil
	}
	if err := struc.Unpack(capReader, c); err != nil {
		glog.Error("Capability unpack error", err, fmt.Sprintf("0x%04x", capType), glog.Hex(capBytes))
		return nil, err
	}
	glog.Debugf("Capability<%s>: %+v", c.Type(), c)
	return c, nil
}
