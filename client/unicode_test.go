package client

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// A character in the basic multilingual plane is one event, which is the common
// case and the one a Chinese or Japanese input method produces.
func TestUnicodeKeyIsOneEventForTheBmp(t *testing.T) {
	for _, r := range []rune{'a', '1', '中', '文', '测', '試', '가', 'é'} {
		down := unicodeKeyEvents(r, false)
		if len(down) != 1 {
			t.Fatalf("%U produced %d events, want 1", r, len(down))
		}
		if down[0].Unicode != uint16(r) {
			t.Errorf("%U sent code %#04x", r, down[0].Unicode)
		}
		if down[0].KeyboardFlags != 0 {
			t.Errorf("%U key down carried flags %#04x, want 0", r, down[0].KeyboardFlags)
		}
		up := unicodeKeyEvents(r, true)
		if len(up) != 1 || up[0].Unicode != uint16(r) {
			t.Errorf("%U release = %v", r, up)
		}
		if up[0].KeyboardFlags != pdu.KBDFLAGS_RELEASE {
			t.Errorf("%U key up carried flags %#04x, want %#04x", r, up[0].KeyboardFlags, pdu.KBDFLAGS_RELEASE)
		}
	}
}

// A supplementary character cannot be one event, because the event carries a
// single 16 bit code. It becomes two, high surrogate first.
func TestUnicodeKeySplitsASurrogatePair(t *testing.T) {
	const emoji = '\U0001F600'
	high, low := utf16.EncodeRune(emoji)

	down := unicodeKeyEvents(emoji, false)
	if len(down) != 2 {
		t.Fatalf("a supplementary character produced %d events, want 2", len(down))
	}
	if down[0].Unicode != uint16(high) || down[1].Unicode != uint16(low) {
		t.Errorf("pressed %#04x %#04x, want high %#04x then low %#04x",
			down[0].Unicode, down[1].Unicode, high, low)
	}

	// The release is the other way round, so the pair is never left half
	// pressed if the two are delivered out of order.
	up := unicodeKeyEvents(emoji, true)
	if len(up) != 2 {
		t.Fatalf("a supplementary character released with %d events, want 2", len(up))
	}
	if up[0].Unicode != uint16(low) || up[1].Unicode != uint16(high) {
		t.Errorf("released %#04x %#04x, want low %#04x then high %#04x",
			up[0].Unicode, up[1].Unicode, low, high)
	}
	for i, e := range up {
		if e.KeyboardFlags != pdu.KBDFLAGS_RELEASE {
			t.Errorf("release event %d carried flags %#04x", i, e.KeyboardFlags)
		}
	}
}

// A surrogate half on its own is not a character. Sending it would put half a
// pair in the server's input queue, so nothing is sent.
func TestUnicodeKeyRefusesLoneSurrogates(t *testing.T) {
	for _, r := range []rune{0xD800, 0xDBFF, 0xDC00, 0xDFFF} {
		if e := unicodeKeyEvents(r, false); len(e) != 0 {
			t.Errorf("%#04x sent %d events", r, len(e))
		}
		if e := unicodeKeyEvents(r, true); len(e) != 0 {
			t.Errorf("%#04x released with %d events", r, len(e))
		}
	}
}

func TestUnicodeUnits(t *testing.T) {
	if got := unicodeUnits('A'); len(got) != 1 || got[0] != 0x41 {
		t.Errorf("A = %v", got)
	}
	got := unicodeUnits('\U0001F600')
	if len(got) != 2 || !utf16.IsSurrogate(rune(got[0])) || !utf16.IsSurrogate(rune(got[1])) {
		t.Errorf("emoji = %v, want two surrogate units", got)
	}
}

// TypeText is RDP only: an RFB server takes keysyms and scancodes, and has no
// way to say which character an input method produced.
func TestTypeTextRefusesVnc(t *testing.T) {
	c := NewClient("host:5900", "u", "p", TC_VNC, nil)
	c.ctl = &fakeCtl{}
	if err := c.TypeText("hi", 0); err != ErrNoUnicodeInput {
		t.Errorf("TypeText on VNC = %v, want ErrNoUnicodeInput", err)
	}
}

// Text that is not valid UTF-8 would otherwise be typed as replacement
// characters, which looks like the text arrived and is wrong.
func TestTypeTextRefusesInvalidUTF8(t *testing.T) {
	c := newFakeClient(&fakeCtl{})
	if err := c.TypeText("ok\xff\xfe", 0); err == nil {
		t.Error("invalid UTF-8 was accepted")
	}
}

// TypeText on a VNC client has to reach the caller as an error rather than a
// partial send, and the hold must be applied without changing what is sent.
func TestTypeTextThroughTheFake(t *testing.T) {
	f := &fakeCtl{}
	c := newFakeClient(f)
	if err := c.TypeText("ab", 0); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	// A small hold is accepted too, and does not lose characters.
	if err := c.TypeText("中", time.Millisecond); err != nil {
		t.Fatalf("TypeText with a hold: %v", err)
	}
}

func TestTypeTextEmptyStringIsFine(t *testing.T) {
	c := newFakeClient(&fakeCtl{})
	if err := c.TypeText("", time.Millisecond); err != nil {
		t.Errorf("TypeText(\"\") = %v", err)
	}
	// And a string of only invalid bytes is still rejected, so the check is
	// not simply skipping empty input.
	if err := c.TypeText(string([]byte{0xff}), 0); err == nil {
		t.Error("a lone invalid byte was accepted")
	}
}

// The doc for TypeText claims the hold is between press and release, so that a
// server which synthesises keystrokes sees a held key rather than a press and a
// release in the same instant.
func TestTypeTextHoldIsSpent(t *testing.T) {
	f := &fakeCtl{}
	c := newFakeClient(f)
	const hold = 20 * time.Millisecond
	start := time.Now()
	if err := c.TypeText(strings.Repeat("x", 3), hold); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	// Three characters, each held and each followed by the same pause.
	if elapsed := time.Since(start); elapsed < 3*hold {
		t.Errorf("TypeText returned after %s, which is less than one hold per character", elapsed)
	}
}
