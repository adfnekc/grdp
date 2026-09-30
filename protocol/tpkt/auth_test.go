package tpkt

import (
	"errors"
	"net"
	"testing"

	"github.com/adfnekc/grdp/core"
)

func newTestTPKT(t *testing.T) *TPKT {
	t.Helper()
	a, _ := net.Pipe()
	t.Cleanup(func() { a.Close() })
	return New(core.NewSocketLayer(a), nil)
}

// A failure while the server is deciding whether to accept the credentials is an
// authentication failure, whatever it looks like on the wire. This is the window
// Windows leaves: it does not answer a bad logon, it drops TLS, and the alert
// arrives on the ordinary read path.
func TestClassifyAuthErrorInsideTheWindow(t *testing.T) {
	tpkt := newTestTPKT(t)
	tpkt.awaitingVerdict = true

	alert := errors.New("remote error: tls: internal error")
	got := tpkt.classifyAuthError(alert, "read header")

	var cred *CredSSPError
	if !errors.As(got, &cred) {
		t.Fatalf("a failure while awaiting the verdict was not marked: %v", got)
	}
	if !errors.Is(got, alert) {
		t.Errorf("the underlying error was lost: %v", got)
	}
	// One failure is one verdict: the window closes, so a later drop is not
	// blamed on the credentials.
	if tpkt.awaitingVerdict {
		t.Error("the window stayed open after a failure")
	}
}

// Outside the window the error is left exactly as it came. Blaming every later
// TLS problem on the password would be worse than saying nothing.
func TestClassifyAuthErrorOutsideTheWindow(t *testing.T) {
	tpkt := newTestTPKT(t)
	alert := errors.New("remote error: tls: internal error")

	got := tpkt.classifyAuthError(alert, "read header")
	if got != alert {
		t.Errorf("got %v, want the error unchanged", got)
	}
	var cred *CredSSPError
	if errors.As(got, &cred) {
		t.Error("a failure outside the window was marked as authentication")
	}
	if tpkt.classifyAuthError(nil, "read header") != nil {
		t.Error("a nil error became non-nil")
	}
}
