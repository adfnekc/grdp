package per

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/pkg/errors"
	"github.com/tomatome/grdp/glog"

	"github.com/tomatome/grdp/core"
)

const (
	MinSimpleNumericStringLen       = 1
	MinH221NonStandardIdentifierLen = 4
)

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

func ReadInteger16(r io.Reader) (uint16, error) {
	return core.ReadUint16BE(r)
}

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
func WriteLength(value int, w io.Writer) {
	if value > 0x7f {
		core.WriteUInt16BE(uint16(value|0x8000), w)
	} else {
		core.WriteUInt8(uint8(value), w)
	}
}

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
func WriteObjectIdentifier(oid []byte, w io.Writer) {
	core.WriteUInt8(5, w)
	core.WriteByte((oid[0]<<4)&(oid[1]&0x0f), w)
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

func WritePadding(length int, w io.Writer) {
	for i := 0; i < length; i++ {
		core.WriteUInt8(uint8(0), w)
	}

}

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

func ReadChoice(r io.Reader) uint8 {
	choice, _ := core.ReadUInt8(r)
	return choice
}

func ReadEnumerates(r io.Reader) (uint8, error) {
	return core.ReadUInt8(r)
}

func ReadSelection(r io.Reader) (uint8, error) {
	return core.ReadUInt8(r)
}

func ReadPadding(r io.Reader) (uint8, error) {
	return core.ReadUInt8(r)
}

func ReadNumberOfSet(r io.Reader) uint8 {
	choice, _ := core.ReadUInt8(r)
	return choice
}
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
func MacthOctetStream(r io.Reader, s string, min int) bool {
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

func ReadExcept(b byte, r io.Reader) {
	u, err := core.ReadUInt8(r)
	if err != nil {
		panic(fmt.Errorf("except %X,but read err", b))
	}
	if byte(u) != b {
		panic(errors.WithStack(fmt.Errorf("except %X,but read %X", b, u)))
	}
}
