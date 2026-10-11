package per

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/adfnekc/grdp/glog"

	"github.com/adfnekc/grdp/core"
)

// The minimum lengths of the two string types MCS uses, which are subtracted from
// a length before it goes on the wire and added back when it is read: a PER length
// is a count above the minimum the type allows, not the size itself. Getting that
// the wrong way round shifts every string by the minimum.
const (
	MinSimpleNumericStringLen       = 1
	MinH221NonStandardIdentifierLen = 4
)

// WriteInteger writes an unsigned integer, choosing the shortest of one, two and
// four bytes that holds it. Unlike the BER integer beside it, this one writes no
// tag: PER lengths and counts sit in the message with no type in front of them.
func WriteInteger(n int, w io.Writer) {
	if n <= 0xff {
		WriteLength(1, w)
		core.WriteUInt8(uint8(n), w)
	} else if n <= 0xffff {
		WriteLength(2, w)
		core.WriteUInt16BE(uint16(n), w)
	} else {
		WriteLength(4, w)
		core.WriteUInt32BE(uint32(n), w)
	}
}

// ReadInteger16 reads a two byte big endian integer.
func ReadInteger16(r io.Reader) (uint16, error) {
	return core.ReadUint16BE(r)
}

// WriteInteger16 writes a two byte big endian integer.
func WriteInteger16(value uint16, w io.Writer) {
	core.WriteUInt16BE(value, w)
}

/**
 * @param choice {integer}
 * @returns {type.UInt8} choice per encoded
 */
func WriteChoice(choice uint8, w io.Writer) {
	core.WriteUInt8(choice, w)
}

/**
 * @param value {raw} value to convert to per format
 * @returns type objects per encoding value
 */
// WriteLength writes a PER length: one byte below 128, and above it two bytes
// with the top bit set to mark the length as two bytes wide.
//
// The value is not reduced by the type's minimum here. Two bytes are used as soon
// as the length exceeds 127, which is where the top bit would otherwise be taken
// for the mark.
func WriteLength(value int, w io.Writer) {
	if value > 0x7f {
		core.WriteUInt16BE(uint16(value|0x8000), w)
	} else {
		core.WriteUInt8(uint8(value), w)
	}
}

// ReadLength reads a PER length, which is one byte, or two bytes with the top bit
// of the first set.
//
// The error on the first byte is swallowed and zero returned, which makes a short
// read look like an empty string. That is how the callers treat it, and the
// message has already ended by then.
func ReadLength(r io.Reader) (uint16, error) {
	b, err := core.ReadUInt8(r)
	if err != nil {
		return 0, nil
	}
	var size uint16
	if b&0x80 > 0 {
		b = b &^ 0x80
		size = uint16(b) << 8
		left, _ := core.ReadUInt8(r)
		size += uint16(left)
	} else {
		size = uint16(b)
	}
	return size, nil
}

/**
 * @param oid {array} oid to write
 * @returns {type.Component} per encoded object identifier
 */
// WriteObjectIdentifier writes the object identifier MCS connects with,
// 0.0.20.127.0.0.1, which is encoded as five bytes of content behind a length of
// five.
//
// The first byte is the first two components combined as oid[0]*40+oid[1], and the
// bytes after it are one octet per component. Only identifiers whose first two
// components are both below 40 encode this way, which is every identifier this
// library writes and not every identifier there is: a larger component needs the
// multi-byte base-128 form, which is not implemented here.
//
// This used to combine the two components with a bitwise AND, which agrees with
// the arithmetic only when both are zero. That is true of this identifier, which
// is why it worked, and it is why the fault was invisible.
func WriteObjectIdentifier(oid []byte, w io.Writer) {
	core.WriteUInt8(5, w)
	core.WriteByte(byte(oid[0]*40+oid[1])&0xff, w)
	core.WriteByte(oid[2], w)
	core.WriteByte(oid[3], w)
	core.WriteByte(oid[4], w)
	core.WriteByte(oid[5], w)
}

/**
 * @param selection {integer}
 * @returns {type.UInt8} per encoded selection
 */
func WriteSelection(selection uint8, w io.Writer) {
	core.WriteUInt8(selection, w)
}

// WriteNumericString writes a numeric string, two decimal digits to a byte, each
// digit reduced modulo ten and offset by '0' back to a nibble. An odd length pads
// the last byte with a zero digit rather than failing.
//
// The length that goes on the wire is the digit count less the minimum for the
// type, which is why the caller passes that minimum in.
func WriteNumericString(s string, minValue int, w io.Writer) {
	length := len(s)
	mLength := minValue
	if length >= minValue {
		mLength = length - minValue
	}
	buff := &bytes.Buffer{}
	for i := 0; i < length; i += 2 {
		c1 := int(s[i])
		c2 := 0x30
		if i+1 < length {
			c2 = int(s[i+1])
		}
		c1 = (c1 - 0x30) % 10
		c2 = (c2 - 0x30) % 10
		core.WriteUInt8(uint8((c1<<4)|c2), buff)
	}
	WriteLength(mLength, w)
	w.Write(buff.Bytes())
}

// ReadNumericString reads a numeric string and returns it as digits. The count on
// the wire is a length above the minimum, so the minimum is added back, and each
// byte becomes two characters because that is how the digits are packed.
//
// A read failure returns the empty string rather than an error, so a truncated
// message is indistinguishable from an empty string to a caller.
func ReadNumericString(minValue int, r io.Reader) string {
	mLength, err := ReadLength(r)
	if err != nil {
		return ""
	}
	nBytes := minValue + int(mLength)
	buf, err := core.ReadBytes(nBytes, r)
	if err != nil {
		return ""
	}
	var s strings.Builder
	for i := 0; i < nBytes; i++ {
		b := buf[i]
		c1 := (b >> 4) & 0x0f
		c2 := b & 0x0f
		s.WriteByte(byte(c1 + '0'))
		s.WriteByte(byte(c2 + '0'))
	}
	return s.String()
}

// WritePadding writes length zero bytes, which a PER encoding uses to fill an
// alignment it has promised.
func WritePadding(length int, w io.Writer) {
	for i := 0; i < length; i++ {
		core.WriteUInt8(uint8(0), w)
	}

}

// WriteNumberOfSet writes the count of a set of items as one byte.
func WriteNumberOfSet(n int, w io.Writer) {
	core.WriteUInt8(uint8(n), w)
}

/**
 * @param oStr {String}
 * @param minValue {integer} default 0
 * @returns {type.Component} per encoded octet stream
 */
func WriteOctetStream(oStr string, minValue int, w io.Writer) {
	length := len(oStr)
	mlength := minValue

	if length-minValue >= 0 {
		mlength = length - minValue
	}
	WriteLength(mlength, w)
	w.Write([]byte(oStr)[:length])
}

// ReadChoice reads a choice as one byte. A read failure returns zero, which is a
// valid choice in several of the messages this is used for, so a truncated read is
// not distinguishable from a choice of zero here.
func ReadChoice(r io.Reader) uint8 {
	choice, _ := core.ReadUInt8(r)
	return choice
}

// ReadEnumerates reads an enumerated value as one byte.
func ReadEnumerates(r io.Reader) (uint8, error) {
	return core.ReadUInt8(r)
}

// ReadSelection reads a selection as one byte.
func ReadSelection(r io.Reader) (uint8, error) {
	return core.ReadUInt8(r)
}

// ReadPadding reads one byte of padding and returns it, which is what a PER
// encoding with an alignment the sender chose not to use looks like.
func ReadPadding(r io.Reader) (uint8, error) {
	return core.ReadUInt8(r)
}

// ReadNumberOfSet reads the count of a set of items, as one byte.
func ReadNumberOfSet(r io.Reader) uint8 {
	choice, _ := core.ReadUInt8(r)
	return choice
}

// ReadInteger reads a length prefixed integer of one, two or four bytes.
//
// Any other length returns zero, and the length is logged rather than reported:
// the callers of this are reading counts and identifiers inside a message they
// have already decided to accept, so the return value has no error to carry.
func ReadInteger(r io.Reader) uint32 {
	size, _ := ReadLength(r)
	switch size {
	case 1:
		ret, _ := core.ReadUInt8(r)
		return uint32(ret)
	case 2:
		ret, _ := core.ReadUint16BE(r)
		return uint32(ret)
	case 4:
		ret, _ := core.ReadUInt32BE(r)
		return ret
	default:
		glog.Trace("ReadInteger", size)
	}
	return 0
}

// MatchObjectIdentifier reads an object identifier and reports whether it is the
// one given, consuming it either way. It is how the MCS connect exchange checks
// that the server is speaking T.124, and the length of five is part of the check:
// the identifier this understands is always five bytes of content.
func MatchObjectIdentifier(r io.Reader, oid []byte) bool {
	size, _ := ReadLength(r)
	if size != 5 {
		return false
	}

	a_oid := []byte{0, 0, 0, 0, 0, 0}
	t12, _ := core.ReadByte(r)
	a_oid[0] = t12 >> 4
	a_oid[1] = t12 & 0x0f
	a_oid[2], _ = core.ReadByte(r)
	a_oid[3], _ = core.ReadByte(r)
	a_oid[4], _ = core.ReadByte(r)
	a_oid[5], _ = core.ReadByte(r)

	for i := range oid {
		if oid[i] != a_oid[i] {
			return false
		}
	}
	return true
}

// MatchOctetStream reads an octet stream and reports whether it is the string
// given.
//
// The length on the wire is a count above the type's minimum, so the minimum is
// added back before the comparison. The comparison is byte by byte, so the stream
// is consumed even when it does not match, and a caller that gets false must treat
// the message as abandoned rather than trying another value.
//
// This was called MacthOctetStream, which was a misspelling of the same function
// and was exported, so the rename is a breaking change.
func MatchOctetStream(r io.Reader, s string, min int) bool {
	ln, _ := ReadLength(r)
	size := int(ln) + min
	if size != len(s) {
		return false
	}
	for i := 0; i < size; i++ {
		b, _ := core.ReadByte(r)
		if b != s[i] {
			return false
		}
	}

	return true
}

// ReadOctetStream reads an octet stream and returns the part of it after the
// first min bytes, because the minimum is the part the caller has already matched
// against a known value and does not need back.
func ReadOctetStream(r io.Reader, min int) (string, error) {
	ln, err := ReadLength(r)
	if err != nil {
		return "", err
	}
	size := int(ln) + min
	buf, err := core.ReadBytes(size, r)
	if err != nil {
		return "", err
	}
	return string(buf[min:]), nil
}

// ReadExcept reads one byte and reports whether it is the expected value.
//
// An unexpected byte means the stream has been misread and every later field
// would be nonsense, so a caller must treat the error as fatal to the message and
// not as something to skip past. It returns an error rather than panicking: this
// parses bytes that arrived over a network, and a parser that can end the process
// on hostile input is not a parser this library should have.
func ReadExcept(b byte, r io.Reader) error {
	u, err := core.ReadUInt8(r)
	if err != nil {
		return fmt.Errorf("per: expected %#02x, could not read: %w", b, err)
	}
	if byte(u) != b {
		return fmt.Errorf("per: expected %#02x, read %#02x", b, u)
	}
	return nil
}
