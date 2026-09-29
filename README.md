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
      the ellipses, the multi rectangle orders and the glyph cache behind TEXT2.
      Verified against a real Windows server, where a whole lock screen comes
      through the cache, gradients and text included. It matches the bitmap path to the taskbar
      clock, which is where two runs of the same path differ too. Off by default,
      see below.
* [ ] VNC. The RFB client inherited from upstream is kept and still builds, but
      it is untested: it has never been run against a VNC server in this project,
      it has no tests, and its clipboard is not implemented.

Not done, or not finished:
* [ ] RemoteFX Progressive (codec ids 0x0009 and 0x000D), which needs its own
      arithmetic decoder. The `THINCLIENT` capability flag asks the server not
      to use it, and the dispatch error names it if a server does anyway.
* [ ] Interleaved and banded RLE bitmaps. Only bitmap cache revision 3 asks for
      them, and offering revision 3 to Windows changes nothing: it sends revision
      2 either way, which uses the same decoder bitmap updates do. So this is not
      implemented because nothing tested reaches it, not because it was skipped.
* [ ] H.264 (AVC420 / AVC444) codecs, which EGFX servers may choose. Not
      advertising them is what keeps them out of the negotiation.
* [ ] Scaled surface placement: a surface mapped with scale factors would need
      resampling. `MapSurfaceToOutput` is honoured, the scaled variants are not.

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
and `probe-session`, which prepares a session whose X input events are logged so
that input can be verified rather than eyeballed.

`scripts/gen-codec-vectors.sh` regenerates the codec test vectors from
libfreerdp's encoders and decoders, so the image codecs are checked against a
reference implementation rather than against hand written expectations.
`scripts/gen-zgfx-vectors.sh` does the same for ZGFX, except that the streams
come from our test encoder because FreeRDP's compressor is a stub; FreeRDP's
decoder still supplies the expected output.

`scripts/compare-shots.py` compares two screenshots of the same screen, taken
over different drawing paths, and reports whether they differ like a lossy codec
or like a region that never got drawn.

## Take ideas from

* [rdpy](https://github.com/citronneur/rdpy)
* [node-rdpjs](https://github.com/citronneur/node-rdpjs)
* [gordp](https://github.com/Madnikulin50/gordp)
* [ncrack_rdp](https://github.com/nmap/ncrack/blob/master/modules/ncrack_rdp.cc)
