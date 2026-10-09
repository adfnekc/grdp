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
    c.MouseDown(0, x, y)          // 0 left, 1 middle, 2 right
    c.MouseUp(0, x, y)
    c.MouseWheel(notches, x, y)   // positive is away from the user

    c.KeyDown(0x1c, "")           // PC set 1 scancodes
    c.KeyUp(0x1c, "")
    c.TypeText("你好", 8*time.Millisecond)

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
  prose was ambiguous or silently incomplete about all four.

* **The input preamble is available and is not held to fix anything.**
  `SendInputPreamble` sends what mstsc and FreeRDP send when the session becomes
  ready: a Tab release, the toggle key state, and another Tab release, all three in
  one PDU. `SendSynchronize` sends only the toggle state. The preamble was worth
  having because its three events have three different event codes and the fast
  path used to work the code out once per PDU, so the PDU could not be expressed
  at all; `SendInputEvents` now takes the code from each event and a PDU may mix
  types freely. What it is not is a fix for a target that ignores mouse buttons:
  it was measured against such a target and changed nothing, and the bytes of the
  button events were already correct. Do not adopt it hoping for that.

* **A listener can run twice at once, and that is fatal rather than awkward.**
  The emitter calls every listener in its own goroutine, so a handler registered
  twice for one event runs twice concurrently. If both copies write the same Go
  map, the runtime raises a `fatal error: concurrent map writes`, which is not a
  panic: `recover` does not catch it, and it takes the process down with every
  session in it. It happened to a gateway. `Once` is what stops the connection
  sequence from doing it, by taking the listener off the list before calling it,
  but a handler registered with `On` twice is still two goroutines. Anything a
  handler touches that another copy could touch needs a lock, and anything the
  read loop and another goroutine share needs one too.

* **A comment that describes a mistake is worth keeping.** Several times the fix
  left the old wrong assumption in place as a note, and twice a later reader
  nearly "fixed" working code back to the wrong version because a test asserted
  the old behaviour. When you correct a belief, correct the test with it.
