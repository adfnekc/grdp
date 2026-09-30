package core

import (
	"encoding/binary"
	"errors"
	"io"
)

// ReadBytesComplete is the callback a reader uses to hand back a completed
// message along with any error.
type ReadBytesComplete func(result []byte, err error)

// StartReadBytes reads exactly length bytes from r in a background goroutine
// and then invokes cb(result, err) exactly once.
//
// On any error - including io.EOF and io.ErrUnexpectedEOF - cb is invoked with
// that error and no further reads are scheduled by this function. Callers are
// responsible for stopping their own read chain and surfacing the error.
//
// NOTE: this used to loop forever on io.EOF, spinning the CPU and leaking the
// goroutine whenever the peer closed the connection.
func StartReadBytes(length int, r io.Reader, cb ReadBytesComplete) {
	if length < 0 {
		cb(nil, errors.New("core: negative read length"))
		return
	}
	if cb == nil {
		return
	}
	b := make([]byte, length)
	go func() {
		_, err := io.ReadFull(r, b)
		cb(b, err)
	}()
}

// ReadBytes reads exactly len bytes. On a short read it returns the bytes that
// were read together with io.ErrUnexpectedEOF (or io.EOF when none were read).
func ReadBytes(length int, r io.Reader) ([]byte, error) {
	if length < 0 {
		return nil, errors.New("core: negative read length")
	}
	b := make([]byte, length)
	n, err := io.ReadFull(r, b)
	if err != nil {
		if err == io.EOF && n > 0 {
			err = io.ErrUnexpectedEOF
		}
		return b[:n], err
	}
	return b, nil
}

// ReadByte reads one byte.
func ReadByte(r io.Reader) (byte, error) {
	b, err := ReadBytes(1, r)
	if err != nil || len(b) == 0 {
		return 0, err
	}
	return b[0], nil
}

// ReadUInt8 reads one unsigned byte.
func ReadUInt8(r io.Reader) (uint8, error) {
	b, err := ReadByte(r)
	return uint8(b), err
}

// ReadUint16LE reads a little endian 16 bit unsigned integer.
func ReadUint16LE(r io.Reader) (uint16, error) {
	b, err := ReadBytes(2, r)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// ReadUint16BE reads a big endian 16 bit unsigned integer.
func ReadUint16BE(r io.Reader) (uint16, error) {
	b, err := ReadBytes(2, r)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

// ReadUInt32LE reads a little endian 32 bit unsigned integer.
func ReadUInt32LE(r io.Reader) (uint32, error) {
	b, err := ReadBytes(4, r)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// ReadUInt32BE reads a big endian 32 bit unsigned integer.
func ReadUInt32BE(r io.Reader) (uint32, error) {
	b, err := ReadBytes(4, r)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// WriteByte writes one byte and reports how many it wrote.
func WriteByte(data byte, w io.Writer) (int, error) {
	b := make([]byte, 1)
	b[0] = byte(data)
	return w.Write(b)
}

// WriteBytes writes a slice and reports how many bytes it wrote.
func WriteBytes(data []byte, w io.Writer) (int, error) {
	return w.Write(data)
}

// WriteUInt8 writes one unsigned byte.
func WriteUInt8(data uint8, w io.Writer) (int, error) {
	b := make([]byte, 1)
	b[0] = byte(data)
	return w.Write(b)
}

// WriteUInt16BE writes a big endian 16 bit unsigned integer.
func WriteUInt16BE(data uint16, w io.Writer) (int, error) {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, data)
	return w.Write(b)
}

// WriteUInt16LE writes a little endian 16 bit unsigned integer.
func WriteUInt16LE(data uint16, w io.Writer) (int, error) {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, data)
	return w.Write(b)
}

// WriteUInt32LE writes a little endian 32 bit unsigned integer.
func WriteUInt32LE(data uint32, w io.Writer) (int, error) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, data)
	return w.Write(b)
}

// WriteUInt32BE writes a big endian 32 bit unsigned integer.
func WriteUInt32BE(data uint32, w io.Writer) (int, error) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, data)
	return w.Write(b)
}

// PutUint16BE splits a value into its two big endian bytes.
func PutUint16BE(data uint16) (uint8, uint8) {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, data)
	return uint8(b[0]), uint8(b[1])
}

// Uint16BE joins two big endian bytes into a 16 bit value.
func Uint16BE(d0, d1 uint8) uint16 {
	b := make([]byte, 2)
	b[0] = d0
	b[1] = d1

	return binary.BigEndian.Uint16(b)
}

// RGB565ToRGB expands a 5-6-5 packed colour to eight bits per channel. The
// parentheses are load bearing for the reader rather than for the parser: & and
// >> have the same precedence and associate left, so the unparenthesised form
// already means (data & mask) >> shift. That is not obvious, and an obvious
// looking "fix" of it breaks the colours. The expansion truncates, so full scale
// comes out as 248 rather than 255.
func RGB565ToRGB(data uint16) (r, g, b uint8) {
	r = uint8((data & 0xF800) >> 8)
	g = uint8((data & 0x07E0) >> 3)
	b = uint8((data & 0x001F) << 3)

	return
}

// RGB555ToRGB expands a 5-5-5 packed colour to eight bits per channel, with the
// same truncating expansion and the same load bearing parentheses as
// RGB565ToRGB.
func RGB555ToRGB(data uint16) (r, g, b uint8) {
	r = uint8((data & 0x7C00) >> 7)
	g = uint8((data & 0x03E0) >> 2)
	b = uint8((data & 0x001F) << 3)

	return
}
