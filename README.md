# Golang Remote Desktop Protocol

grdp is a pure Go client implementation of Microsoft's Remote Desktop Protocol.

> [中文说明](README.zh-CN.md)

Forked from [tomatome/grdp](https://github.com/tomatome/grdp), itself forked from
[icodeface/grdp](https://github.com/icodeface/grdp).

## Status

The connection, authentication, rendering, input and clipboard paths are
implemented and verified against real servers, over both the legacy bitmap path
and the newer graphics channel.

Verified against **xrdp 0.9.24** and against a real **Windows 10** host:

* [x] Connection, MCS, capability exchange
* [x] TLS and NLA (CredSSP with NTLMv2, including the version 6 public key
      binding)
* [x] Standard RDP Security, the old path with no TLS: verified against a local
      xrdp configured for it, which `scripts/dev-rdp.sh standard` sets up. It
      produces output pixel-identical to the TLS path on the same target, which
      is a stronger check than "it connects".
* [x] Licensing exchange
* [x] Bitmap updates, RLE, 24 and 32 bpp
* [x] Pointer position and pointer shape updates
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
| Robustness | Fuzzing, which found two infinite loops and three panics; the corpus is kept |
| The shape, glyph, nine grid and multi rectangle orders | FreeRDP's parsers and unit tests only: no reachable server sends them |
| The AVC420 and AVC444 framing | FreeRDP's parsers and unit tests only: decoding needs a decoder this does not ship |

## Using it as a library

```go
s := client.NewSetting()
s.Width, s.Height = 1024, 768
s.Protocol = "nla"           // "tls" or "nla"

c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)
c.OnBitmap(func(bs []client.Bitmap) { /* paint */ })
c.OnClipboardText(func(text string) { /* paste */ })
if err := c.Login(); err != nil {
    log.Fatal(err)
}
c.KeyDown(0x1c, "")
c.KeyUp(0x1c, "")
```

`Setting.EnableClipboard` opens the clipboard channel, and
`Setting.EnableEGFX` opts into the graphics channel described above. EGFX is off
by default because a server that picks it stops sending bitmap updates: with it
on, an unsupported codec means a blank screen rather than a degraded one. Use
`OnSurfaceFrame` to receive the surfaces and `Surface.Origin` to place them.

`Setting.EnableOrders` advertises MEMBLT and renders the drawing orders that
follow, into a framebuffer reachable through `Client.Screen`, with
`OnOrdersFrame` reporting what changed. It is off by default because it changes
how the server draws and the two paths cannot be mixed: advertising MEMBLT stops
bitmap updates entirely. With it off the server stays on bitmap updates, which is
correct, just larger on the wire.

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

`go run ./cmd/docaudit ./...` lists exported identifiers that godoc will render
without a doc comment, which is the one thing that makes an API unpleasant to
read from `go doc` alone.

## Take ideas from

* [rdpy](https://github.com/citronneur/rdpy)
* [node-rdpjs](https://github.com/citronneur/node-rdpjs)
* [gordp](https://github.com/Madnikulin50/gordp)
* [ncrack_rdp](https://github.com/nmap/ncrack/blob/master/modules/ncrack_rdp.cc)
