# Golang Remote Desktop Protocol

grdp is a pure Go client implementation of Microsoft's Remote Desktop Protocol.

> [中文说明](README.zh-CN.md)

Forked from [tomatome/grdp](https://github.com/tomatome/grdp), itself forked from
[icodeface/grdp](https://github.com/icodeface/grdp).

## Documentation

* [docs/porting-guide.md](docs/porting-guide.md) is for building something with
  this library: the session from connect to disconnect, what each part expects of
  you, and the traps that have already been paid for once.
* [docs/protocol-layers.md](docs/protocol-layers.md) is a map of the layers for
  someone porting or extending it: what each package does and does not do, what
  each layer relies on the one below it for, and where to look first when
  something breaks.
* [docs/windows-verification.md](docs/windows-verification.md) records what was
  checked against a real Windows host and how, including the symptoms worth
  recognising again.
* [docs/input-verification.md](docs/input-verification.md) records how input is
  proved to have arrived rather than inferred from a screenshot.

Every package has a package doc, and every exported identifier has a doc comment;
`go doc ./client` is a reasonable place to start.

## Status

The connection, authentication, rendering, input and clipboard paths are
implemented and verified against real servers, over both the legacy bitmap path
and the newer graphics channel.

Verified against **xrdp 0.9.24** and against a real **Windows 10** host:

* [x] Connection, MCS, capability exchange
* [x] Display Control: the desktop can be resized while the session runs. The
      channel is asked for by the client, the layout waits for the server's
      capabilities, `OnResize` reports the size the server actually chose - it
      rounds to a mode it has, so 720 comes back as 768 - and the framebuffer
      follows. Verified against a real Windows 10 host, where the desktop
      resizes and the two composites still agree to 0 of 983040 pixels.
* [x] TLS and NLA (CredSSP with NTLMv2, including the version 6 public key
      binding)
* [x] Standard RDP Security, the old path with no TLS: verified against a local
      xrdp configured for it, which `scripts/dev-rdp.sh standard` sets up. It
      produces output pixel-identical to the TLS path on the same target, which
      is a stronger check than "it connects".
* [x] Licensing exchange
* [x] Bitmap updates, RLE, 24 and 32 bpp
* [x] Pointer position, and pointer shapes decoded into a cursor: the AND and
      XOR masks are applied, the 20 slot cache the capability set advertises is
      implemented so a cached shape resolves instead of vanishing, system
      cursors are reported as such, and a gateway gets RGBA with alpha from
      OnCursor. Verified against xrdp, which sends two colour cursors and whose
      shapes come back with the hotspots a real cursor has (arrow at 0,0, I beam
      at 4,8) rather than garbage, and against a real Windows 10 host, whose
      41x39 arrows and I beams come back with 145 to 352 opaque pixels each.
* [x] Keyboard and mouse input, including modifier keys
* [x] Clipboard, text, both directions
* [x] NSCodec and RemoteFX (RFX) bitmap codecs, checked byte for byte against
      libfreerdp's own decoder
* [x] ZGFX, the bulk compression the EGFX channel wraps its messages in, also
      checked byte for byte against libfreerdp, including against captures of
      real Windows traffic
* [x] Surface commands, `drdynvc`, RDPGFX command parsing
* [x] EGFX (RDPGFX) rendering, verified against a real Windows server: the
      desktop, its icons and text come through over the graphics channel with
      no bitmap updates at all. Off by default, see below.
* [x] Drawing orders: the bitmap cache and MEMBLT, with the bounds an order
      carries as a clip, plus PATBLT with its brush, the polygons, the polyline,
      the ellipses, the multi rectangle orders, the glyph cache behind TEXT2,
      LineTo, SaveBitmap and FastIndex. Verified against a real Windows server,
      where a whole lock screen comes through the cache, gradients and text
      included. It matches the bitmap path to the taskbar
      clock, which is where two runs of the same path differ too. Off by default,
      see below.
* [x] VNC, against a real TigerVNC server: a frame is received and parsed, and
      the clipboard works in both directions. `scripts/vnc-dev.sh` fetches
      TigerVNC without root and runs the live tests. Raw, CopyRect and Hextile
      encodings, and `RequestClipboardText` is not implemented, which a test
      records. The live tests have seen TigerVNC choose Hextile for ordinary
      rectangles and CopyRect for a real scroll, and check that the copied pixels
      are the ones the source held.

Not done, or not finished:
* [ ] RemoteFX Progressive (codec ids 0x0009 and 0x000D), which needs its own
      arithmetic decoder. The `THINCLIENT` capability flag asks the server not
      to use it, and the dispatch error names it if a server does anyway.
* [ ] Interleaved and banded RLE bitmaps. Only bitmap cache revision 3 asks for
      them, and offering revision 3 to Windows changes nothing: it sends revision
      2 either way, which uses the same decoder bitmap updates do. So this is not
      implemented because nothing tested reaches it, not because it was skipped.
* [ ] Decoding H.264. The framing is implemented, with an `AVCDecoder` interface
      for a caller to supply one, and the codecs are advertised only when it is
      set: a pure Go decoder is out of scope and cgo would change what this
      library is. Without one nothing is advertised, so the negotiation is
      unchanged.
* [ ] Windowed surface placement: `MapSurfaceToScaledOutput` is applied, with the
      pixels resampled to the size the server asked for, but the two window
      variants are recorded and exposed rather than applied, because there is no
      window to place anything in.
* [ ] Compressed dynamic virtual channel data is refused with an error rather than
      decoded, which is visible rather than silent, and the negotiation does not
      ask for it.
* [ ] The shape, glyph and nine grid orders have no server to be verified
      against. A Windows 10 host was watched with every one of them advertised
      and sent 1473 MEMBLTs and nothing else, and xrdp sends no orders at all.
      They are implemented from FreeRDP's parsers and unit tested, which is the
      honest extent of it, and for that reason the capability advertisement
      offers MEMBLT alone: asking an older server for orders never watched
      working would be worse than leaving it on bitmap updates.

## What a failed login looks like

Login failures are classified, because the message alone does not say which kind
it was and the most common one is the most misleading. Against an NLA server a
wrong password does not come back as an authentication error: Windows does not
answer a bad logon, it drops the TLS connection, so what arrives is
`tls: internal error`, which reads like a certificate problem and sends an
operator looking in the wrong place.

| What happened | The error | How to test it |
| --- | --- | --- |
| The password was refused | wraps `ErrAuthenticationFailed` | `errors.Is(err, client.ErrAuthenticationFailed)` |
| The connection could not be established at all | wraps `ErrUnreachable` | `errors.Is(err, client.ErrUnreachable)` |
| ...and why it could not | the `*net.OpError` is still in the chain | `errors.Is(err, syscall.ECONNREFUSED)` for nothing listening, `EHOSTUNREACH` for the network |
| The TLS handshake failed, so nothing was sent | wraps `ErrTLSFailure` | `errors.Is(err, client.ErrTLSFailure)` |
| The CredSSP exchange failed before the credentials were sent | wraps `ErrCredSSP` | `errors.Is(err, client.ErrCredSSP)` |
| The server wants a different security protocol | `*x224.NegotiationFailure`, with a code | `errors.As(err, &x224.NegotiationFailure{})` |
| The server ended the session, usually because another connection took it over | wraps `ErrSessionEndedByServer` | `errors.Is(err, client.ErrSessionEndedByServer)` |

The four kinds that involve the account are separated by the stage they happen
in, because that is the only thing the transport can know for certain. Before the
credentials are sent, a failure is `ErrTLSFailure` or `ErrCredSSP` and the account
has not been proven wrong. After they are sent and the server goes quiet, it is
`ErrAuthenticationFailed`, which is what a refused logon looks like from here.

`ErrTLSFailure` was unreachable until recently: TPKT armed its first read before
the TLS handshake existed, so a handshake failure was reported by the read loop as
a plain transport error while the code that would classify it was still inside its
own read. That is fixed, and the sentinel was verified with a stub that reaches
the handshake and fails it. `ErrCredSSP` is the same code path one step further
along and is unit tested but not verified; docs/protocol-layers.md has the
recipe.

```sh
$ rdpcli -host host -user user -pass wrong -proto nla
login failed: client: the server did not accept the credentials: nla: the CredSSP
              authentication exchange failed: read header: remote error: tls: internal error
  kind: authentication — the exchange did not complete, which is usually a rejected password
```

`ErrAuthenticationFailed` says the exchange did not complete, and deliberately
does not say the password was wrong: a server that vanished in the middle of the
exchange looks the same. What it does rule out is TLS itself, since a certificate
problem fails before the exchange begins.

The last of those is the one that looks most like a fault and is not: a session
taken over by another connection is still on the server, detached, and connecting
again gets it back. A dropped network sends no disconnect ultimatum at all, so
the presence of one is what tells the two apart, and that is a fact rather than a
reading of the bytes.

The one case with no signal at all is a server that accepts the connection and
then reports the failure *inside the session*, which is what xrdp does: it shows
a `login failed for user` dialog as pixels and sends nothing on the wire. There
is no protocol-level way for a client to notice that, and inventing one — reading
the screen — is not something this library is going to do.

## A headless gateway

`cmd/rdpws` is a gateway in one file: it holds an RDP session, serves the screen
to a browser as dirty rectangles encoded to JPEG over a WebSocket, and takes the
browser's keyboard, mouse and pointer back the other way. It is the example the
library was missing, since the two that shipped were a desktop program and a
socket.io page from its early days, and neither showed how to drive a session
with no window in front of it.

```sh
go run ./cmd/rdpws -host 192.0.2.10 -user user -pass secret
# then open http://127.0.0.1:8080/
```

It is also a test that can be run rather than read, which is the point of it:

```sh
go run ./cmd/rdpws -host 127.0.0.1 -user rdptest -pass rdptest -selftest
selftest: ok — page has a canvas, size announced, 12881 bytes as JPEG (1024x768),
                2 cursor images, 1 frames encoded
```

That connects, serves, drives a frame and a pointer image through the WebSocket a
browser would use, decodes them, and sends input back. It is what exercises
`Framebuffer` and `OnFrame`, `OnCursor` and `TypeText` together, so the three
things a gateway needs are in one place that runs.

## What is verified, and how

The distinction this table draws is the point of the repository. Every claim in
the list above is in the first column or the second, and nothing is in the first
without having been run against something that did not write the test.

| | How it is known |
| --- | --- |
| Connection, TLS, NLA, licensing, input, clipboard, bitmap updates | Run against xrdp 0.9.24 and a real Windows 10 host |
| Standard RDP Security | Run against a local xrdp requiring it, and pixel-identical to the TLS path on the same target |
| EGFX rendering | A whole Windows desktop through the graphics channel, compared against the bitmap path |
| Drawing orders | A whole Windows lock screen through the bitmap cache, and the two drawing paths agree to the taskbar clock |
| VNC | A real TigerVNC server: a frame arrives, and the clipboard works both ways |
| NSCodec, RemoteFX, ZGFX | Byte for byte against libfreerdp, whose encoder produced the input and whose decoder produced the expected output |
| Display Control | A real Windows 10 host resizes its desktop on request, and the framebuffer follows to 1280x768 |
| Cursors on Windows | A real Windows 10 host: 41x39 shapes, hotspots at the arrow's tip, and the AND mask rule that xrdp's two colour cursors never exercise |
| Robustness | Fuzzing, which found two infinite loops and three panics; the corpus is kept |
| The shape, glyph, nine grid and multi rectangle orders | FreeRDP's parsers and unit tests only: no reachable server sends them |
| The AVC420 and AVC444 framing | FreeRDP's parsers and unit tests only: decoding needs a decoder this does not ship |

## Using it as a library

```go
s := client.NewSetting()
s.Width, s.Height = 1024, 768
s.Protocol = "nla"           // "tls", "nla" or "rdp"

c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)

// Where the pixels are: one place, whichever way the server chooses to draw.
c.OnFrame(func(dirty []image.Rectangle) { /* encode exactly these regions */ })
c.OnCursor(func(cur *client.Cursor) { /* draw cur.Image, positioned by cur.Hotspot */ })
c.OnCursorPos(func(x, y int) { /* and put it here */ })
c.OnClipboardText(func(text string) { /* paste */ })

if err := c.Login(); err != nil {
    log.Fatal(err)
}

fb := c.Framebuffer()        // BGRA, top down, valid until the next OnFrame
c.MouseMove(400, 300)
c.MouseDown(0, 400, 300)
c.MouseUp(0, 400, 300)
c.TypeText("text", 8*time.Millisecond)  // characters, so an input method works
```

`OnFrame` reports each batch of updates once, with the dirty rectangles already
merged and clipped, so a caller encodes what changed rather than guessing. The
pixels come from `Framebuffer` whatever path the server used, and the dirty
rectangles are what changed rather than the whole screen.

The pointer is not part of the desktop: `OnCursor` gives it as an image with
alpha, plus a hotspot to position it by, and it fires on a shape change and not
on movement.

Opt-ins, all off by default because each changes what is negotiated:

* `Setting.EnableClipboard` opens the clipboard channel, for text both ways.
  `Client.Files` is the file transfer surface, which takes a provider for the
  files to share and reads the ones the server offers by range.
* `Setting.EnableDisplayControl` opens Display Control, so `RequestResize` can
  change the desktop size while the session runs. The answer arrives on
  `OnResize`, and it is the answer that counts: the server rounds to a mode it
  has, so asking for 720 can give 768.
* `Setting.EnableEGFX` opts into the graphics channel. With it on, an unsupported
  codec means a blank screen rather than a degraded one, because a server that
  picks EGFX stops sending bitmap updates entirely.
* `Setting.EnableOrders` advertises MEMBLT and renders the drawing orders that
  follow. It changes how the server draws, and the two paths cannot be mixed.

`OnBitmap`, `Screen`, `OnOrdersFrame` and `OnSurfaceFrame` still work for callers
that want the raw updates or the orders machinery, and are deprecated where
`Framebuffer` and `OnFrame` supersede them.

## Trying it out

`cmd/rdpcli` is a headless client that connects, decodes, and writes what it
received to a PNG. It is enough to check a server end to end:

```sh
go run ./cmd/rdpcli -host 192.0.2.10:3389 -user user -pass secret -proto nla \
    -wait 20s -dump /tmp/screen.png

# drive input and watch it land
go run ./cmd/rdpcli -host 192.0.2.10:3389 -user user -pass secret -proto nla \
    -wait 20s -dump /tmp/after.png -post-input 4s \
    -key click:400,600 -type 'echo hello' -key press:0x1c
```

Flags worth knowing: `-type` types a string (handling shift), `-key` sends raw
input events, `-clipboard` and `-set-clipboard` exercise the clipboard,
`-egfx` and `-orders` switch the drawing path, `-pointer` logs pointer updates,
and `-log 0` is a full trace.

## How the verification was done

`docs/windows-verification.md` records what was checked against a real Windows
host and, more usefully, how: which symptom means the protocol is at fault and
which means the account is, where a capture settled a question the specification
left ambiguous, and which mistakes were made on the way. The codecs and ZGFX are
checked byte for byte against libfreerdp rather than against expectations written
by the same person who wrote the decoder, which is how the interpretations behind
several bugs were caught.

## Development

`scripts/dev-rdp.sh` manages a local xrdp target for integration testing:
`setup` (once, makes the rest passwordless), `up`, `down`, `status`, `probe`,
`probe-session` (which prepares a session whose X input events are logged so that
input can be verified rather than eyeballed), and `standard`, which restarts xrdp
requiring Standard RDP Security so that path can be tested too.

`scripts/gen-codec-vectors.sh` regenerates the codec test vectors from
libfreerdp's encoders and decoders, so the image codecs are checked against a
reference implementation rather than against hand written expectations.
`scripts/gen-zgfx-vectors.sh` does the same for ZGFX, except that the streams
come from our test encoder because FreeRDP's compressor is a stub; FreeRDP's
decoder still supplies the expected output.

`scripts/compare-shots.py` compares two screenshots of the same screen, taken
over different drawing paths, and reports whether they differ like a lossy codec
or like a region that never got drawn.

`scripts/vnc-dev.sh` fetches TigerVNC without root, starts it on a private
display, and runs the live VNC tests against it.

`cmd/rdpws -selftest` is the integration test for the headless path above.

`go run ./cmd/docaudit ./...` lists exported identifiers that godoc will render
without a doc comment, which is the one thing that makes an API unpleasant to
read from `go doc` alone.

## Take ideas from

* [rdpy](https://github.com/citronneur/rdpy)
* [node-rdpjs](https://github.com/citronneur/node-rdpjs)
* [gordp](https://github.com/Madnikulin50/gordp)
* [ncrack_rdp](https://github.com/nmap/ncrack/blob/master/modules/ncrack_rdp.cc)
