# Verification against real Windows

This records what was checked against a real Windows target, and the two
symptoms worth recognising again.

## Target

A Windows 10 (build 19041) virtual machine reached over the local network, with
NLA required:

```
192.168.75.130:3389   NLA required (a TLS-only connection is refused with
                      HYBRID_REQUIRED_BY_SERVER, 0x00000005)
```

## What works

Verified end to end, using only this library:

* **NLA / CredSSP** completes, including the version 6 public key binding.
* **The desktop renders**: a full Windows session, taskbar, wallpaper and a
  `Command Prompt` window running `ipconfig`, decoded from RLE bitmap updates.
* **Mouse and keyboard input** reach the session. Clicking the console window
  focuses it, and typing

  ```
  echo HELLO-FROM-GRDP
  ```

  produces the expected output on screen, so modifier keys (the upper case
  letters need shift) and Enter are all delivered correctly.

## Symptom: the server rejects the logon, not the protocol

If CredSSP gets through its public key exchange and then the connection is
reset, the interchange is not at fault. The exact shape is:

* the server announces version 6 and accepts the client's `pubKeyAuth`,
* it answers with a 48 byte seal (signature plus the sealed 32 byte hash),
* the client verifies that reply successfully,
* the client sends its credentials, sends the X.224 connection request, and the
  server answers with `connection reset by peer`.

Steps 1 to 4 are pure NTLM and prove the session keys, the seal format and the
binding hash are all correct. Step 5 is where Windows performs the actual
interactive logon, so a reset at that point means the account may not log on
over Remote Desktop. SMB authenticating with the same credentials does not rule
this out, because SMB only needs a network logon.

On this target the fix was to add the account to the local **Remote Desktop
Users** group. A TLS alert (`remote error: tls: internal error`) instead of a
reset is a different thing and does point at the wire format.

## EGFX is offered, and its payload was the missing piece

With `Setting.EnableEGFX` the dynamic channel layer negotiates correctly
against Windows (which offers version 3) and the server asks for

```
Microsoft::Windows::RDS::Graphics
```

It then stops sending bitmap updates entirely.

The reason nothing was understood is that the payload is not a bare RDPGFX PDU:

```
e0 24 ...
│  └─ 0x24 = PACKET_COMPRESSED | RDP8
└─ 0xE0 = ZGFX_SEGMENTED_SINGLE
```

Every message on that channel is a ZGFX stream, the RDP 8.0 bulk compression
(MS-RDPEGFX 3.1.8), whether or not it is actually compressed. FreeRDP's client
calls `zgfx_decompress` on every message it receives there without checking, so
the framing is mandatory rather than optional.

Two nearby traps that were checked and are **not** at fault:

* channel data in general is fine against Windows, the clipboard channel parses
  its capabilities and monitor ready PDUs correctly;
* the platform is only a problem for the legacy path, `Setting.EnableEGFX` is
off by default so Windows keeps using bitmap updates.

## EGFX now renders

With ZGFX unwrapped, the graphics channel decodes and the desktop appears: the
wallpaper, the icons, the taskbar and the watermark text, all from surfaces
rather than from a single bitmap update.

What is worth recording is how the last bug was found, because it is a testing
lesson rather than a protocol one.

Every `WireToSurface` was being dropped with a nonsense length (`0xC000001D`).
The header the parser expected and the PDUs the tests wrote had the same
mistake: both had a byte between the pixel format and the destination
rectangle, so the tests passed and the parser was wrong in exactly the way its
tests were. The specification and FreeRDP's struct both say the fields add up to
`RDPGFX_WIRE_TO_SURFACE_PDU_1_SIZE`, which is 17, with no gap. A capture settled
it: the rectangle is `(971,728,1019,768)`, the declared length is exactly the
bytes that follow, and the RemoteFX sync marker sits at offset 17.

So `plugin/rdpgfx/testdata` holds those captured messages, and the test that
replays them checks that pixels land inside the rectangle and nowhere else. Hand
written bytes cannot catch a misunderstanding that the bytes were written from.

## Clipboard, both ways

Verified against the same Windows host, each direction driven from inside the
session rather than assumed:

* publishing to the server, then `powershell -command Get-Clipboard` in the
  session prints back exactly what was published;
* running `echo SERVER-SIDE-TEXT| clip` in the session, then asking the client
  for the clipboard, yields `SERVER-SIDE-TEXT\r\n` (the `clip` tool adds the
  line ending, and it is passed through unchanged).

## Orders: used by Windows, declined by us

Orders are the RDP 4/5 era drawing commands that draw GDI primitives and blit
from a bitmap cache. This client parses them and does not render them, and it
turns out the second half of that is a choice rather than an accident.

The `OrderCapability` we send has an all zero 32 byte `orderSupport` array, which
tells the server we can draw none of the order types. Filling it in changes what
arrives completely:

| | `orderSupport` all zero | `orderSupport` populated |
| --- | --- | --- |
| Orders updates (0x00) | 0 | 144 |
| Bitmap updates (0x01) | 140 | 0 |

So Windows will happily switch to orders, and the traffic becomes 144 orders
updates instead of 140 bitmap updates, each carrying many primitives. What it
sends, counted over one session with a little clicking around:

| order | count |
| --- | --- |
| primary `ORDER_TYPE_MEMBLT` (13), blit from the bitmap cache | 1770 |
| secondary `ORDER_TYPE_BITMAP_COMPRESSED_V2` (5), fill the cache | 1000 |
| alternate secondary | 62 |
| primary `ORDER_TYPE_SCRBLT` (2) | 1 |

That is the whole drawing model: put a bitmap in the cache, then blit it to the
screen with MEMBLT. Nothing else matters much, and in particular the cache is
revision 2, so the interleaved RLE that revision 3 needs does not come up.

Two consequences worth recording:

* declining orders is safe and the current sessions are correct because of it.
  What it costs is efficiency, not correctness, so implementing orders is an
  optimisation rather than a fix;
* xrdp sends no orders even when asked, so this had to be found on Windows.

The placement of the `orderSupport` bits, the cache revision actually used, and
the order types that matter were only visible from a capture. Any implementation
should be built against one, the way the codecs were.

### Where an implementation stands

The parsed cache bitmaps are now kept, in `CacheBitmap` on the secondary order,
with compressed entries decoded on arrival so a blit is a copy. That much is
unit tested. Two things are known and not done.

**The cache order's fields are not fixed width, which the parser assumed.** A
cache order's header looked like it had one byte too many in front of the RLE
data, and it turned out to be our own: `bitmapWidth`, `bitmapHeight` and
`cacheIndex` use MS-RDPEGDI's compact form, one byte when the value fits in seven
bits and two when the top bit of the first says otherwise, and `bitmapLength`
uses the four byte form where the top two bits say how many bytes follow.
Reading them as fixed width stole a byte from the bitmap.

The captured order shows it plainly:

```
40 42 01 ff ff 10 f2 11 ...
│  │     │  └─ cacheIndex: (0x7f<<8)|0xff = 0x7fff, the waiting list
│  │     └──── (the first of those two bytes)
│  └─ bitmapLength: ((0x42 & 0x3f) << 8) | 0x01 = 513, two bytes because
│      the top bits are 01
└─ bitmapWidth: 0x40 = 64, one byte
```

518 bytes, which is 1 + 2 + 2 + 513, and the RLE data starts with `10` exactly as
a bitmap update does. So there was never a second compression format: the
`0xff` was the low byte of a two byte `cacheIndex`.

The parser reads the compact forms now, a captured order is a test fixture, and
that test checks the checksum of the decoded pixels. A shift of one byte changes
the image, and "it decoded to something" would not have noticed.

**MEMBLT has no raster operation, and the parser read its colour index as one.**
The field at present bit 0x0020 is `TS_NEG_COLORINDEX_INDEX`, a colour index for
palettised bitmaps; MEMBLT always copies. Reading it as a ROP made every MEMBLT
look like an unhandled BLACKNESS fill, which is exactly what a black screen
would have suggested too.

**The field flags of a primary order are read from the bottom up.** It is
tempting to read the two control flags that shorten them as meaning the low byte
is absent and the following bytes shift up to leave room for a zero. The wire
says otherwise: a MEMBLT carrying one flag byte carries `0x02`, which is the left
coordinate, and shifting it up gives `0x200`, which is bit nine of a nine bit
field and so names nothing at all. The bytes that remain are read from the bottom
up.

**An order with delta coordinates repeats the previous order's fields.** This is
the one that took longest to see, because nothing about it looks like a bug: with
`TS_DELTA_COORDINATES` set, the fields an order leaves out are not zero, they are
whatever the previous order of the same type carried. Parsing into a fresh struct
loses them, and the symptom is not a parse error but a MEMBLT that omits its
cache id, reads cache 0, and finds nothing there. The parser keeps the last order
of each type now and fills the gaps from it.

**`decompress4` compared the wrong two numbers.** It ended with
`return size == total`, where `size` is the size of the decoded bitmap and
`total` is how many compressed bytes were consumed, so it reported failure for
every 32bpp bitmap. Nobody noticed because the caller ignored the result. It now
returns whether the planes decoded, and the caller checks it.

**The bitmap decompressor used to hang on malformed input.** Handing
`core.Decompress` a short compressed payload did not return an error, it did not
return, and it did not stop: a four byte run of `{0x10, 0, 0, 0}` ran for over
five minutes before the test was killed. That was reachable from the network,
since the same decoder serves bitmap updates.

The cause was the interleaved decoder specifically. It reads a code and then a
run, and once the input is gone every read returns zero, which decodes as a run
of zero pixels: the loop advanced neither the input nor the output, so it never
finished. It now notices exhaustion, clamps runs to the line so they cannot walk
past the buffer, and reports what happened. The fuzzer covers it, and ran 8.7
million cases in 45 seconds without stalling, where the four byte case used to
never return at all.

Worth knowing: only the interleaved form can tell that a stream ended early,
because it carries an explicit size. The 8, 16 and 24 bit forms are terminated
by the buffer ending, so a short stream is not distinguishable from a complete
one and no error is reported for them.

## The two drawing paths agree

The legacy path sends lossless RLE bitmaps and EGFX sends RemoteFX, which is
lossy, so the two are not expected to produce identical pixels. Comparing them is
still worth doing, as long as the comparison says which kind of difference it is:
noise spread thinly, or a region that never got drawn.

`scripts/compare-shots.py` reports that. Run the same session twice, once with
`-egfx` and once without, and compare; then compare two runs of the *same* path,
which is the control and establishes the noise floor.

Against a Windows desktop, unchanged between runs:

| comparison | mean channel difference | pixels differing visibly |
| --- | --- | --- |
| bitmap vs bitmap (the control) | 0.031 of 255 | 169, all inside the taskbar clock |
| bitmap vs EGFX | 2.886 of 255 | 1438 of 786432 (0.18%) |

The control is the useful half. Two runs of the same path differ in 169 pixels,
every one of them in the clock, so the method's noise floor outside the clock is
nothing. Against that, EGFX differs by an average of about one count per channel
and only 0.18% of pixels by more than sixteen: a lossy codec doing its job, not a
region left blank. A surface that was never drawn would show up as a large solid
patch, and there is none.

Comparing sessions that involved input did not work, and the reason is worth
recording because it is a trap: the runs are not independent. Keystrokes land in
whichever window has focus, so the second run inherits the first one's windows and
ends in a different state. The screenshots then differ by a whole window, which
looks like a rendering fault and is not one. A comparison that involves input
needs each run to start from a state it established itself.
