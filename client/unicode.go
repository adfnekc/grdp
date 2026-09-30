package client

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/adfnekc/grdp/glog"
	"github.com/adfnekc/grdp/protocol/pdu"
)

// ErrNoUnicodeInput is returned by TypeText on a session whose protocol has no
// Unicode keyboard event.
var ErrNoUnicodeInput = errors.New("client: this protocol has no Unicode key event; an RFB server takes scancodes only")

// UnicodeKeyDown presses the key that produces r.
//
// A scancode says which key was pressed; this says which character was meant,
// and it is the only way to send what an input method produced. That is what it
// exists for in RDP: a Chinese, Japanese or Korean IME composes several
// keystrokes into one character, and the composed character has no scancode.
// Latin text can be sent this way too, and then the server does not need to know
// the client's keyboard layout.
//
// Use TypeText for a string, since it pairs the presses with releases.
func (c *Client) UnicodeKeyDown(r rune) {
	c.ctl.UnicodeKeyDown(r)
}

// UnicodeKeyUp releases the key that UnicodeKeyDown pressed for r.
func (c *Client) UnicodeKeyUp(r rune) {
	c.ctl.UnicodeKeyUp(r)
}

// TypeText types s one character at a time, holding each key for hold before
// releasing it. A hold of zero sends the release immediately, with no delay at
// all, which is what a server that reads the event stream rather than a keyboard
// queue needs; a small hold is what a server that has to synthesise keystrokes
// from them needs, and is the difference between the text arriving and the
// server dropping all but the first character.
//
// It returns an error if the session's protocol has no Unicode key event, or if
// s is not valid UTF-8 and so cannot be turned into characters.
func (c *Client) TypeText(s string, hold time.Duration) error {
	if c == nil || c.ctl == nil {
		return ErrNoUnicodeInput
	}
	if c.tc != TC_RDP {
		return ErrNoUnicodeInput
	}
	if !utf8.ValidString(s) {
		// Every invalid byte would otherwise become U+FFFD and be typed as a
		// replacement character, which looks like the text arrived and is
		// wrong.
		return fmt.Errorf("client: text is not valid UTF-8")
	}
	for _, r := range s {
		c.UnicodeKeyDown(r)
		if hold > 0 {
			time.Sleep(hold)
		}
		c.UnicodeKeyUp(r)
		if hold > 0 {
			time.Sleep(hold)
		}
	}
	return nil
}

// unicodeUnits returns the UTF-16 code units for r: one for a character in the
// basic multilingual plane, two for one above it.
//
// RDP's Unicode key event carries a single 16 bit code, so a supplementary
// character cannot be one event. FreeRDP's API is the same shape, which is why
// the convention is two events for the two halves of the pair.
func unicodeUnits(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	high, low := utf16.EncodeRune(r)
	return []uint16{uint16(high), uint16(low)}
}

// UnicodeKeyDown presses the key that produces r. See Client.UnicodeKeyDown.
func (c *RdpClient) UnicodeKeyDown(r rune) {
	c.unicodeKey(r, false)
}

// UnicodeKeyUp releases it.
func (c *RdpClient) UnicodeKeyUp(r rune) {
	c.unicodeKey(r, true)
}

// unicodeKey sends one Unicode key event, or two for a supplementary character.
//
// The halves of a surrogate pair are pressed high then low, and released low
// then high, so that the pair is never half pressed. That order is how a Windows
// console receives a non-BMP character too, as two key events with the high
// surrogate first. The specification says nothing about supplementary characters
// (MS-RDPBCGR 2.2.8.1.1.3.1.1.2 documents only KBDFLAGS_RELEASE), so this is
// taken from the shape of the event and from how Windows itself pairs them, and
// it is the one part of this path that a real server has not been asked about:
// only characters in the basic multilingual plane have been typed into a Windows
// input method from here.
func (c *RdpClient) unicodeKey(r rune, release bool) {
	if c == nil || c.pdu == nil {
		return
	}
	events := unicodeKeyEvents(r, release)
	if len(events) == 0 {
		return
	}
	out := make([]pdu.InputEventsInterface, 0, len(events))
	for _, e := range events {
		out = append(out, e)
	}
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_UNICODE, out)
}

// unicodeKeyEvents builds the events for one Unicode key press or release: one
// event for a character in the basic multilingual plane, two for a supplementary
// one, released in the opposite order to the way they were pressed. It returns
// nothing for a lone surrogate, which is not a character.
func unicodeKeyEvents(r rune, release bool) []*pdu.UnicodeKeyEvent {
	if utf16.IsSurrogate(r) {
		// Half of a pair on its own cannot be typed, and sending it would put
		// half a character in the server's input queue.
		glog.Warnf("client: %U is a surrogate half, not a character; not sent", r)
		return nil
	}
	units := unicodeUnits(r)
	if release {
		for i, j := 0, len(units)-1; i < j; i, j = i+1, j-1 {
			units[i], units[j] = units[j], units[i]
		}
	}
	events := make([]*pdu.UnicodeKeyEvent, 0, len(units))
	for _, u := range units {
		e := &pdu.UnicodeKeyEvent{Unicode: u}
		if release {
			e.KeyboardFlags = pdu.KBDFLAGS_RELEASE
		}
		events = append(events, e)
	}
	return events
}
