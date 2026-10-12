# Frame boundaries, and why the picture tears

A branch for one question: is there a signal that says "this is one frame", and if
there is not, what should a presenter do instead. Everything below was measured
against a real Windows 10 target, and the measurements are in the file rather than
the conclusions alone.

## The tearing, and what it is not

The screen tears between regions of what a viewer would call one frame. That is
not a compositing fault and not an encoding fault: a still screen is perfect, which
is what a still screen would look like if the compositing were wrong. It is a
presentation timing fault, and the numbers below say exactly which.

## Do frame boundaries exist? Yes, twice, and neither arrives

RDP has two ways to say where a frame begins and ends, and this library implements
both:

| Mechanism | Advertised | Parsed | Reaches a caller |
| --- | --- | --- | --- |
| Surface command `FRAME_MARKER` (`CMDTYPE_FRAME_MARKER`) | yes, `SURFCMDS_FRAME_MARKER` | yes | the protocol layer emits `frame-marker`, the `client` package offers nothing |
| AltSec order `FRAME_MARKER` (`ORDER_TYPE_FRAME_MARKER`) | yes, `OrderSupportExFlags` has `ORDERFLAGS_EX_ALTSEC_FRAME_MARKER_SUPPORT` | yes, the frame id is read | **no, it is read and dropped** |

So the mechanism is present, both variants are advertised, and one of them is
thrown away. That is worth fixing on its own: advertising something and then
discarding it is the kind of thing this repository treats as a defect.

**But fixing it would not help this session, because the server sends neither.**
Measured over a twenty second session, counting what the server actually sends:

```
1175  FASTPATH_UPDATETYPE_BITMAP
   7  FASTPATH_UPDATETYPE_CACHED
   4  FASTPATH_UPDATETYPE_POINTER
   2  FASTPATH_UPDATETYPE_PTR
```

Zero orders, zero alternate secondary orders, zero surface commands. Both sides
advertise surface command support with the frame marker flag set (`CmdFlags: 0x52`
on both), and the server never uses it. It draws with bitmap updates.

This is Windows behaving normally for a session carrying bitmap updates, not a
fault to report. A client that waits for a frame marker in this session mode waits
forever.

The slow path was counted too, because frame markers can arrive that way as well
and the first measurement only looked at fast path update codes. Over the same
twenty seconds, the slow path carried **no updates of any kind**. So neither path
carries a frame marker, and the answer to "did the server send us any" is no, on
both paths, measured.

Three gaps matter if a server ever does send one, and two of them are defects here
rather than in the session:

* The **alternate secondary order** frame marker is read and dropped. The frame id
  is parsed and thrown away.
* The **slow path** has no branch for a surface command update at all. It handles
  bitmap and orders update types only, so a frame marker arriving on the slow path
  is parsed and silently discarded. This was found while counting slow path
  updates, and it is a defect whether or not this server ever sends one.
* **Neither reaches a caller**: the fast path emits a `frame-marker` event that the
  `client` package does not expose, and the other two do not emit anything.

And there is no way for the client to ask for them in this session mode. The
capability sets say what a client can process, not what it would prefer; both sides
advertise surface command support with the frame marker flag set and the server
still draws with bitmap updates. The way to change which stream the server uses is
to open the graphics channel, which is EGFX, and that is a different decision.

## How the updates arrive instead

Measured with `-gaps` on this branch, which stamps every bitmap update as it
arrives: 379 updates over 5.6 seconds, 5.1 rectangles each on average.

```
gap ms: min 0.6  p25 1.3  median 1.9  p75 27.6  p90 33.4  max 635.5

how the same updates group at a quiet threshold:
  quiet >= 2 ms:  167 groups, 2.3 updates per group
  quiet >= 5 ms:  147 groups, 2.6 updates per group
  quiet >= 16 ms: 124 groups, 3.1 updates per group
  quiet >= 30 ms:  82 groups, 4.6 updates per group
```

The distribution is bimodal, and that is the whole finding. Half the gaps are
under two milliseconds; a quarter are over twenty seven. The server sends a burst
of two or three updates a millisecond or two apart, then pauses about thirty
milliseconds, which is its update cadence.

**So one frame is a burst.** A presenter that shows each update as it arrives
therefore shows the first update of a burst, then the second a millisecond later,
and so on, which is exactly the tearing that was reported: the parts of one frame
appearing at different times. The span of a burst is a few milliseconds normally
and was around twenty three in the worst case that was reported, which is within
the spread measured here.

## The design

Three parts, in the order they are worth doing.

**1. Stop discarding what is there.** Emit the AltSec frame marker with a frame id
instead of reading and dropping it, and expose both frame marker forms to a
caller through the `client` package, so that a session which does carry them can
use them. This is small, it makes the library honest about a capability it
advertises, and it does not fix this session mode.

**2. Coalesce the updates of one frame.** The presenter accumulates the regions
that have changed and shows them together, rather than showing each update as it
arrives. The boundary is inferred:

* show when nothing has arrived for a **quiet period**, which the measurement says
  is four milliseconds: at two milliseconds 2.3 updates are grouped, at five 2.6,
  so the curve has already flattened and a longer wait only adds latency;
* show anyway once a **maximum wait** has elapsed since the first pending region,
  thirty milliseconds being a reasonable default, because a continuous stream,
  which is what video looks like, never goes quiet and would otherwise never be
  shown.

The second bound is what makes this safe: worst case latency is the maximum wait,
and in the common case it is the quiet period. Both are tunable, and both are
worth exposing as settings rather than hiding.

**3. Present once per frame.** Whatever signal decides the boundary, the swap has
to happen once for the whole frame rather than once per update, and the dirty
region set has to be the frame's rather than the update's. The compositing is
already correct. This is about when it is shown.

## What this cannot do, said plainly

* It **cannot remove the tearing by waiting for a signal that never comes**. The
  server does not say where its frames begin in this session mode, so the boundary
  has to be inferred from timing, and an inferred boundary is a heuristic rather
  than a fact.
* It **buys coherence with latency**, four milliseconds in the common case and up
  to the maximum wait in the worst. That is the trade, and it is a choice worth
  having explicitly rather than by accident.
* **Three structural signals were looked for and none exists**, which is what
  makes this a heuristic rather than a decision: the frame markers are not sent,
  one PDU carries one update so the PDU boundary groups nothing, and the
  fragmentation flags delimit an update rather than a frame. Anyone proposing a
  cleaner solution should be able to say which signal they are using, because
  after this measurement there are three candidates fewer.

* **Coalescing is close to a pure loss when the content is continuous.** With no
  quiet moment to key on, the window degenerates to the maximum wait, and every
  part of a moving picture that arrived inside it is a part that could have been
  shown already. A presenter that detects that pattern, gaps almost all below a
  millisecond and large covered regions, should stop coalescing and show what it
  has. That is a measurable detector and it is not in the design above yet.

* **EGFX is different, and that is the one honest argument for it.** Its stream is
  organised in frames and carries markers, so the boundary is real rather than
  inferred. But it costs cgo and a hardware codec, and it gives up a single static
  binary, and this exploration has already measured that its other attractions are
  not there: a pure Go decoder is not cheaper in CPU, and it is lossy where clarity
  was wanted. Choosing EGFX for a frame boundary means choosing it for that alone.

## What was not done

Nothing here has been implemented. The branch has the measurement tool, `-gaps`,
and this document. The coalescing design in part 2 is where an implementation
would start, and part 1 is independent of it and could land first.
