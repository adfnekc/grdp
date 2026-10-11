# Writing an RDP client on top of this library

This is for someone building something with grdp: a desktop client, a gateway, a
test harness or a tool. It walks the session from connect to disconnect and says
what each part expects of you, with the traps that have already been paid for
once. For the layers underneath, see protocol-layers.md; for how each claim in
here was checked, see windows-verification.md.

## The shape of a session

    s := client.NewSetting()
    s.Width, s.Height = 1024, 768
    s.Protocol = "nla"

    c := client.NewClient(host, user, password, client.TC_RDP, s)
    defer c.Close()

    c.OnError(func(err error) { ... })
    c.OnClose(func() { ... })
    c.OnFrame(func(dirty []image.Rectangle) { ... })
    c.OnCursor(func(cur *client.Cursor) { ... })

    if err := c.Login(); err != nil {
        return err
    }

Handlers are registered before Login. They are buffered and replayed once the
layers exist, so nothing is lost by registering early, and Login does not need a
handler to be registered after it returns.

Login returns when the session is usable. `LoginContext` takes a context and is
what you want if the user can cancel: a server that accepts the connection and
then says nothing will otherwise hold you until the timeout.

Everything after that is driven by callbacks on the session's read goroutine.
That goroutine is the one place a session makes progress, so a callback that
blocks stops the session: anything slow belongs on another goroutine, with a
queue between.

## Reading the screen

`Framebuffer` is the desktop, whatever path the server chose to draw it:

    fb := c.Framebuffer()          // BGRA, top down, stride 4*Width
    pix := fb.Pix()                // the buffer itself, not a copy
    w, h := fb.Size()

`OnFrame` says what changed since the last call, already merged and clipped:

    c.OnFrame(func(dirty []image.Rectangle) {
        for _, r := range dirty {
            encode(pix, fb.Stride(), r)
        }
    })

Two things about the lifetime. `Pix` is the live buffer, not a copy, and it stays
as it is until the next `OnFrame`: read it inside the callback or copy it out.
And `OnFrame` fires per batch, not per change, so one call can carry several
rectangles and one rectangle can cover everything; the merge rules are in the
`addDirty` documentation, and the short version is that neighbouring regions come
as one and a region covering most of the screen comes as the whole screen.

That is the intended way to read a screen. `OnBitmap`, `Screen` and
`OnOrdersFrame` still work and are deprecated where the framebuffer supersedes
them.

The pointer is not part of the desktop and does not arrive in frames. `OnCursor`
gives it as an image with alpha and a hotspot:

    c.OnCursor(func(cur *client.Cursor) {
        if cur.System {
            drawSystemCursor(cur.SystemType)   // SYSPTR_NULL means hide it
            return
        }
        drawImage(cur.Image, at(lastPos.Sub(cur.Hotspot)))
    })

It fires when the shape changes, not when it moves, so pair it with `OnCursorPos`
and keep the last position yourself. A component that tracks the mouse already
knows where the pointer is and only needs the shape.

## Sending input

    c.MouseMove(x, y)
    c.MouseDown(0, x, y)              // 0 left, 1 middle, 2 right
    c.MouseUp(0, x, y)
    c.MouseWheel(client.WheelDelta, x, y)  // 120 units is one notch, up

    c.KeyDown(0x1c, "")           // PC set 1 scancodes
    c.KeyUp(0x1c, "")
    c.TypeText("你好", 8*time.Millisecond)

The wheel counts rotation units, not notches: one notch is `client.WheelDelta`,
which is Windows' `WHEEL_DELTA` of 120. A value is sent as a signed nine bit
field, so the sign of the argument is what decides the direction; a caller that
passes 1 for a notch sends a hundred and twentieth of one and scrolls nothing.

Keys go as scancodes, which is what makes shortcuts work: control-c is a scancode
for a key with a modifier, and no character can express it. Text goes as
characters, which is what makes an input method work: an IME composes several
keystrokes into a character that has no scancode, and Unicode key events are the
only way to send it. A client needs both, and which one a keystroke becomes is
the caller's decision.

An extended key is a scancode in `0xE000..0xE0FF`: the low byte is the scancode
and the prefix becomes the extended flag. `TypeText` rejects text that is not
valid UTF-8 rather than typing replacement characters, and it cannot work on a
VNC session, where it returns `ErrNoUnicodeInput` instead of doing nothing.

## Clipboard and files

`Setting.EnableClipboard` opens the channel, and without it the clipboard methods
return an error rather than silently doing nothing.

    c.OnClipboardText(func(text string) { ... })
    c.SetClipboardText("...")

Files are not text and do not go through those methods. `Client.Files` is the
surface, and it exists because the application has to decide where bytes live:

    c.Files().SetFileProvider(myProvider)          // what we offer
    c.Files().OnRemoteFiles(func(files []cliprdr.RemoteFile) { ... })
    data, err := c.Files().ReadRemoteFile(index, offset, length)

Nothing file shaped is advertised until a provider is set. A format that is
advertised and cannot be produced hangs the paste rather than failing it, so the
order matters: set the provider first.

Reading is by range. A multi-gigabyte file costs no more memory than the range
asked for, and a range past the end is an error rather than a short read
presented as success.

## Changing the size

`Setting.EnableDisplayControl` opens the channel, and `RequestResize` asks the
server to change the desktop:

    c.RequestResize(1280, 720)
    c.OnResize(func(w, h int) { /* the size that actually took effect */ })

The answer is what counts, not the request: the server rounds to a display mode
it has, so asking for 720 can give 768. `Framebuffer` follows the size the server
starts drawing at, and a `OnResize` is followed by a whole-frame `OnFrame`.

## Drawing paths, and why two of them are opt in

A session uses one path at a time:

| Path | Setting | What happens |
| --- | --- | --- |
| Bitmap updates | the default | Rectangles of RLE or codec data. Every server supports it. |
| Drawing orders | `EnableOrders` | The client advertises MEMBLT and the server stops sending bitmap updates entirely, drawing through its bitmap cache. Much smaller on the wire, much more work to render. |
| EGFX | `EnableEGFX` | A dynamic channel carries surfaces. Again the server stops sending bitmap updates. |

The last two cannot be mixed with the first within a session, which is why both
are off by default: turn one on and meet a codec that is not implemented and you
get a blank screen rather than a degraded one. Advertising a capability changes
what the server sends, and that is the general rule here rather than a property
of these two.

Neither path changes how you read the screen: the framebuffer is the framebuffer.

## Failures worth telling apart

The error kinds are separated by the stage the failure happened in, because that
is what the transport can know. Before the credentials are sent, it is
`ErrTLSFailure` or `ErrCredSSP` and the account has not been proven wrong; after
they are sent and the server goes quiet, it is `ErrAuthenticationFailed`, which is
what a refused logon looks like from here. `ErrUnreachable` means the connection
was never established, and the `*net.OpError` underneath still says whether
nothing was listening or the network was unreachable.

`ErrSessionEndedByServer` is the one that looks most like a fault and is not: a
session taken over by another connection is still on the server, detached, and
reconnecting gets it back. A dropped network sends no disconnect ultimatum at
all, so the presence of one is what tells the two apart.

The README has the table with the test for each.

One case has no signal at all. A server that accepts the connection and then
reports the failure inside the session, which is what xrdp does with a
"login failed for user" dialog, sends nothing on the wire. No client can detect
that, and reading the screen is not a detection strategy.

## Traps

These are the ones that have cost time here. Most were found by pointing the code
at a server that disagreed, after unit tests had passed.

* **Advertise only what you implement.** The pointer cache size is a contract:
  advertise twenty slots and the server stops resending shapes, so a client with
  nowhere to put them loses the cursor rather than drawing it wrongly. The same
  reasoning is behind every capability in this library being conservative.

* **A test that constructs a structure instead of parsing bytes cannot see a
  misalignment.** Several faults here were invisible to unit tests that built
  their input in Go and were found the moment real bytes arrived.

* **A parser that cannot read the fields it needs must return an error.** Reading
  nothing and carrying on leaves the stream misaligned, and the symptom appears
  in whatever is parsed next.

* **Bound anything from the wire before it feeds an allocation or a loop.** A
  thirty byte bitmap update once asked for seventeen gigabytes.

* **Settle protocol questions against FreeRDP or a capture, never against the
  specification's prose.** Every time that rule was broken here it cost a bug:
  the pointer mask padding, the fast path's release flag, the width of
  systemPointerType, and the ordering of the Display Control capabilities. The
  prose was ambiguous or silently incomplete about all four. The rule has a second
  half, which is to settle the question rather than to note it: the wheel's
  rotation was queried as ambiguous here and turned out to be defined, with a
  `MUST` and a FreeRDP commit behind it, and the code was wrong in two ways.

* **A log line that is not written must not be paid for.** `glog.Hex` exists for
  this. Putting `hex.EncodeToString(payload)` in a `glog.Trace` call encodes the
  payload and then discovers that tracing is off, on every packet, at five layers
  of the stack: measured at 4,194,382 bytes of garbage per 1 MiB received PDU, and
  16% of a file transfer's CPU profile. The message must not be built before the
  level is read, which is why the level check is inside `glog` and not at the call
  site, where a later caller can get it wrong.

* **The wheel's rotation is a signed nine bit value, and a notch is 120 units.**
  `PTRFLAGS_WHEEL_NEGATIVE` is the sign bit of the rotation field, not a flag to
  set beside a magnitude, so one notch down is `0x0188` and not `0x0100|0x78`.
  `WheelDelta` is the 120. Passing a notch count as the argument sends one unit
  instead of one notch, which scrolls nothing. See docs/input-verification.md for
  the spec text and the FreeRDP commit that shows both forms side by side.

* **A tag means a fix the maintainer has confirmed.** Do not tag a change on the
  strength of a test that could have passed for another reason, and do not tag
  documentation or speculative work at all. Several versions were tagged here for
  changes that were later shown to fix nothing, and the tags had to be deleted.

* **The input preamble is available and is not held to fix anything.**
  `SendInputPreamble` sends what mstsc and FreeRDP send when the session becomes
  ready: a Tab release, the toggle key state, and another Tab release, all three in
  one PDU. `SendSynchronize` sends only the toggle state. The preamble was worth
  having because its three events have three different event codes and the fast
  path used to work the code out once per PDU, so the PDU could not be expressed
  at all; `SendInputEvents` now takes the code from each event and a PDU may mix
  types freely. It is not a fix for anything, and in particular not for the mouse
  button faults that turned out to be a movement flag on the button events: the
  preamble does not touch those, and sending it changed nothing. Do not adopt it
  hoping for that.

* **A listener that writes a map the read loop also writes needs a lock, and
  `Once` is what makes the connection sequence safe.** Two concurrent `Emit` calls
  still run concurrently with each other, so a handler can still be entered twice
  at once by two deliveries, and two concurrent writers to one Go map are a
  `fatal error: concurrent map writes` rather than a panic: `recover` does not
  catch it and it takes the process down with every session in it. It happened to
  a gateway. `Once` is what stops the connection sequence from doing it, by taking
  the listener off the list before calling it.

  This entry used to say more than that, and the rest is no longer true. The
  emitter used to call every listener in its own goroutine, so two listeners for
  one event ran concurrently too, and the reflection and the goroutines together
  cost about 1.5 microseconds per delivery on a path that delivers once per PDU at
  every layer. Listeners are now called in order on the emitting goroutine, which
  is five to fifty times faster and removes that class of bug: two listeners of
  one event cannot overlap, and the test that pins it fails on the old code.

* **A per-delivery cost is a per-packet cost, and the packet rate is what you
  notice.** The emitter was 1.5 microseconds and six allocations per delivery,
  which does not sound like much until it is multiplied by five layers and every
  PDU of a file transfer. Delivering a 1460 byte channel message went from 2486
  nanoseconds to 469, and the smaller the message the larger the gain, because the
  cost was fixed rather than proportional. When something looks expensive, check
  whether it is on a per-packet path before deciding it is too small to matter.

* **Advertising a capability version is a promise to process what it implies.**
  A server is entitled to refuse a client that claims a version it does not
  implement, and Windows 10 does more than refuse: adding the version 10.0
  graphics capability set, which is what asking for AVC444 requires, makes it reset
  the connection as soon as the advertisement goes out. Offering 8.0 and 8.1 with
  the AVC420 flag keeps the session. The rule that follows is the one this
  repository already applies to capabilities elsewhere: advertise what you
  implement, and nothing else.

* **Measure the path before changing the client.** A file transfer here ran at
  about a tenth of what the same network path does raw, and every client-side
  change that was tried against it left throughput where it was: removing the
  reflection, buffering the reads, pipelining the range requests, and declaring a
  different connection type. What settled it was measuring the path on its own
  first, sweeping the range size, and finding the shape of that curve. See
  docs/transfer-performance.md for the numbers and for the four wrong theories
  that cost more than the experiment did.

* **A comment that describes a mistake is worth keeping.** Several times the fix
  left the old wrong assumption in place as a note, and twice a later reader
  nearly "fixed" working code back to the wrong version because a test asserted
  the old behaviour. When you correct a belief, correct the test with it.

## Upgrading from 0.8 to 0.9

### Since the v0.9.0 tag: listeners are called in order, on the calling goroutine

One change landed after v0.9.0 and is not in that tag. `emission.Emitter` used to
call every listener in its own goroutine, through `reflect.Value.Call`, with the
emit waiting for all of them. It now calls them in registration order, directly,
on the goroutine that emitted.

That is a semantic change for a caller that relied on two listeners of one event
running concurrently, and it removes a class of bug rather than adding one: the
`fatal error: concurrent map writes` in the traps below came from exactly that
concurrency, and it is now impossible for one emit. Two concurrent emits still run
concurrently with each other, so a handler that another delivery can enter still
needs its own lock, and `Once` is still what protects the connection sequence.

It is also the fix for the per-PDU delivery cost: 1.5 microseconds and six
allocations per delivery became 67 nanoseconds and two, and delivering a 1460 byte
channel message went from 2486 to 469 nanoseconds. The smaller the message, the
larger the gain, because the cost was fixed.

## Upgrading from 0.8 to 0.9

0.9.0 was already tagged when the reflection and the goroutines were removed, so a
caller moving from 0.8 to a later tag gets both this and the list below. 0.9.0 is a
breaking release. The list below was not written from memory: the
exported API of v0.8.6 and of v0.9.0 was extracted from both trees by parsing
them and the two lists were diffed, so it is the whole of what changed.

If you import `client` and nothing below it, **your build does not break**. That
package's exported API is purely additive in this release. What you have to check
is behaviour, and there is one change that matters.

| Package imported | Breaks the build | Changes behaviour |
| --- | --- | --- |
| `client` | no, additive only | yes: `MouseWheel` counts units, not notches |
| `glog` | no, additive only | no, but see the note on `glog.Hex` |
| `plugin` | no | yes: `Channels.SendToChannel` |
| `protocol/t125/per` | yes | yes |
| `protocol/t125/gcc` | yes | yes |
| `protocol/t125`, `protocol/nla`, `protocol/lic` | yes, removals | yes |
| everything else | no | no |

The full diff, for completeness:

    removed
    lic.NewLicenseBinaryBlob
    nla.NewChallengeMessage
    t125.NewConnectResponse
    gcc.MakeConferenceCreateResponse
    gcc.NewServerCoreData
    gcc.NewServerNetworkData
    gcc.ServerCoreData.Serialize
    gcc.ProprietaryServerCertificate.Encrypt
    gcc.X509CertificateChain.Encrypt

    signature changed
    gcc.CertData.Verify            bool -> error
    gcc.ProprietaryServerCertificate.Verify   bool -> error
    gcc.X509CertificateChain.Verify           bool -> error
    per.ReadExcept                 no result -> error
    per.MacthOctetStream           renamed to MatchOctetStream

    added
    client.WheelDelta
    client.Setting.RequireVerifiedCertificate
    sec.Client.SetRequireVerifiedCertificate
    glog.Hex

Two other things changed in this release that were pushed after v0.8.6 and are
therefore part of v0.9.0 rather than of v0.8.6: the wheel encoding and the
logging cost. A caller that jumped from v0.8.6 to v0.9.0 gets both, and the wheel
one needs a code change.

### The one code change a gateway needs: MouseWheel

The rotation field counts rotation units, not notches, and one notch is 120 of
them. 0.8.6 sent a caller's argument through as units, and sent a negative one
wrongly, so a gateway that passed `1` for a notch was sending a hundred and
twentieth of one in one direction and about two notches in the other. Both are
fixed, which means a caller that passed notches must now say so:

    // before, which scrolled nothing up and about two notches down
    c.MouseWheel(1, x, y)

    // after
    c.MouseWheel(client.WheelDelta, x, y)     // one notch up
    c.MouseWheel(-client.WheelDelta, x, y)    // one notch down

A browser wheel event carries a direction rather than a distance, so the usual
shape is:

    scroll := 1
    if ev.DeltaY > 0 {
        scroll = -1
    }
    c.MouseWheel(scroll*client.WheelDelta, x, y)

Anything larger than 255 or smaller than -256 is clamped rather than wrapped,
because the field is nine bits and signed and a scroll up that becomes a scroll
down is worse than a shorter scroll.

### glog: nothing breaks, but stop encoding payloads at the call site

`glog` is compatible: the level functions and the level constants are unchanged.
What changed is when work happens. The level is now read before a message is
built, so a discarded line costs almost nothing, and `glog.Hex` defers the
encoding of a byte dump to the same moment.

A caller that adds its own per-packet TRACE line should use it. The pattern it
replaces encoded every received PDU into a string whether tracing was on or not,
which measured at four megabytes of garbage for each megabyte transferred:

    glog.Trace("recvData", hex.EncodeToString(payload))   // encodes always
    glog.Trace("recvData", glog.Hex(payload))             // encodes if written

Nothing breaks if this is left alone. It just stays slow.

### The server certificate is now actually checked

`gcc.CertData.Verify` returned `true` for every certificate. It was called on the
Standard RDP Security path, before the client random was encrypted to the
certificate's key, so a certificate that nobody trusts was reported as trusted and
the session keys were agreed with whoever presented it.

It now returns an `error` and does the work:

    Verify() error        // was: Verify() bool

* `gcc.X509CertificateChain.Verify` verifies the leaf against the system roots,
  using the other blobs as intermediates. A chain issued by a public certificate
  authority passes; a self-signed one does not, because there is nothing in the
  roots to anchor it. The server's name is not checked, because this structure
  carries no name; TLS checks names, this cannot.
* `gcc.ProprietaryServerCertificate.Verify` always returns an error, and says why:
  a version 1 certificate is a bare public key and a signature blob with no
  issuer, no validity period and no trust anchor, so there is nothing to verify it
  against. It is not a check that failed, it is a check that cannot exist.

The default is to report the result and continue, which is the same default the
TLS layer already used (`InsecureSkipVerify`), and it is why most sessions do not
change: an RDP server normally presents a self-signed certificate, so requiring
verification by default would refuse most servers, including a local xrdp.

To require it:

    s := client.NewSetting()
    s.RequireVerifiedCertificate = true    // Standard RDP Security only

or on the layers below, `sec.Client.SetRequireVerifiedCertificate(true)`. A
version 1 certificate then refuses the login, which is what that setting means.

Over TLS or CredSSP this has no effect: there the certificate belongs to the TLS
layer.

### A send that failed no longer looks like one that worked

`plugin.Channels.SendToChannel` returned a count that was always zero, because it
returned the variable its loop counted down, and it discarded the error from the
channel sender. It now returns the number of payload bytes sent and propagates the
error, so a caller that checked `n == 0` to mean "nothing was sent" must be
checked: zero now means zero, and a partial chunked send returns the part that
went.

### Names

    per.MacthOctetStream        ->  per.MatchOctetStream

It was a misspelling of the same function, exported, so the rename is breaking.

### Functions that reported rather than did

    per.ReadExcept(b byte, r io.Reader)   // panicked on a mismatch
    per.ReadExcept(b byte, r io.Reader)   // returns an error

It parses bytes that arrived over a network. A parser that can end the process on
hostile input is not one this library should have. The byte is still consumed
either way, so a caller must treat the error as fatal to the message rather than
skipping past it.

### Deleted

These were exported, unimplemented and called from nowhere. An exported function
that returns `nil` or an empty slice says it does something, which is worse than
not being there:

    lic.NewLicenseBinaryBlob                 // ignored its only argument
    gcc.MakeConferenceCreateResponse         // returned nil
    gcc.ServerCoreData.Serialize             // returned an empty slice
    gcc.NewServerCoreData                    // for a server side that does not exist
    gcc.NewServerNetworkData                 // as above
    t125.NewConnectResponse                  // as above
    nla.NewChallengeMessage                  // as above
    gcc.ProprietaryServerCertificate.Encrypt // returned nil
    gcc.X509CertificateChain.Encrypt         // returned nil

`gcc.NewServerSecurityData` is kept, because a test uses it.

### One encoding corrected

`per.WriteObjectIdentifier` combined the first two OID components with a bitwise
AND where the encoding calls for `oid[0]*40+oid[1]`. The two agree only when both
are zero, which is true of 0.0.20.127.0.0.1, the identifier MCS actually sends, so
the bytes on the wire do not change for it. For any other identifier they do, and
now they are right.

`ber.WriteInteger` wrote a negative value's low byte as though it were positive,
because the comparison that chose the width was a comparison on an `int`. Negative
values are now written as four bytes of two's complement, which is what a BER
reader sign extends. Positive values are unchanged, byte for byte.
