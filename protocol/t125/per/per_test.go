package per

import (
	"bytes"
	"strings"
	"testing"
)

// ReadExcept used to panic on an unexpected byte, which is not something a parser
// reading bytes off a network should do: hostile input could end the process. The
// value is still consumed, so a caller must treat the error as fatal to the
// message.
func TestReadExceptReportsRatherThanPanics(t *testing.T) {
	if err := ReadExcept(0x08, bytes.NewReader([]byte{0x08})); err != nil {
		t.Errorf("ReadExcept on the expected byte failed: %v", err)
	}

	err := ReadExcept(0x08, bytes.NewReader([]byte{0x09}))
	if err == nil {
		t.Fatal("ReadExcept accepted the wrong byte")
	}
	if !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "read") {
		t.Errorf("ReadExcept failed with %q, which does not say what it saw and wanted", err)
	}

	// A short read is an error here rather than a panic, and the caller cannot
	// tell it from a wrong byte without reading the message.
	if err := ReadExcept(0x08, bytes.NewReader(nil)); err == nil {
		t.Error("ReadExcept accepted a short read")
	}
}

// The object identifier's first byte is the first two components combined as
// oid[0]*40+oid[1]. This used to be a bitwise AND of the two, which agrees only
// when both are zero: that is true of 0.0.20.127.0.0.1, the identifier this
// library actually writes, and of nothing else.
func TestWriteObjectIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name string
		oid  []byte
		want []byte
	}{
		// The identifier MCS connects with, whose two leading zeroes are what
		// made the old expression look right.
		{"the T.124 identifier", []byte{0, 0, 20, 124, 0, 1},
			[]byte{0x05, 0x00, 0x14, 0x7c, 0x00, 0x01}},
		// A general identifier: 1*40+2 is 42, where the old expression produced
		// zero because 1&2 is zero.
		{"components that are not zero", []byte{1, 2, 3, 4, 5, 6},
			[]byte{0x05, 42, 3, 4, 5, 6}},
		{"a large second component", []byte{2, 30, 9, 9, 9, 9},
			[]byte{0x05, 110, 9, 9, 9, 9}},
	} {
		var buf bytes.Buffer
		WriteObjectIdentifier(tc.oid, &buf)
		if !bytes.Equal(buf.Bytes(), tc.want) {
			t.Errorf("%s: wrote % X, want % X", tc.name, buf.Bytes(), tc.want)
		}
	}
}

// MatchOctetStream was called MacthOctetStream. The rename is a breaking change,
// so the behaviour it had is pinned here: the minimum is added back to the length
// on the wire before comparing.
func TestMatchOctetStream(t *testing.T) {
	var buf bytes.Buffer
	WriteOctetStream(h221CSKey, MinH221NonStandardIdentifierLen, &buf)

	if !MatchOctetStream(bytes.NewReader(buf.Bytes()), h221CSKey, MinH221NonStandardIdentifierLen) {
		t.Error("MatchOctetStream did not match the stream it had just written")
	}
	if MatchOctetStream(bytes.NewReader(buf.Bytes()), "other", MinH221NonStandardIdentifierLen) {
		t.Error("MatchOctetStream matched a different string")
	}
}

const h221CSKey = "Duca"
