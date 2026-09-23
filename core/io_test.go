package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"
)

func TestWriteUInt16LE(t *testing.T) {
	buff := &bytes.Buffer{}
	WriteUInt32LE(66538, buff)
	result := hex.EncodeToString(buff.Bytes())
	expected := "ea030100"
	if result != expected {
		t.Error(result, "not equals to", expected)
	}
}

func TestStartReadBytesEOFInvokesOnce(t *testing.T) {
	done := make(chan error, 1)
	StartReadBytes(4, bytes.NewReader(nil), func(_ []byte, err error) {
		done <- err
	})
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		// The previous implementation busy-looped forever on EOF.
		t.Fatal("callback not invoked within 2s (EOF spin regression)")
	}
}

func TestStartReadBytesPartialRead(t *testing.T) {
	done := make(chan error, 1)
	StartReadBytes(4, bytes.NewReader([]byte{1, 2}), func(b []byte, err error) {
		if len(b) != 4 {
			t.Errorf("buffer len = %d, want 4", len(b))
		}
		done <- err
	})
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err = %v, want ErrUnexpectedEOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback not invoked")
	}
}

func TestReadUint16LEPropagatesError(t *testing.T) {
	if _, err := ReadUint16LE(bytes.NewReader([]byte{1})); err == nil {
		t.Fatal("expected error for short input")
	}
}
