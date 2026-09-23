// client_test.go
package client

import (
	"testing"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in            string
		domain, uname string
	}{
		{"dom\\user", "dom", "user"},
		{"dom/user", "dom", "user"},
		{"user", "", "user"},
	}
	for _, c := range cases {
		d, u := split(c.in)
		if d != c.domain || u != c.uname {
			t.Errorf("split(%q) = (%q,%q), want (%q,%q)", c.in, d, u, c.domain, c.uname)
		}
	}
}

// TestClientLoginFailureIsSafe verifies that a client which never connected
// (dial failed) does not panic when the public API is used.
func TestClientLoginFailureIsSafe(t *testing.T) {
	c := NewClient("127.0.0.1:1", "foo", "bar", TC_RDP, nil)
	if err := c.Login(); err == nil {
		t.Fatal("expected Login to fail against a closed port")
	}
	// These must be safe no-ops on a disconnected client.
	c.OnReady(func() {})
	c.OnError(func(error) {})
	c.OnClose(func() {})
	c.OnBitmap(func([]Bitmap) {})
	c.KeyDown(0x1e, "")
	c.KeyUp(0x1e, "")
	c.MouseMove(1, 1)
	c.MouseWheel(1, 1, 1)
	c.MouseDown(0, 1, 1)
	c.MouseUp(0, 1, 1)
	c.Close()
}
