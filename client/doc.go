// Package client is the API to use. It connects, authenticates, negotiates, and
// hands over the desktop, the pointer, the clipboard and the input.
//
// A session is built with NewSetting and NewClient, handlers are registered, and
// Login connects and waits for readiness:
//
//	s := client.NewSetting()
//	s.Protocol = "nla"
//	c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)
//	c.OnFrame(func(dirty []image.Rectangle) { /* encode */ })
//	c.OnCursor(func(cur *client.Cursor) { /* draw cur.Image at cur.Hotspot */ })
//	if err := c.Login(); err != nil {
//		log.Fatal(err)
//	}
//
// # Where the pixels are
//
// [Framebuffer] holds the desktop as BGRA, whatever drawing path the server
// chose, and [Client.OnFrame] reports the regions that changed since the last
// call, already merged and clipped. That is the intended way to read a screen:
// OnBitmap, Screen and OnOrdersFrame still work for callers that want the raw
// updates or the orders machinery, and are deprecated where the framebuffer
// supersedes them.
//
// The pointer is not part of the desktop. [Client.OnCursor] gives it as an image
// with alpha and a hotspot, and fires when its shape changes rather than when it
// moves.
//
// # Opting in
//
// Four settings change what is negotiated and are therefore off by default:
// EnableClipboard, EnableDisplayControl, EnableEGFX and EnableOrders. Each is
// documented on the field with what it costs, and the two drawing ones in
// particular cannot be combined with the default bitmap path in a single
// session.
//
// # Failures
//
// Login and the error events report failures that classify into kinds a caller
// can act on: [ErrUnreachable], [ErrTLSFailure], [ErrCredSSP],
// [ErrAuthenticationFailed] and [ErrSessionEndedByServer], alongside the typed
// errors from the protocol packages. The split is by stage, because that is what
// the transport can know: before the credentials are sent a failure is about TLS
// or CredSSP, and after they are sent and the server goes quiet it is about the
// account. The README has the table.
//
// # VNC
//
// [VncClient] is the RFB client for TC_VNC sessions. It is a different protocol
// that shares only the socket, and it has its own framebuffer inside
// protocol/rfb because an RFB server chooses the pixel format per connection.
package client
