package client

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/adfnekc/grdp/protocol/tpkt"
	"github.com/adfnekc/grdp/protocol/x224"
)

// A refused logon against an NLA server arrives as a TLS alert, because Windows
// drops the connection rather than answering with an NTLM status. It has to come
// out as an authentication failure, or an operator reads "tls: internal error"
// and goes looking at certificates.
func TestClassifyLoginErrorMarksAuthentication(t *testing.T) {
	// This is the shape a real one has: the TLS alert wrapped by the CredSSP
	// layer, then by x224.
	inner := errors.New("remote error: tls: internal error")
	err := fmt.Errorf("x224: start NLA: %w", &tpkt.CredSSPError{Err: fmt.Errorf("read header: %w", inner)})

	got := classifyLoginError(err)
	if !errors.Is(got, ErrAuthenticationFailed) {
		t.Errorf("a CredSSP failure is not reported as an authentication failure: %v", got)
	}
	// The cause is kept, because it is the evidence.
	if !errors.Is(got, inner) {
		t.Errorf("the underlying error was lost: %v", got)
	}
	var cred *tpkt.CredSSPError
	if !errors.As(got, &cred) {
		t.Errorf("the CredSSP error is not in the chain: %v", got)
	}
}

// A failure to reach the host keeps its type, so a caller can still tell a
// closed port from an unreachable network. Formatting rather than wrapping would
// lose the net.OpError and make every network fault look alike.
func TestClassifyLoginErrorLeavesNetworkErrorsAlone(t *testing.T) {
	refused := fmt.Errorf("client: cannot reach the server: %w",
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	got := classifyLoginError(refused)

	if errors.Is(got, ErrAuthenticationFailed) {
		t.Error("a refused connection was reported as an authentication failure")
	}
	if !errors.Is(got, syscall.ECONNREFUSED) {
		t.Errorf("the connection refused errno was lost: %v", got)
	}
	var op *net.OpError
	if !errors.As(got, &op) {
		t.Errorf("the net.OpError was lost: %v", got)
	}
}

// A security negotiation failure is already a typed error with a code, and the
// classification must not get in its way.
func TestClassifyLoginErrorLeavesNegotiationAlone(t *testing.T) {
	err := fmt.Errorf("client: the RDP connection failed: %w",
		&x224.NegotiationFailure{Code: x224.HYBRID_REQUIRED_BY_SERVER})
	got := classifyLoginError(err)

	var neg *x224.NegotiationFailure
	if !errors.As(got, &neg) || neg.Code != x224.HYBRID_REQUIRED_BY_SERVER {
		t.Errorf("the negotiation failure was lost: %v", got)
	}
	if errors.Is(got, ErrAuthenticationFailed) {
		t.Error("a negotiation failure was reported as an authentication failure")
	}
}

func TestClassifyLoginErrorPassesOthersThrough(t *testing.T) {
	want := errors.New("something else")
	if got := classifyLoginError(want); got != want {
		t.Errorf("got %v, want the error unchanged", got)
	}
	// A CredSSP error with no cause inside it is not worth inventing one for.
	if got := classifyLoginError(&tpkt.CredSSPError{}); got == nil {
		t.Error("a bare CredSSP error became nil")
	} else if errors.Is(got, ErrAuthenticationFailed) {
		t.Errorf("an empty CredSSP error was reported as an authentication failure: %v", got)
	}
}
