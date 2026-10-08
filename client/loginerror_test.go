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
//
// The shape below is what the transport produces: the credentials were sent, the
// server went quiet, and the alert arrived on the ordinary read path.
func TestClassifyErrorMarksAuthentication(t *testing.T) {
	inner := errors.New("remote error: tls: internal error")
	err := fmt.Errorf("x224: start NLA: %w",
		&tpkt.CredentialsError{Err: fmt.Errorf("read header: %w", inner)})

	got := classifyError(err)
	if !errors.Is(got, ErrAuthenticationFailed) {
		t.Errorf("a failure while the server was deciding is not reported as authentication: %v", got)
	}
	// The cause is kept, because it is the evidence.
	if !errors.Is(got, inner) {
		t.Errorf("the underlying error was lost: %v", got)
	}
	// And it is not confused with the two failures it is easy to confuse it
	// with: a CredSSP fault and a TLS fault.
	if errors.Is(got, ErrCredSSP) || errors.Is(got, ErrTLSFailure) {
		t.Errorf("authentication was reported as %v", got)
	}
}

// The exchange failing before the credentials are sent is a CredSSP fault, not an
// authentication one: nothing has been proven wrong about the account yet, and a
// caller told to check credentials would be checking the wrong thing.
func TestClassifyErrorSeparatesCredSSPFromCredentials(t *testing.T) {
	err := fmt.Errorf("x224: start NLA: %w",
		&tpkt.CredSSPError{Err: errors.New("nla: DER message would not parse")})
	got := classifyError(err)

	if !errors.Is(got, ErrCredSSP) {
		t.Errorf("a CredSSP fault is not reported as one: %v", got)
	}
	if errors.Is(got, ErrAuthenticationFailed) {
		t.Error("a CredSSP fault was reported as an authentication failure")
	}
}

// A TLS handshake failure happens before anything is sent at all, so it is about
// the certificate or the cipher and cannot be caused by the password.
func TestClassifyErrorMarksTLSFailure(t *testing.T) {
	err := fmt.Errorf("x224: start NLA: %w",
		&tpkt.TLSError{Err: errors.New("x509: certificate signed by unknown authority")})
	got := classifyError(err)

	if !errors.Is(got, ErrTLSFailure) {
		t.Errorf("a TLS handshake failure is not reported as one: %v", got)
	}
	if errors.Is(got, ErrAuthenticationFailed) || errors.Is(got, ErrCredSSP) {
		t.Error("a TLS failure was reported as an authentication or CredSSP failure")
	}
}

// A failure to reach the host keeps both things a caller needs: the sentinel that
// says "could not connect", and the net.OpError underneath, so that a refused
// port is still distinguishable from an unreachable host.
func TestClassifyErrorMarksUnreachable(t *testing.T) {
	err := fmt.Errorf("client: cannot reach the server: %w",
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	got := classifyError(err)

	if !errors.Is(got, ErrUnreachable) {
		t.Errorf("a dial failure is not reported as unreachable: %v", got)
	}
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

// A TLS alert is also a net.OpError, with an operation of "remote error". Calling
// that unreachable would be a second answer to a question already answered, and
// the wrong one: the server was reached.
func TestClassifyErrorDoesNotCallATLSAlertUnreachable(t *testing.T) {
	err := fmt.Errorf("x224: start NLA: %w",
		&tpkt.CredentialsError{Err: &net.OpError{Op: "remote error", Err: errors.New("tls: internal error")}})
	got := classifyError(err)

	if errors.Is(got, ErrUnreachable) {
		t.Errorf("a TLS alert from a server that was reached was reported as unreachable: %v", got)
	}
	if !errors.Is(got, ErrAuthenticationFailed) {
		t.Errorf("it should still be an authentication failure: %v", got)
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
