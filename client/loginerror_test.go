package client

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/adfnekc/grdp/protocol/t125"
	"github.com/adfnekc/grdp/protocol/tpkt"
	"github.com/adfnekc/grdp/protocol/x224"
)

// A refused logon against an NLA server arrives as a TLS alert, because Windows
// drops the connection rather than answering with an NTLM status. It has to come
// out as an authentication failure, or an operator reads "tls: internal error"
// and goes looking at certificates.
func TestClassifyErrorMarksAuthentication(t *testing.T) {
	// This is the shape a real one has: the TLS alert wrapped by the CredSSP
	// layer, then by x224.
	inner := errors.New("remote error: tls: internal error")
	err := fmt.Errorf("x224: start NLA: %w", &tpkt.CredSSPError{Err: fmt.Errorf("read header: %w", inner)})

	got := classifyError(err)
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
func TestClassifyErrorLeavesNetworkErrorsAlone(t *testing.T) {
	refused := fmt.Errorf("client: cannot reach the server: %w",
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	got := classifyError(refused)

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
func TestClassifyErrorLeavesNegotiationAlone(t *testing.T) {
	err := fmt.Errorf("client: the RDP connection failed: %w",
		&x224.NegotiationFailure{Code: x224.HYBRID_REQUIRED_BY_SERVER})
	got := classifyError(err)

	var neg *x224.NegotiationFailure
	if !errors.As(got, &neg) || neg.Code != x224.HYBRID_REQUIRED_BY_SERVER {
		t.Errorf("the negotiation failure was lost: %v", got)
	}
	if errors.Is(got, ErrAuthenticationFailed) {
		t.Error("a negotiation failure was reported as an authentication failure")
	}
}

func TestClassifyErrorPassesOthersThrough(t *testing.T) {
	want := errors.New("something else")
	if got := classifyError(want); got != want {
		t.Errorf("got %v, want the error unchanged", got)
	}
	// A CredSSP error with no cause inside it is not worth inventing one for.
	if got := classifyError(&tpkt.CredSSPError{}); got == nil {
		t.Error("a bare CredSSP error became nil")
	} else if errors.Is(got, ErrAuthenticationFailed) {
		t.Errorf("an empty CredSSP error was reported as an authentication failure: %v", got)
	}
}

// A disconnect ultimatum has to come out as something other than a fault: the
// session is still there, detached, and reconnecting gets it back.
//
// Every reason byte is treated the same way, because the byte a real server sends
// does not decode as a reason this library can name. Against a local xrdp a
// takeover arrives as `02 f0 80 21 80`: 21 is the option byte naming
// disconnectProviderUltimatum and 80 is the reason, and 0x80 is not a value a
// five alternative Reason can hold. What is certain is that the server ended the
// session, and that is what is reported.
func TestClassifyErrorMarksServerEndedSessions(t *testing.T) {
	for _, reason := range []uint8{0x80, t125.RN_DOMAIN_DISCONNECTED, t125.RN_PROVIDER_INITIATED, t125.RN_USER_REQUESTED} {
		err := fmt.Errorf("session: %w", &t125.DisconnectError{Reason: reason})
		got := classifyError(err)

		if !errors.Is(got, ErrSessionEndedByServer) {
			t.Errorf("reason %#x: a server disconnect was not reported as one: %v", reason, got)
		}
		var disc *t125.DisconnectError
		if !errors.As(got, &disc) {
			t.Errorf("reason %#x: the error was lost: %v", reason, got)
		}
		if errors.Is(got, ErrAuthenticationFailed) {
			t.Errorf("reason %#x: reported as an authentication failure", reason)
		}
	}
}

// The reason byte is passed through as it arrived rather than mapped to a name,
// because the mapping is not established.
func TestDisconnectErrorKeepsTheRawReason(t *testing.T) {
	e := &t125.DisconnectError{Reason: 0x80}
	if !strings.Contains(e.Error(), "0x80") {
		t.Errorf("the reason byte is not in the message: %v", e)
	}
}

// Both classification paths run, so a second pass must not wrap what the first
// one did. Otherwise the message grows a prefix per caller.
func TestClassifyErrorIsIdempotent(t *testing.T) {
	for _, err := range []error{
		&t125.DisconnectError{Reason: 0x80},
		&tpkt.CredSSPError{Err: errors.New("remote error: tls: internal error")},
	} {
		once := classifyError(err)
		twice := classifyError(once)
		if once.Error() != twice.Error() {
			t.Errorf("classifying twice changed the error:\n once: %v\ntwice: %v", once, twice)
		}
	}
}
