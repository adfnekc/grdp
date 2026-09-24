# Golang Remote Desktop Protocol

grdp is a pure Go client implementation of Microsoft's Remote Desktop Protocol.

Forked from [tomatome/grdp](https://github.com/tomatome/grdp), itself forked from
[icodeface/grdp](https://github.com/icodeface/grdp).

## Status

The connection, authentication, rendering and input paths are implemented and
have been verified against real servers. The graphics paths that newer servers
prefer are not done yet.

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
* [x] Surface commands, `drdynvc`, RDPGFX command parsing

Not done:

* [ ] EGFX rendering. Windows offers the graphics channel and then stops sending
      bitmap updates, and the payload is ZGFX compressed, which is not
      implemented yet. See `docs/windows-verification.md`. Off by default.
* [ ] Orders and the bitmap cache. xrdp never sends orders, so this path is only
      needed for Windows' partial updates.
* [ ] Interleaved RLE bitmaps, which the bitmap cache revision 3 needs.
* [ ] H.264 (AVC420 / AVC444) codecs, which EGFX servers may choose.
* [ ] VNC. The RFB subset this fork inherited is unused and unverified.

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
`Setting.EnableEGFX` opts into the graphics channel described above.

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
`-pointer` logs pointer updates, `-log 0` is a full trace.

## Development

`scripts/dev-rdp.sh` manages a local xrdp target for integration testing:
`setup` (once, makes the rest passwordless), `up`, `down`, `status`, `probe`,
and `probe-session`, which prepares a session whose X input events are logged so
that input can be verified rather than eyeballed.

`scripts/gen-codec-vectors.sh` regenerates the codec test vectors from
libfreerdp's encoders and decoders, so the image codecs are checked against a
reference implementation rather than against hand written expectations.

## Take ideas from

* [rdpy](https://github.com/citronneur/rdpy)
* [node-rdpjs](https://github.com/citronneur/node-rdpjs)
* [gordp](https://github.com/Madnikulin50/gordp)
* [ncrack_rdp](https://github.com/nmap/ncrack/blob/master/modules/ncrack_rdp.cc)
