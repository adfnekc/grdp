package x224_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/protocol/tpkt"
	"github.com/adfnekc/grdp/protocol/x224"
)

// readTPKT reads one TPKT frame (4 byte header + body) from the server end.
func readTPKT(c net.Conn) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	if hdr[0] != 0x03 {
		return nil, errors.New("not a TPKT X.224 frame")
	}
	size := int(binary.BigEndian.Uint16(hdr[2:4]))
	if size < 4 {
		return nil, errors.New("invalid TPKT size")
	}
	body := make([]byte, size-4)
	if _, err := io.ReadFull(c, body); err != nil {
		return nil, err
	}
	return body, nil
}

// writeConfirm sends an X.224 ServerConnectionConfirm with a negotiation block.
func writeConfirm(c net.Conn, negType byte, code uint32) error {
	body := []byte{
		0x13, 0xD0, // Len, TPDU_CONNECTION_CONFIRM
		0x00, 0x00, 0x12, 0x34, 0x00, // padding
		negType, 0x00, 0x08, 0x00, // Negotiation Type/Flag/Length
		byte(code), byte(code >> 8), byte(code >> 16), byte(code >> 24),
	}
	frame := []byte{0x03, 0x00, byte((len(body) + 4) >> 8), byte((len(body) + 4) & 0xff)}
	frame = append(frame, body...)
	_, err := c.Write(frame)
	return err
}

func newPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	return cli, srv
}

func TestConnectSelectsStandardRDP(t *testing.T) {
	cli, srv := newPair(t)

	go func() {
		if _, err := readTPKT(srv); err != nil {
			t.Errorf("server read request: %v", err)
			return
		}
		// TYPE_RDP_NEG_RSP with result PROTOCOL_RDP (0)
		if err := writeConfirm(srv, 0x02, x224.PROTOCOL_RDP); err != nil {
			t.Errorf("server write confirm: %v", err)
		}
	}()

	x := x224.New(tpkt.New(core.NewSocketLayer(cli), nil))
	x.SetRequestedProtocol(x224.PROTOCOL_RDP)
	if err := x.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

func TestConnectNegotiationFailure(t *testing.T) {
	cli, srv := newPair(t)

	go func() {
		if _, err := readTPKT(srv); err != nil {
			return
		}
		// TYPE_RDP_NEG_FAILURE, SSL_NOT_ALLOWED_BY_SERVER (2)
		if err := writeConfirm(srv, 0x03, 2); err != nil {
			t.Errorf("server write confirm: %v", err)
		}
	}()

	x := x224.New(tpkt.New(core.NewSocketLayer(cli), nil))
	x.SetRequestedProtocol(x224.PROTOCOL_SSL)
	err := x.Connect()
	if err == nil {
		t.Fatal("expected a negotiation failure")
	}
	var nf *x224.NegotiationFailure
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NegotiationFailure", err)
	}
	if nf.Code != 2 {
		t.Fatalf("failure code = %d, want 2", nf.Code)
	}
}

func TestConnectTimeout(t *testing.T) {
	cli, srv := newPair(t)
	// Consume the request but never answer.
	go func() { readTPKT(srv) }()

	x := x224.New(tpkt.New(core.NewSocketLayer(cli), nil))
	x.SetConnectTimeout(200 * time.Millisecond)
	start := time.Now()
	err := x.ConnectContext(context.Background())
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("ConnectContext took %v, expected to time out quickly", elapsed)
	}
}

func TestConnectPeerClose(t *testing.T) {
	cli, srv := newPair(t)
	go func() {
		readTPKT(srv)
		srv.Close() // hang up instead of confirming
	}()

	x := x224.New(tpkt.New(core.NewSocketLayer(cli), nil))
	x.SetConnectTimeout(2 * time.Second)
	if err := x.ConnectContext(context.Background()); err == nil {
		t.Fatal("expected an error when the peer closes the connection")
	}
}
