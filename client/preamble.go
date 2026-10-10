package client

import (
	"errors"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// ErrNoInputPreamble is returned by SendSynchronize and SendInputPreamble on a
// session whose protocol has no such event. It is an RDP notion; an RFB server
// takes input through its own messages and has no toggle key state to be told
// about.
var ErrNoInputPreamble = errors.New("client: this protocol has no input preamble; an RFB server takes input through its own messages")

// SendSynchronize sends the synchronise input event, which tells the server which
// toggle keys are on. Every other client sends one when the session becomes
// ready; FreeRDP's comment on its own says "send a tab up like mstsc.exe".
//
// toggleFlags is a mask of the pdu.TS_SYNC_* constants. The state only has to be
// right for the server's notion of, say, Num Lock to match the user's, so passing
// 0 says "all off".
//
// On the fast path this is a single byte: the toggle state rides in the event
// header and there is no payload after it.
func (c *Client) SendSynchronize(toggleFlags uint32) error {
	if c == nil || c.ctl == nil {
		return ErrNoInputPreamble
	}
	return c.ctl.SendSynchronize(toggleFlags)
}

// SendInputPreamble sends the input sequence mstsc and FreeRDP send once the
// session is ready: a Tab release, the toggle key state, then another Tab release.
// The two Tab releases clear any key the server thinks is still held, which is
// why they are there, and all three events travel in one PDU.
//
// That PDU is the reason this is one call rather than three: its events have
// three different event codes, and until the fast path derived a code per event
// rather than one per PDU it could not be expressed at all. A client that wants
// only the toggle state can call SendSynchronize instead.
func (c *Client) SendInputPreamble(toggleFlags uint32) error {
	if c == nil || c.ctl == nil {
		return ErrNoInputPreamble
	}
	return c.ctl.SendInputPreamble(toggleFlags)
}

// SendSynchronize sends the synchronise input event, which tells the server which
// toggle keys are on. Every other client sends one when the session becomes
// ready; FreeRDP's comment on its own says "send a tab up like mstsc.exe".
//
// toggleFlags is a mask of the TS_SYNC_* constants in the pdu package. The state
// only has to be right for the server's notion of, say, Num Lock to match the
// user's, so passing 0 says "all off".
//
// On the fast path this is a single byte: the toggle state rides in the event
// header and there is no payload.
func (c *RdpClient) SendSynchronize(toggleFlags uint32) error {
	if c == nil || c.pdu == nil {
		return errors.New("client: sending input needs a connected session")
	}
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_SYNC, []pdu.InputEventsInterface{
		&pdu.SynchronizeEvent{ToggleFlags: toggleFlags},
	})
	return nil
}

// SendInputPreamble sends the input sequence mstsc and FreeRDP send once the
// session is ready: a Tab release, the toggle key state, then another Tab release.
// The two Tab releases clear any key the server thinks is still held, which is
// why they are there, and all three events travel in one PDU.
//
// That PDU is the reason this is a single call rather than three: its events have
// three different event codes, and until the fast path derived a code per event
// rather than one per PDU, it could not be expressed at all. A client that wants
// only the toggle state can call SendSynchronize instead.
func (c *RdpClient) SendInputPreamble(toggleFlags uint32) error {
	if c == nil || c.pdu == nil {
		return errors.New("client: sending input needs a connected session")
	}
	// Scancode 0x0f is Tab. The release flag is the slow path's; the fast path
	// maps it to its own release bit as it serializes.
	tabUp := &pdu.ScancodeKeyEvent{KeyboardFlags: pdu.KBDFLAGS_RELEASE, KeyCode: 0x0f}
	tabUp2 := &pdu.ScancodeKeyEvent{KeyboardFlags: pdu.KBDFLAGS_RELEASE, KeyCode: 0x0f}
	c.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{
		tabUp,
		&pdu.SynchronizeEvent{ToggleFlags: toggleFlags},
		tabUp2,
	})
	return nil
}
