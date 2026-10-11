package ber

import (
	"errors"
	"fmt"
	"io"

	"github.com/adfnekc/grdp/core"
)

// The class of a tag, in the top two bits of its identifier octet. Universal
// means the tag is defined by the encoding rules themselves, and application
// means it belongs to the protocol using them, which for MCS is T.125's own tags.
const (
	CLASS_MASK uint8 = 0xC0
	CLASS_UNIV       = 0x00
	CLASS_APPL       = 0x40
	CLASS_CTXT       = 0x80
	CLASS_PRIV       = 0xC0
)

// Whether a value is primitive or constructed, in bit 6 of the identifier octet.
// A constructed tag is followed by its members rather than by its bytes, which is
// what makes a sequence a sequence.
const (
	PC_MASK      uint8 = 0x20
	PC_PRIMITIVE       = 0x00
	PC_CONSTRUCT       = 0x20
)

// The tag number, in the low five bits of the identifier octet. Tags above thirty
// do not fit and use a second octet, which is what ReadApplicationTag and
// WriteApplicationTag handle.
//
// TAG_SEQUENCE and TAG_SEQUENCE_OF are the same value and the same encoding, so
// two names exist only because the specifications name both.
const (
	TAG_MASK            uint8 = 0x1F
	TAG_BOOLEAN               = 0x01
	TAG_INTEGER               = 0x02
	TAG_BIT_STRING            = 0x03
	TAG_OCTET_STRING          = 0x04
	TAG_OBJECT_IDENFIER       = 0x06
	TAG_ENUMERATED            = 0x0A
	TAG_SEQUENCE              = 0x10
	TAG_SEQUENCE_OF           = 0x10
)

func berPC(pc bool) uint8 {
	if pc {
		return PC_CONSTRUCT
	}
	return PC_PRIMITIVE
}

// ReadEnumerated reads an ENUMERATED, which in DER is always one byte. A length
// other than one is rejected rather than read as though it were, because an
// enumerated with the wrong length means the stream is out of step.
func ReadEnumerated(r io.Reader) (uint8, error) {
	if !ReadUniversalTag(TAG_ENUMERATED, false, r) {
		return 0, errors.New("invalid ber tag")
	}
	length, err := ReadLength(r)
	if err != nil {
		return 0, err
	}
	if length != 1 {
		return 0, errors.New(fmt.Sprintf("enumerate size is wrong, get %v, expect 1", length))
	}
	return core.ReadUInt8(r)
}

// WriteEnumerated writes an ENUMERATED as one byte.
func WriteEnumerated(e uint8, w io.Writer) {
	WriteUniversalTag(TAG_ENUMERATED, false, w)
	WriteLength(1, w)
	core.WriteUInt8(e, w)
}

// ReadUniversalTag reads an identifier octet and reports whether it is the
// universal tag asked for, of the asked for primitive or constructed form. It
// consumes the octet either way, so a caller that gets false must abandon the
// message rather than try another tag: the octet has been read.
func ReadUniversalTag(tag uint8, pc bool, r io.Reader) bool {
	bb, _ := core.ReadUInt8(r)
	return bb == (CLASS_UNIV|berPC(pc))|(TAG_MASK&tag)
}

// WriteUniversalTag writes the identifier octet of a universal tag.
func WriteUniversalTag(tag uint8, pc bool, w io.Writer) {
	core.WriteUInt8((CLASS_UNIV|berPC(pc))|(TAG_MASK&tag), w)
}

// ReadLength reads a DER length, which is one byte below 128 and a count
// followed by that many bytes at or above it.
//
// Only one and two byte lengths are accepted, which is what CredSSP needs: a
// message longer than 65535 bytes is not something this layer should be reading
// anyway, and a length field that says otherwise is treated as a malformed
// message rather than as a reason to allocate. The read error on the first octet
// is not reported, because a short read has already ended the message.
func ReadLength(r io.Reader) (int, error) {
	ret := 0
	size, _ := core.ReadUInt8(r)
	if size&0x80 > 0 {
		size = size &^ 0x80
		if size == 1 {
			r, err := core.ReadUInt8(r)
			if err != nil {
				return 0, err
			}
			ret = int(r)
		} else if size == 2 {
			r, err := core.ReadUint16BE(r)
			if err != nil {
				return 0, err
			}
			ret = int(r)
		} else {
			return 0, errors.New("BER length may be 1 or 2")
		}
	} else {
		ret = int(size)
	}
	return ret, nil
}

// WriteLength writes a DER length, using the long form once the value no longer
// fits in the short one.
func WriteLength(size int, w io.Writer) {
	if size > 0x7f {
		core.WriteUInt8(0x82, w)
		core.WriteUInt16BE(uint16(size), w)
	} else {
		core.WriteUInt8(uint8(size), w)
	}
}

// ReadInteger reads an INTEGER of one, two, three or four bytes big endian.
//
// Three bytes is not a size DER produces and the specification does not have: it
// is here because a three byte integer is what some producers write, and the
// value is assembled most significant byte first as though the bytes were a four
// byte integer missing its first. Any other size is refused rather than guessed
// at.
func ReadInteger(r io.Reader) (int, error) {
	if !ReadUniversalTag(TAG_INTEGER, false, r) {
		return 0, errors.New("Bad integer tag")
	}
	size, _ := ReadLength(r)
	switch size {
	case 1:
		num, _ := core.ReadUInt8(r)
		return int(num), nil
	case 2:
		num, _ := core.ReadUint16BE(r)
		return int(num), nil
	case 3:
		integer1, _ := core.ReadUInt8(r)
		integer2, _ := core.ReadUint16BE(r)
		return int(integer2) + int(integer1)<<16, nil
	case 4:
		num, _ := core.ReadUInt32BE(r)
		return int(num), nil
	default:
		return 0, errors.New("wrong size")
	}
}

// WriteInteger writes an INTEGER, choosing one, two or four bytes by the size of
// the value.
//
// A negative value is written as its four byte two's complement, which is what an
// INTEGER means in BER: the encoding is signed and a reader sign extends it. That
// is why a negative value takes four bytes rather than the one byte its magnitude
// would: the negative numbers that fit in one byte are not the ones a caller sends.
//
// The width is chosen by value rather than being the shortest encoding of the
// value, so this is not minimal DER: a positive value at or above 0x80 is written
// without the leading zero byte that DER requires to keep it positive, and a value
// at or above 0x100 is written in two bytes even though that is the minimum. What
// it produces is what the RDP connection sequence reads, and changing it to be
// minimal would change the bytes on the wire for every domain parameter.
func WriteInteger(n int, w io.Writer) {
	WriteUniversalTag(TAG_INTEGER, false, w)
	switch {
	case n < 0:
		WriteLength(4, w)
		core.WriteUInt32BE(uint32(int32(n)), w)
	case n <= 0xff:
		WriteLength(1, w)
		core.WriteUInt8(uint8(n), w)
	case n <= 0xffff:
		WriteLength(2, w)
		core.WriteUInt16BE(uint16(n), w)
	default:
		WriteLength(4, w)
		core.WriteUInt32BE(uint32(n), w)
	}
}

// WriteOctetstring writes an OCTET STRING.
func WriteOctetstring(str string, w io.Writer) {
	WriteUniversalTag(TAG_OCTET_STRING, false, w)
	WriteLength(len(str), w)
	core.WriteBytes([]byte(str), w)
}

// ReadOctetstring reads an OCTET STRING and returns its bytes. The tag is checked
// first, so a caller that gets an error should treat the stream as unusable: the
// read has begun.
func ReadOctetstring(r io.Reader) ([]byte, error) {
	if !ReadUniversalTag(TAG_OCTET_STRING, false, r) {
		return nil, errors.New("Bad string tag")
	}
	size, err := ReadLength(r)
	if err != nil {
		return nil, err
	}
	return core.ReadBytes(size, r)
}

// WriteBoolean writes a BOOLEAN. DER has exactly one encoding for true, which is
// 0xff, and this writes it: a 0x01 would be BER and would change the bytes a hash
// is computed over.
func WriteBoolean(b bool, w io.Writer) {
	bb := uint8(0)
	if b {
		bb = uint8(0xff)
	}
	WriteUniversalTag(TAG_BOOLEAN, false, w)
	WriteLength(1, w)
	core.WriteUInt8(bb, w)
}

// ReadBoolean reads a BOOLEAN. DER has one encoding for each value, 0xff for true
// and 0x00 for false, and both others that BER allows are rejected rather than
// read as false: a boolean that is neither is a message that has been misread, and
// 0x01 in particular is what BER permits and DER does not.
func ReadBoolean(r io.Reader) (bool, error) {
	if !ReadUniversalTag(TAG_BOOLEAN, false, r) {
		return false, errors.New("Bad bool tag")
	}
	ReadLength(r)
	bb, err := core.ReadByte(r)
	if err != nil {
		return false, err
	}
	if bb == 0xff {
		return true, nil
	} else if bb == 0x00 {
		return false, nil
	}
	return false, fmt.Errorf("Bad bool value: %X", bb)
}

// ReadApplicationTag reads an application tag's identifier octet and returns the
// length that follows it.
//
// Tags above thirty do not fit in the five bits available, so they are written as
// a full five bits of ones followed by another octet holding the number. That is
// why there are two forms here, and why the small form is the one that checks the
// tag in the same octet.
func ReadApplicationTag(tag uint8, r io.Reader) (int, error) {
	bb, _ := core.ReadUInt8(r)
	if tag > 30 {
		if bb != (CLASS_APPL|PC_CONSTRUCT)|TAG_MASK {
			return 0, errors.New("ReadApplicationTag invalid data")
		}
		bb, _ := core.ReadUInt8(r)
		if bb != tag {
			return 0, errors.New("ReadApplicationTag bad tag")
		}
	} else {
		if bb != (CLASS_APPL|PC_CONSTRUCT)|(TAG_MASK&tag) {
			return 0, errors.New("ReadApplicationTag invalid data2")
		}
	}
	return ReadLength(r)
}

// WriteApplicationTag writes an application tag and the length of what follows,
// using the two octet form for tags above thirty. MCS uses that form, so it is the
// path that is actually exercised.
func WriteApplicationTag(tag uint8, size int, w io.Writer) {
	if tag > 30 {
		core.WriteUInt8((CLASS_APPL|PC_CONSTRUCT)|TAG_MASK, w)
		core.WriteUInt8(tag, w)
		WriteLength(size, w)
	} else {
		core.WriteUInt8((CLASS_APPL|PC_CONSTRUCT)|(TAG_MASK&tag), w)
		WriteLength(size, w)
	}
}

// WriteEncodedDomainParams writes data that is already encoded as a SEQUENCE of
// domain parameters, which the public key exchange in CredSSP carries. The inner
// encoding is the caller's, since this layer does not know what the parameters
// are; only the outer tag and length are written here.
func WriteEncodedDomainParams(data []byte, w io.Writer) {
	WriteUniversalTag(TAG_SEQUENCE, true, w)
	WriteLength(len(data), w)
	core.WriteBytes(data, w)
}
