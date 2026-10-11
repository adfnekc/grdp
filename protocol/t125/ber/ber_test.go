package ber

import (
	"bytes"
	"testing"
)

// A negative INTEGER used to take the one byte branch, because the comparison that
// chose the width was a comparison on an int and every negative number is less
// than 0xff. What went on the wire was the low byte of the value, so -1 was written
// as 0x01: a positive number, and a wrong one.
func TestWriteIntegerNegative(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []byte
	}{
		// The tag, the length, then four bytes of two's complement, which is what
		// a BER reader sign extends.
		{-1, []byte{0x02, 0x04, 0xff, 0xff, 0xff, 0xff}},
		{-120, []byte{0x02, 0x04, 0xff, 0xff, 0xff, 0x88}},
		{-256, []byte{0x02, 0x04, 0xff, 0xff, 0xff, 0x00}},
	} {
		var buf bytes.Buffer
		WriteInteger(tc.n, &buf)
		if !bytes.Equal(buf.Bytes(), tc.want) {
			t.Errorf("WriteInteger(%d) wrote % X, want % X", tc.n, buf.Bytes(), tc.want)
		}
	}
}

// The positive encodings must not move: they are what the connection sequence
// reads, and the width is chosen by value rather than being the shortest encoding,
// so a change here would change the bytes on the wire for every domain parameter.
func TestWriteIntegerPositiveIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []byte
	}{
		{0, []byte{0x02, 0x01, 0x00}},
		{34, []byte{0x02, 0x01, 0x22}},
		{0xff, []byte{0x02, 0x01, 0xff}},
		{0x100, []byte{0x02, 0x02, 0x01, 0x00}},
		{0xffff, []byte{0x02, 0x02, 0xff, 0xff}},
		{0x10000, []byte{0x02, 0x04, 0x00, 0x01, 0x00, 0x00}},
	} {
		var buf bytes.Buffer
		WriteInteger(tc.n, &buf)
		if !bytes.Equal(buf.Bytes(), tc.want) {
			t.Errorf("WriteInteger(%d) wrote % X, want % X", tc.n, buf.Bytes(), tc.want)
		}
	}
}

// A round trip through ReadInteger is what says the encoding is the one the reader
// on the other side of this package understands, rather than only self-consistent.
func TestIntegerRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 127, 255, 256, 65535} {
		var buf bytes.Buffer
		WriteInteger(n, &buf)
		got, err := ReadInteger(&buf)
		if err != nil {
			t.Errorf("ReadInteger of %d: %v", n, err)
			continue
		}
		if got != n {
			t.Errorf("round trip of %d gave %d", n, got)
		}
	}
}

func TestReadBooleanRejectsWhatDERDoesNot(t *testing.T) {
	// DER has one encoding for each value. 0x01 is true in BER and not in DER,
	// and reading it as false would be a quiet mistake rather than a loud one.
	var buf bytes.Buffer
	WriteUniversalTag(TAG_BOOLEAN, false, &buf)
	WriteLength(1, &buf)
	buf.WriteByte(0x01)
	if _, err := ReadBoolean(&buf); err == nil {
		t.Error("ReadBoolean accepted 0x01, which is BER and not DER")
	}

	buf.Reset()
	WriteBoolean(true, &buf)
	got, err := ReadBoolean(&buf)
	if err != nil || !got {
		t.Errorf("ReadBoolean(true) gave %v, %v", got, err)
	}
}
