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

## The disconnect ultimatum's reason byte is not decoded

A session taken over by another connection is reported as the server ending it,
which is certain, and not as a takeover, which is not. The reason byte that would
have to be read to say is passed through raw.

Against a local xrdp, the takeover arrives as exactly five bytes:

    02 f0 80 | 21 80
    X.224 DT | mcsDPum

`21` is the option byte naming disconnectProviderUltimatum, as the existing
header check reads it, and the single byte of PER-encoded content is `80`. That
is the reason, and `0x80` is not a value a five alternative Reason can hold.

The disagreement is in the references: T.125 declares
`Reason ::= CHOICE { rn-domain-disconnected NULL, rn-provider-initiated NULL,
rn-token-purged NULL, rn-user-requested NULL, rn-channel-purged NULL, ... }` while
other renderings of the same ASN.1, including the one Wireshark generates from,
declare an ENUMERATED, and an annotated dump in MS-RDPBCGR shows
`reason = rn-user-requested (3)` as one octet. None of those produces `0x80`.

So the classification rests on what is not in dispute - an ultimatum arrived, so
the server ended the session - and the byte is exposed as it came. Working out
the encoding is a job for a capture of a client that works, which is the same
method that settled the questions above it.

## Display Control: the client has to ask, and the id spaces collide

Two faults were fixed here, and the second is the kind that only a server which
waits to be asked can reveal.

**Nothing ever asked.** A dynamic channel can be created by either side, and this
client only ever listened: `DvcClient.Open` existed and had no callers, so it
waited for channels the server opened. EGFX works that way, which is why it
looked correct. Display Control is a client-to-server feature, so Windows waited
for a request that never came. With `Open` called once the capability exchange
finishes, against this host:

    drdynvc: asked for channel id=1 name="Microsoft::Windows::RDS::DisplayControl"
    disp: channel 10 open
    disp: server caps monitors=16 area=8192x8192

Those capabilities are the confirmation that the parse is right: 16 monitors and
8192x8192 are FreeRDP's own defaults, which is where the expected values came
from, and they were not used to produce this.

**The name needs its NUL terminator.** Without one, the request is answered with
silence: no refusal, no error, nothing. With one, the channel opens. The unit test
for `Open` asserted the name without the terminator and passed, because the only
server it had ever been tested against was a test.

What is left. The resize still does not take effect, and the log shows why to
look at next: after our request, Windows creates a conversation of its own

    server requests id=10 name="Microsoft::Windows::RDS::Video::Data::v08.01"
    server requests id=11 name="Microsoft::Windows::RDS::Geometry::v08.01"
    server requests id=13 name="Microsoft::Windows::RDS::Input"
    server requests id=10 name="Microsoft::Windows::RDS::DisplayControl"

including Display Control itself, on an id of its choosing, and ids are reused
across those requests. Two directions each created the same channel on their own
numbering, so which id the layout should be sent on, and whether the server's own
DisplayControl channel is the one to use, is the question to settle next. That is
where a capture of a working client would answer it directly.

## Display Control: the dynamic channel alone was not created

What is settled. With `Setting.EnableDisplayControl` and nothing else, the
connect sequence advertises exactly one channel, `drdynvc`, and Windows never
creates the dynamic channel: `Microsoft::Windows::RDS::DisplayControl` does not
appear in the exchange, and `RequestResize` reports "the display control channel
is not open".

Advertising the **static** `disp` channel as well — which is what FreeRDP does,
since it carries both `DISP_CHANNEL_NAME "disp"` and
`DISP_DVC_CHANNEL_NAME "Microsoft::Windows::RDS::DisplayControl"` — gets further.
The connect sequence now carries two channels and this host joins both:

    clientNetworkData: {ChannelCount:2 [{Name:drdynvc ...} {Name:disp ...}]}
    sec on connect: [{1003 global} {1007 user} {1004 drdynvc} {1005 disp}]

With that, `RequestResize` sends: the layout goes out on the static channel and
rdpcli prints "asked the server to resize to 1280x720". So the channel is up and
the message is framed and written as a static channel payload.

What is not settled. The host does nothing with it. The desktop stays 1024x768,
`OnResize` never fires, and the server sends nothing back on the channel at all:
no `DISPLAYCONTROL_CAPS_PDU`, which it would send if it were treating the channel
as Display Control. Three channel option sets were tried —
`INITIALIZED|ENCRYPT_RDP`, that plus `COMPRESS_RDP`, and `INITIALIZED` alone —
with no difference, which rules out the options being the whole story but not
much else.

So the layout bytes are checked field by field against FreeRDP's sender and round
tripped through their own reader, the channel is joined, and the message is sent,
and the host still ignores it. The next step is the one that has settled every
other question in this file: **capture what a client that works sends**. Point
mstsc or FreeRDP at this host, resize the window, and compare its connect
sequence and its display control PDUs against these. Guessing has lost too often
to start now.

## Symptom: keyboard input that reaches nothing

Mouse input works on this target: clicking a desktop icon highlights it and shows
a tooltip. Typing produces no visible effect anywhere, including in the Start
menu's search box after `Ctrl+Esc`, which does open it.

The control that matters: typing over the **slow** path behaves identically to the
fast path, and the search box region is byte for byte the same in both. So this is
the shell not taking keyboard focus, and not a fault in the input path or in
Unicode key events. Without that comparison the obvious conclusion would have been
that the Unicode path was broken, which it is not: it is verified byte for byte
against FreeRDP's parser and unit tested, and this target simply cannot say.

One more hazard worth knowing when comparing screenshots of this host: its
wallpaper rotates. Two runs a minute apart differ in about 85% of their pixels
because of it, which looks exactly like a total failure and is nothing at all.
Compare a crop, or compare runs that are seconds apart.

## Symptom: `DISCONNECT_PROVIDER_ULTIMATUM` and typed input

A connection that gets `MCS DISCONNECT_PROVIDER_ULTIMATUM` a few seconds in, and
that survives while nothing is typed, is not a protocol fault. Look at the
screen: on this target it showed

    Another user is signed in. If you continue, they'll be disconnected.
    Do you want to sign in anyway?          [ Yes ]  [ No ]

The console session was signed in, so every RDP connection met this prompt, whose
default button is **No**. Typed keys were answering it: any Enter or space
answered No, and the server then disconnected. Clicking **Yes** at (441, 524) on
a 1024x768 desktop takes the session, and the desktop appears — a burst of about
190 bitmap rectangles is what that looks like — but the console reclaims it
within a few seconds on this target, so anything needing longer than that has to
wait for a host whose console is signed out.

The lesson generalises: a session that disconnects when you type and not when you
do not is a session whose input is going somewhere other than where it was meant
to. Reading the screen answers it in one look, and the answer is worth the look
because nothing about the protocol is wrong.

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

### Orders now render

The parsed cache bitmaps are kept on the secondary order, with compressed entries
decoded on arrival so a blit is a copy, and the renderer draws MEMBLT, DSTBLT,
SCRBLT and OPAQUERECT with the bounds an order carries as a clip. A session comes
out whole: wallpaper, icons, the text in a console window, taskbar and clock, all
of it through the cache, with no order skipped.

It is accurate rather than approximate, which the screenshot comparison shows.
Against the same screen drawn from bitmap updates, orders differ in 155 pixels,
every one of them in the taskbar clock, which is where two runs of the *same*
path differ too:

| comparison | mean channel difference | pixels differing visibly |
| --- | --- | --- |
| bitmap vs bitmap (the control) | 0.031 of 255 | 169, all in the clock |
| bitmap vs EGFX, which is lossy | 2.886 of 255 | 1438 (0.18%) |
| bitmap vs orders | 0.026 of 255 | 148, all in the clock |

Orders are lossless because the cache holds RLE bitmaps, so they match the bitmap
path to the clock. EGFX is RemoteFX and does not, which is expected and is why
the control is worth running: without it, a mean difference of 2.9 could be read
as a bug.

Four things had to be fixed to get there, and each was found by running against
the server rather than by reading the specification.

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

**An order repeats the previous order's fields, and not only when it says it
does.** The fields an order leaves out are not zero, they are whatever the
previous order of the same type carried, and parsing into a fresh struct loses
them. The symptom is not a parse error: a MEMBLT that omits its cache id reads
cache 0, finds nothing, and draws nothing.

The part that took longest to see is that this has nothing to do with
`TS_DELTA_COORDINATES`. That flag only says the coordinates which are present are
relative to the last ones; which fields are present at all is what the field flags
are for. The carry over was gated on the delta flag, so an order that merely moved
along kept nothing and drew nothing, and a session of 1300 batches produced one
frame. Against a real server the evidence was plain once it was printed: the
order carried `present=0x0006`, no delta flag, and discarded a perfectly good
previous entry of cache 2 at 64x64.

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

The control has to be run at the same time as the comparison, and that is not a
formality. A later attempt compared the two paths again and read a mean of 2.55
with 87% of pixels differing, which looks like a fault. Running the *same* path
twice, immediately, gave 2.551, 87.517% and 688265 pixels: the same numbers to
three digits. The screen had been changing between runs, because Windows fades
between wallpapers on a timer, and a fade is a slow full screen change. The
comparison had measured the fade, not the renderer. When the screen is genuinely
still, the two paths agree to 0.026 of 255 with the differences confined to the
clock.

So: compare against a control taken at the same moment, or the number means
nothing.

Comparing sessions that involved input did not work either, and the reason is
worth recording because it is a trap too: the runs are not independent. Keystrokes land in
whichever window has focus, so the second run inherits the first one's windows and
ends in a different state. The screenshots then differ by a whole window, which
looks like a rendering fault and is not one. A comparison that involves input
needs each run to start from a state it established itself.

## Revision 3 bitmap caches are not used, even when offered

Bitmap cache revision 3 is the one that would ask for an RDP 6.1 compressed
blob, which is where the unimplemented interleaved and banded RLE would come in.
Nothing tested asks for it.

`ORDERFLAGS_EX_CACHE_BITMAP_REV3_SUPPORT` was not being advertised, which is the
same shape as MEMBLT being commented out, so it was worth checking whether
offering it changes anything. It does not: with it set, Windows sends revision 2
anyway, 1373 cache fills of `ORDER_TYPE_BITMAP_COMPRESSED_V2` and not one of
revision 3. xrdp sends no orders at all.

The flag is left unset deliberately, since a revision 3 entry carries either a
bitmap codec or an RDP 6.1 compressed blob, and the second is not decoded here.
Advertising it would claim support that does not exist, for a server behaviour
that does not happen anyway.

Worth noting for anyone who does want it: the premise that revision 3 means
interleaved RLE is only half true. Its entries carry a codec id, so a
`CacheBitmapDataEx` may well hold NSCodec or RemoteFX data, which this library
already decodes. The blob only appears for the banded form.

## An independent review was worth more than another test

The order parser had been tested, fuzzed and watched on a live server, and it
still had three remotely reachable panics and two ways to exhaust memory. They
were found by handing the work to a reviewer with fresh context and the specific
question of what a server could do to it, and it found them in the parsers
nobody had exercised: a colour table order with the colour count it is required
to carry wrote past the end of its own array, a monochrome brush wrote into a
slice that was never allocated, and a compressed brush was decoded from whatever
bytes followed it.

Two things about that are worth keeping. The first is that "the path works" is
not the same as "the parser is safe": every one of these sits in an order no
server tested sends, which is exactly where nobody looks. The second is that the
reviewer also found the one byte shift in the polygon and polyline point lists,
which the tests could not see because they built their orders directly instead of
parsing them. Tests written by the same person who wrote the parser share its
reading of the wire, and this project has now been caught by that three times.

The order fuzzer covers the parse path now. It runs 1.4 million executions in
ninety seconds without stalling; before these fixes it panicked after thirty-five
thousand.

## A fix that looks done is not the same as done

The second review was asked one question, on the grounds that a fix which is wrong
is worse than the bug: did the previous round's fixes actually fix anything. Two
of them had not.

The polygon point list still lacked its one byte cbData field. The fix had been
written once and applied to the polyline and to the colour brush polygon, and
missed the third parser, because the three read similarly and the edit matched two
of them. The glyph cache was never filled: the parser set it, nothing consumed it,
DrawText2 drew from an empty cache, and every TEXT2 order was a silent no-op. That
wiring had been written and then lost when another agent rewrote the file it lived
in.

Neither was visible to the tests, and both had passed a build, a vet, a race run
and 263 tests. The tests build their orders as structs and call the drawing
functions, so a parser that reads the wrong number of bytes never enters the
picture. That is the same shape as the three earlier traps in this document, and
it is worth stating plainly: an order that misreads the stream does not fail, it
makes the next order in the batch nonsense, and nothing that skips the parser can
see it.

There is now a test per order type that puts a MEMBLT with known values after it
and requires the MEMBLT to arrive intact. It found the field flag width of
POLYGON_SC while it was being written, which is the behaviour wanted from it.

The other half of the lesson is about editing. Two of the fixes here were lost by
a later edit to the same file by a different writer, with no conflict and no test
failure to notice it. Where several people or agents touch one file, the result
has to be re-read afterwards rather than assumed.
