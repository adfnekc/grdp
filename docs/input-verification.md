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
   `Enter` was delivered before the text it was supposed to submit;
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
