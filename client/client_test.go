// client_test.go
package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeCtl is a Control implementation used to drive LoginContext without a
// server.
type fakeCtl struct {
	handlers map[string][]interface{}
	loginErr error
	onLogin  func()
}

func (f *fakeCtl) On(event string, h interface{}) {
	if f.handlers == nil {
		f.handlers = map[string][]interface{}{}
	}
	f.handlers[event] = append(f.handlers[event], h)
}

func (f *fakeCtl) fire(event string, err error) {
	for _, h := range f.handlers[event] {
		switch fn := h.(type) {
		case func():
			fn()
		case func(error):
			fn(err)
		}
	}
}

func (f *fakeCtl) Login(host, user, passwd string, w, h int) error {
	if f.onLogin != nil {
		f.onLogin()
	}
	return f.loginErr
}
func (f *fakeCtl) KeyUp(sc int, name string)     {}
func (f *fakeCtl) KeyDown(sc int, name string)   {}
func (f *fakeCtl) MouseMove(x, y int)            {}
func (f *fakeCtl) MouseWheel(scroll, x, y int)   {}
func (f *fakeCtl) MouseUp(button, x, y int)      {}
func (f *fakeCtl) MouseDown(button, x, y int)    {}
func (f *fakeCtl) SetClipboardText(string) error { return nil }
func (f *fakeCtl) RequestClipboardText() error   { return nil }

func (f *fakeCtl) Close() {}

func newFakeClient(f *fakeCtl) *Client {
	c := NewClient("host:3389", "u", "p", TC_RDP, nil)
	c.ctl = f
	return c
}

func TestLoginContextReady(t *testing.T) {
	f := &fakeCtl{onLogin: func() {}}
	f.onLogin = func() { f.fire("ready", nil) }
	if err := newFakeClient(f).LoginContext(context.Background()); err != nil {
		t.Fatalf("LoginContext: %v", err)
	}
}

func TestLoginContextError(t *testing.T) {
	want := errors.New("boom")
	f := &fakeCtl{}
	f.onLogin = func() { f.fire("error", want) }
	err := newFakeClient(f).LoginContext(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestLoginContextClosed(t *testing.T) {
	f := &fakeCtl{}
	f.onLogin = func() { f.fire("close", nil) }
	if err := newFakeClient(f).LoginContext(context.Background()); err == nil {
		t.Fatal("expected an error when the connection closes before ready")
	}
}

func TestLoginContextTimeout(t *testing.T) {
	f := &fakeCtl{} // never emits anything
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := newFakeClient(f).LoginContext(ctx)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("LoginContext took %v, expected to honor the context", elapsed)
	}
}

func TestLoginContextHandshakeError(t *testing.T) {
	want := errors.New("dial failed")
	f := &fakeCtl{loginErr: want}
	if err := newFakeClient(f).LoginContext(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

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
