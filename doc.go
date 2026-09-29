// Package grdp is a Go client for Microsoft's Remote Desktop Protocol (RDP),
// with an RFB (VNC) client inherited from the upstream fork alongside it.
//
// # What it does
//
// It connects to a server, authenticates with TLS or NLA (CredSSP), negotiates
// the session, receives the desktop, and sends keyboard, mouse and clipboard
// data back. The desktop arrives on one of three drawing paths, and a session
// uses one of them at a time:
//
//   - bitmap updates, the default, decoded by core.Decompress into BGRA pixels;
//   - drawing orders, selected with Setting.EnableOrders and rendered into a
//     framebuffer by the orders package;
//   - EGFX surfaces, selected with Setting.EnableEGFX and delivered by
//     plugin/rdpgfx.
//
// The entry point is client.NewClient:
//
//	s := client.NewSetting()
//	s.Protocol = "nla" // "tls" (the default), "nla" or "rdp"
//	c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)
//	defer c.Close()
//	c.OnBitmap(func(bs []client.Bitmap) { /* paint bs */ })
//	if err := c.Login(); err != nil {
//		log.Fatal(err)
//	}
//
// # What it does not do
//
// It does not paint anything for you: each drawing path hands you pixels and
// leaves the rendering to the caller. It does not decode RemoteFX Progressive
// or H.264, and it does not use bitmap cache revision 3. The VNC client builds
// but has never been exercised against a VNC server. The example/ and
// cmd/rdpcli programs are demonstrations, not part of the library.
//
// # What is verified, and what is not
//
// The connection, NLA, licensing, bitmap, codec, order and EGFX paths have been
// run against a real Windows 10 host and against xrdp. docs/windows-verification.md
// records what was checked, how, and the traps that were already paid for once.
// Not verified: VNC, RemoteFX Progressive, H.264 and bitmap cache revision 3.
// "It builds" is not "it works": settle layout and value questions against
// FreeRDP or against a capture, which is the habit that document exists for.
//
// # Traps
//
//   - The module does not build with CGO_ENABLED=0. The plugin package, which
//     every RDP session loads, imports "C" while using no C, and that makes the
//     whole module cgo-only. See package plugin.
//   - Setting.EnableEGFX and Setting.EnableOrders are opt in, change how the
//     server draws, and cannot be mixed with the default bitmap path in one
//     session.
//   - Setting.EnableClipboard is opt in; without it the clipboard methods
//     return an error rather than doing nothing.
package grdp
