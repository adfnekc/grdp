# Input verification

This records how keyboard and mouse input were verified end-to-end against a
real server, because "the click did nothing" is very hard to distinguish from
"the event never arrived" by looking at screenshots alone.

## Why screenshots are not enough

While testing against xrdp the mouse appeared to be broken: clicks did not move
focus in i3 and typed characters kept landing in the wrong window. The PDUs
looked correct and xrdp reported no errors. The apparent failure had three
unrelated causes, none of which were in the protocol encoding:

1. the CLI under test played all `-key` events *before* `-type` strings, so the
   `Enter` was delivered before the text it was supposed to submit (this has
   since been fixed: the events are replayed in the order they were given, and
   the list below is what the debugging session found at the time);
2. the window layout was being read off the framebuffer, which can be stale;
3. the shell prompt had a coloured background and looked like a window title.

## Deterministic method

`scripts/dev-rdp.sh probe-session` starts a session whose `.xsession` runs

```sh
stdbuf -oL xinput test-xi2 --root >/tmp/xi.log 2>&1 &
exec xterm -class UXTerm -u8
```

so every XI2 event the X server receives is written to a world-readable
`/tmp/xi.log`. Drive input with `cmd/rdpcli`, then read the log from outside the
session. This separates "we encoded the event wrongly" from "the server or X
server dropped it".

A dump of the session's screen is also worth reading rather than skimming: shell
error messages explain far more than a screenshot of a prompt.

An even more direct check that does not depend on rendering at all is to have
the typed command produce a side effect:

```sh
go run ./cmd/rdpcli -host 127.0.0.1:3389 -user rdptest -pass rdptest -proto tls \
  -wait 18s -bitmaps 0 -post-input 3s \
  -key wait:2000 -key click:400,300 -key wait:600 \
  -type 'touch /tmp/grdp-ok.txt' -key press:0x1c -key wait:2000
ls -l /tmp/grdp-ok.txt   # proves the keystrokes executed
```

## Observed result (xrdp 0.9.24 + xorgxrdp 0.9.19, Xorg backend)

`/tmp/xi.log` showed `xrdpMouse` and `xrdpKeyboard` as slave devices and the
following events, confirming every layer works:

| Sent by rdpcli | Received by the X server |
| --- | --- |
| `-key move:300,200` | `Motion` at `root 300.00/200.00` |
| `-key click:500,300` | `ButtonPress`/`ButtonRelease` detail 1 (left) |
| `-type 'z'` | `KeyPress` keycode 52 (`z`) |
| `-key down:0x2a` | `KeyPress` keycode 50 (`Shift_L`) |
| `-type 'a'` while Shift down | `KeyPress` keycode 38 with `effective: 0x1` |
| `-key press:0x1c` | `KeyPress` keycode 36 (`Return`) |

Keyboard, mouse and modifier state all arrive correctly. Fast-path input
(`FASTPATH_INPUT` advertised in the Confirm Active PDU) is what carries them by
default; `-slow-input` forces the slow path for comparison.

## Pointer updates

The server also sends pointer updates back. `-pointer` logs them:

* `FASTPATH_UPDATETYPE_PTR_POSITION` (0x8) — the pointer position.
* `FASTPATH_UPDATETYPE_COLOR`/`CACHED`/`POINTER`/`LARGE_POINTER` and the
  slow-path `PDUTYPE2_POINTER` — pointer shapes.

Note that a freshly connected session always receives two pointer-shape updates
before any input is sent, so those must not be mistaken for a response to mouse
input. Always use a control connection with no input when checking that.

Fast-path updates are parsed inside a reader bounded by the update's declared
`size`. This matters: xrdp's 32x32 24bpp pointer update is five bytes longer
than the documented layout, and without the bound those trailing bytes were
misparsed as a second, bogus update.

## Clipboard

Both directions are verified against xrdp:

* client to server: text published with `SetClipboardText` is read back
  verbatim by `xclip -selection clipboard -o` inside the session;
* server to client: the `CB_FORMAT_DATA_RESPONSE` decodes to exactly the text
  that was placed on the session's clipboard.

Two real xrdp PDUs captured while doing this are kept as regression fixtures in
`plugin/cliprdr/cliprdr_protocol_test.go`. They are worth having because xrdp
under-declares the length of both messages and appends four bytes beyond it.

A note on timing: xrdp can announce a format list slightly before it is able to
serve the data, and a request made in that window is silently dropped. The
client therefore defers and retries its request, and `RequestClipboardText`
exists for callers that want to control the moment themselves.

## Read the terminal, do not guess

An embarrassing amount of time went into a "corrupted clipboard" that turned
out to be a bug in the test client: `cmd/rdpcli`'s ASCII to scancode map only
listed unshifted characters, so typing an upper case letter hit a missing entry
and was silently skipped. Shifted punctuation was in the map, so shifted
*digits and symbols* worked while shifted *letters* vanished, which made it look
like the library's shift handling was at fault. It was not, and the X server
event log proved it by showing the letters had never been sent at all.

The lesson generalises: when an interaction seems broken, dump what the other
side actually received, and read the terminal, before theorising about the
protocol. `scripts/dev-rdp.sh probe-session` exists for exactly that.

## Mouse buttons against a Windows 10 target

An issue reported that pointer button events reach a Windows 10 build 19041 target
but have no effect there, while pointer moves and keyboard input work. It was
reproduced on the target this repository tests against, which turns out to carry
the same build string: Windows 10 Enterprise LTSC Evaluation, Build
19041.vb_release.191206-1406.

What holds, measured rather than argued:

| | Result |
|---|---|
| Pointer moves | work, and the server draws a fresh tooltip on hover, which is what says the position arrived |
| Keyboard, including shell hotkeys | work; `Ctrl+Esc` opened the Start menu |
| Left button, fast path | no effect |
| Left button, slow path | no effect |
| Right button | no effect |
| The input preamble (tab up, synchronise, tab up) | sends, and changes nothing |

The issue's claim that `Ctrl+Esc` fails on this target does not reproduce. The
Start menu opens, so the shell does process keyboard input, and the session is not
in a state where input is being dropped wholesale.

The bytes are not the problem. A click at (300, 400) sends

```
move: 04 80 0a 20 | 0008 2c01 9001   MOVE
down: 04 80 0a 20 | 0098 2c01 9001   DOWN|BUTTON1|MOVE
up:   04 80 0a 20 | 0018 2c01 9001   BUTTON1|MOVE
```

which is what FreeRDP sends, field for field, including the header byte, the
big-endian length, the event code shift and the little-endian payload. The
capability that gates fast path input, `INPUT_FLAG_FASTPATH_INPUT`, is advertised.
The slow path's event layout has no length field, which is correct, and it fails
the same way.

None of that is where the fault was. What follows is what was checked before it was
found, because the checks are the reason it took as long as it did, and because two
of them produced changes that had to be reverted.

## What was ruled out first

The pointer path was checked against outside references rather than reasoning,
because "the bytes look right" had already been said once too often.

| Checked | Against | Result |
|---|---|---|
| Fast path pointer PDU | FreeRDP `fastpath_send_multiple_input_pdu` and `input_send_fastpath_mouse_event` | identical: header byte `action \| numEvents << 2`, length `0x8000 \| total`, event byte `eventFlags \| eventCode << 5`, then flags, x, y little endian |
| Same PDU | Microsoft's own annotated capture, MS-RDPBCGR "Annotated Fast-Path Input Event PDU" | identical, including the one and two byte forms of the length field |
| Slow path input PDU | FreeRDP `rdp_client_input_pdu_init` (which is the shape master builds by default) | identical: share control and share data headers, `numEvents`, `pad2Octets`, then `eventTime`, `messageType`, flags, x, y |
| Fast path input capability | MS-RDPBCGR `TS_INPUT_CAPABILITYSET` | `INPUT_FLAG_FASTPATH_INPUT` and `INPUT_FLAG_FASTPATH_INPUT2` are both advertised |
| Session control | the `CTRLACTION_GRANTED_CONTROL` reply | `grantedControlId` is this client's own channel, so no other controller holds the session |

Two things nearly became "fixes" and were not, which is worth writing down because
this project has done that before. The slow path PDU appeared to carry
`numEvents = 0`, which would explain everything; it does not, because the share
data header is twelve bytes and not ten, and the count is `01 00` exactly where it
should be. And a drag appeared to need the held button repeated on every move;
it does not, because a button flag without `PTRFLAGS_DOWN` means the button was
released, as the spec says and as the `0x1800` of an ordinary release shows. A
movement event carries `PTRFLAGS_MOVE` alone and the server keeps the button state
between events. Both of those changes were reverted.

The encoding was right on both paths, and that was the point: the fault was not in
how an event is laid out but in **which flags were put in it**.

## What it was: a movement flag on the button events

`MouseDown` and `MouseUp` both added `PTRFLAGS_MOVE` to their pointer event, so a
click went out as

```
down: 04 80 0a 20 | 0098 ...   PTRFLAGS_DOWN | PTRFLAGS_BUTTON1 | PTRFLAGS_MOVE
up:   04 80 0a 20 | 0018 ...   PTRFLAGS_BUTTON1 | PTRFLAGS_MOVE
```

`PTRFLAGS_MOVE` does not belong on a button event. MS-RDPBCGR 2.2.8.1.1.3.1.1.3
lists the pointer flags in separate groups: a movement event carries
`PTRFLAGS_MOVE`, and a button event carries `PTRFLAGS_DOWN` together with the button
that was pressed or released. FreeRDP's own client sends exactly that, which
`client/X11/xf_event.c` shows plainly:

```c
	else if (flags & (PTR_FLAGS_BUTTON1 | PTR_FLAGS_BUTTON2 | PTR_FLAGS_BUTTON3))
	{
		if (down)
			flags |= PTR_FLAGS_DOWN;
	}
```

There is no `PTR_FLAGS_MOVE` there, and the motion path sends `PTR_FLAGS_MOVE`
alone.

This is why it survived every byte level comparison. The comparison the issue made,
and the one made here, was of the **encoder**: how a given set of flags is packed.
The encoder is shared and was always correct. The flags themselves are chosen by the
client's input layer, and that is where the two bits had been added. Nothing that
looks at encoding could ever have found it.

It is also why xrdp accepted clicks and Windows did not. xrdp only reads the button
bits and ignores a stray movement bit; Windows validates the event and drops it.

The fix removes `PTRFLAGS_MOVE` from `MouseDown` and `MouseUp` and leaves it on
`MouseMove`, which is what every other client does.

## Verification

Confirmed on the target, with the fix in place, using tests that cannot be produced
by hovering. Each is a screenshot cropped to the region that matters and looked at
on its own, rather than an impression of a whole screen:

| Test | Result |
|---|---|
| Left click a desktop icon, then move the pointer away | the icon stays selected, so it was a click and not the hover highlight |
| Left click and drag across the wallpaper | a selection rectangle whose corners are the coordinates that were sent |
| Right click the desktop | the desktop context menu |
| Left click the taskbar clock | the calendar flyout |

And the same tests against the code as it was, with `PTRFLAGS_MOVE` on the button
events:

| Test | Result |
|---|---|
| Right click the desktop | nothing |
| Drag across the wallpaper | nothing |

That A/B is what settles it. `PTRFLAGS_MOVE` on a button event is what this target
rejects, and removing it is the fix. The issue's note that removing it made no
difference does not reproduce.

## What made this hard to see, and one test that lies

The Start button does not open the Start menu on this target, with or without the
fix, and it is worth writing down because it misled the issue report and this file
for several rounds. Its Start menu is broken: on the one occasion it did open, the
shell stopped accepting input entirely, including from the keyboard, and stayed that
way until the machine was rebooted.

So "click the Start button and see whether the menu opens" is not a usable test on
this machine. It fails for reasons that have nothing to do with the client, and it
fails in a way that looks exactly like the mouse being ignored. The tests above were
chosen because they do not touch the Start menu, and because a hover cannot fake
them: a selection that survives the pointer moving away, a context menu, a flyout, a
selection rectangle.

Two mistakes were made here and are recorded rather than quietly dropped. The first
was claiming the buttons shipped in an earlier version still failed, and then the
opposite, on the strength of a hung shell. The second was reading a selection
rectangle into a screenshot that did not have one. Both came from looking rather than
measuring, which this file already warned about.

## The wheel's rotation is signed, and a notch is 120 units

The wheel was reported as scrolling the wrong distance: counting notches on the
remote, one direction moved about eight notches over fifty one wheel events and the
other about four hundred and eighty over three hundred and eighty one, with the
same distance travelled each way. Both numbers have the same cause, and there were
three faults behind it.

**The rotation is a signed value in a nine bit field.** MS-RDPBCGR defines
`WheelRotationMask` as 0x01FF and `PTRFLAGS_WHEEL_NEGATIVE` as 0x0100, which are
the same bit, and says of the flag that "the wheel rotation value (contained in the
WheelRotationMask bit field) is negative and MUST be sign-extended before injection
at the server". The value travels in the field; the flag is its sign bit. The code
here set the sign bit and then put a positive magnitude beside it, so one notch down
went out as `0x0100 | 0x78` where it should be `0x0188`: a server sign extends the
first to -136 and the second to -120. FreeRDP shipped the same mistake in its X11
client and fixed it in 2023, in
[FreeRDP#6805](https://github.com/FreeRDP/FreeRDP/pull/6805), with exactly that
substitution in `client/X11/xf_client.c`:

```c
-	{ Button5, PTR_FLAGS_WHEEL | PTR_FLAGS_WHEEL_NEGATIVE | 0x78 },
+	{ Button5, PTR_FLAGS_WHEEL | 0x188 },
```

Masking the signed value into the field is what produces the two's complement, so
the fix is smaller than the fault was, and the value is now clamped to the field's
range instead of wrapping: scaling a scroll up into a scroll down is a worse
outcome than a shorter scroll.

**The event carried `PTRFLAGS_MOVE`, which a wheel event does not have.** The only
valid flags in a wheel rotation event are the sign bit and the rotation mask, "all
other pointer flags are ignored", and FreeRDP's wheel entries above carry the
rotation and nothing else. This is the same fault that made Windows drop button
events, in the same place, and it is the reason both were looked for together.

**A caller passing notches was sending units.** The field counts rotation units,
not notches, and one notch is 120 of them, which Windows calls `WHEEL_DELTA`.
`WheelDelta` is now exported and documented, and the two callers in this repository
that passed 1 meaning "one notch" multiply by it. That is what explains the reported
asymmetry: one direction was sent as a single unit, a hundred and twentieth of a
notch, which scrolls nothing at all, and the other as `0x0100 | 1`, which a server
sign extends to -255 units, about two notches. Eight notches over fifty events and
four hundred and eighty over three hundred and eighty are those two errors, with
the counts this client's own `MouseWheel` produced at the time.

The tests pin the bytes for a notch each way and for the ends of the field, and
decode the field back the way a server does, sign extending it, so a value that does
not survive the round trip fails. What they cannot show is the remote scrolling the
distance it should, which needs a scrollable window on a live target.
