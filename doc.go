// Package grdp is a Go implementation of Microsoft's Remote Desktop Protocol:
// the client half of RDP, the RFB (VNC) client that this fork inherited
// alongside it, and the codecs, orders and virtual channels they need.
//
// It is meant to be built on rather than used as a program. There is no window
// anywhere in it, and every drawing path hands out pixels and leaves the
// painting to the caller, so the same library serves a desktop client, a
// command line tool and a browser gateway.
//
// # Getting started
//
//	s := client.NewSetting()
//	s.Width, s.Height = 1024, 768
//	s.Protocol = "nla"          // "tls" (the default), "nla" or "rdp"
//
//	c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)
//	defer c.Close()
//
//	c.OnFrame(func(dirty []image.Rectangle) { /* encode these regions */ })
//	c.OnCursor(func(cur *client.Cursor) { /* draw cur.Image at cur.Hotspot */ })
//
//	if err := c.Login(); err != nil {
//		log.Fatal(err)
//	}
//	c.MouseMove(400, 300)
//	c.TypeText("text", 8*time.Millisecond)
//
// [client.Framebuffer] is where the pixels are, whichever way the server chose
// to draw them, and [client.Client.OnFrame] says which parts of it changed.
// cmd/rdpcli is a headless client that dumps a PNG, and cmd/rdpws is a gateway
// that serves one to a browser; both are small enough to read as examples.
//
// # How it is put together
//
// The layers are the protocol's, from the socket upwards, and each one is a
// package that knows only about the layer beneath it:
//
//	protocol/tpkt      TPKT framing, and the TLS and CredSSP handshake
//	protocol/x224      the X.224 connection request, where security is chosen
//	protocol/nla       NTLMv2 and CredSSP, used by tpkt
//	protocol/t125      MCS: channel joins, and the GCC blocks that carry
//	                   the client's capabilities and settings
//	protocol/sec       RDP security: encryption, and the licensing exchange
//	protocol/pdu       the PDU layer: capabilities, updates, input, orders
//	protocol/rfb       RFB (VNC), which is a different protocol and shares none
//	                   of the above except the socket
//
// [client] sits on top and is the only package most callers need. The packages
// below it are exported because types from them appear in the API: a pointer
// update is a [pdu.PointerDataPDU], a security negotiation failure is an
// [x224.NegotiationFailure]. They are not offered as a stable interface of their
// own, and they change when the protocol work needs them to.
//
// # The three drawing paths
//
// A session uses one path at a time, and the choice is made at connect time by
// what the client advertises:
//
//   - Bitmap updates, the default. Rectangles of RLE or codec data decoded into
//     BGRA. Every server supports this and it is the path that works.
//   - Drawing orders, with Setting.EnableOrders. The client advertises MEMBLT
//     and the server stops sending bitmap updates entirely, drawing through its
//     bitmap cache instead, which is far smaller on the wire and far more work
//     to render.
//   - EGFX, with Setting.EnableEGFX. A dynamic virtual channel carries surfaces
//     and codec data, and the client composites. Again the server stops sending
//     bitmap updates.
//
// The last two cannot be mixed with the first in one session, which is why both
// are off by default: turning one on and meeting a codec that is not implemented
// gives a blank screen rather than a degraded one.
//
// # What it does not do
//
// It does not paint. There is no widget, no window and no pixel format
// conversion for a display, only the pixels as the protocol delivers them.
//
// It does not implement RemoteFX Progressive, which needs an arithmetic decoder,
// or H.264, whose framing is implemented but whose decoding is left to a caller
// through the codec.AVCDecoder interface, because a pure Go decoder is out of
// scope and cgo would change what this library is.
//
// It does not implement bitmap cache revision 3, and two of the drawing orders
// have no server here to be checked against. Both are recorded in the README
// rather than implied by silence.
//
// # What has been verified
//
// The connection, TLS, NLA, licensing, bitmap, RemoteFX, NSCodec, ZGFX, orders,
// EGFX, cursor, input and Display Control paths have all been run against a real
// Windows 10 host and against xrdp, and the codecs are checked byte for byte
// against libfreerdp rather than against expectations written by the same person
// who wrote the decoder. docs/windows-verification.md records what was checked
// and how, including the traps that have already been paid for once.
//
// "It builds" is not "it works", and this package's own history is the argument:
// five separate faults were found only by pointing the code at a server that
// disagreed, after unit tests had passed. When a layout or a value is in
// question, settle it against FreeRDP's implementation or against a capture
// rather than against the specification's prose, which has been ambiguous or
// wrong every time it was asked.
//
// # Traps
//
//   - Setting.EnableEGFX and Setting.EnableOrders are opt in, change how the
//     server draws, and cannot be mixed with the bitmap path in one session.
//   - Setting.EnableClipboard and Setting.EnableDisplayControl are opt in
//     because each opens a channel and changes the connect sequence. Without
//     them the clipboard and resize methods return an error rather than doing
//     nothing.
//   - A refused logon against an NLA server arrives looking like a TLS problem.
//     [client.ErrAuthenticationFailed] is what says otherwise; see the README
//     for the whole set of failure kinds.
//   - Change a capability and you change what the server sends. Order support
//     and the pointer cache size in particular are contracts: advertise a
//     pointer cache and the server stops resending shapes, so a client with
//     nowhere to put them loses the cursor.
//   - Input and the pointer are checked against a real server with tests a hover
//     cannot fake. A hover highlight, a tooltip or an already focused window all
//     look like success while the event is being dropped, which is how a button
//     flag and a cursor mask each stayed wrong for months.
//   - Logging a payload on a per packet path means [glog.Hex], never
//     hex.EncodeToString at the call site. The encoding has to happen after the
//     level is read, or it happens on every packet whether tracing is on or not.
//
// # Working on it
//
//	go build ./... && go vet ./... && go test ./... && go test -race ./...
//	CGO_ENABLED=0 go test ./...       // the library is pure Go
//	gofmt -l .                        // must print nothing
//	go run ./cmd/docaudit .           // exported identifiers with no doc comment
//
// docaudit takes directories, not package patterns: "./..." walks nothing and
// reports nothing missing, which is how this API was believed documented while
// it was not. The client package, the codecs, the orders, the commands and glog
// report no gaps; the protocol layers report several hundred, nearly all of them
// wire constants, and that number is meant to fall.
//
// scripts/dev-rdp.sh manages a local xrdp for integration testing and
// scripts/vnc-dev.sh fetches TigerVNC without root for the RFB side. The
// decoders are checked against libfreerdp by scripts/gen-codec-vectors.sh, which
// is why they can be trusted further than their own tests.
package grdp
