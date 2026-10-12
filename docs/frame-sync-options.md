# Frame synchronisation: the options

For deciding together, before anything is implemented. This follows
docs/frame-boundaries.md, which established the measurements; this file is about
what to do with them.

## The finding that reframes the problem

**mstsc does not tear, and it is not because it has a better signal.** The server
sends it the same bitmap updates, which was measured: no frame markers on either
path, one PDU carrying one update. So the difference has to be in what the client
does with them, and FreeRDP's X11 client shows what that is.

`xf_end_paint` in `client/X11/xf_client.c` does not paint as updates arrive. It
accumulates the invalid regions into a list, and when the frame is done it takes
the X11 lock, paints every region in one pass, flushes, and unlocks:

```c
	xf_lock_x11(xfc);
	for (INT32 i = 0; i < ninvalid; i++)
		xf_paint(xfc, &cinvalid[i]);
	LogDynAndXFlush(xfc->log, xfc->display);
	xf_unlock_x11(xfc);
```

The regions are applied to a persistent surface and shown together. **The tearing
being fought here is therefore a presentation model problem rather than a missing
signal problem**: parts of one frame appear at different times because the sink
shows them as they arrive, not because the boundary is unknown.

That also explains the measurement: 89 per cent of updates arrive within a
millisecond of the previous one, so a sink that shows its accumulated state once
per display interval shows whole frames almost all of the time, and the eleven per
cent that are genuinely split are the residual, not the rule.

## The options

**A. Persistent surface, presented on the sink's cadence.** Keep one surface.
Each update applies its rectangles to it. Present, meaning encode, or push, or
swap buffers, only when the sink is ready to show something, roughly once per
display interval. This is what mstsc and FreeRDP do, and it needs no boundary at
all. Cost: rendering and presenting stop being the same act. Residual tearing:
frames the server itself splits, plus frames that straddle a present boundary.

**B. Coalesce on a quiet gap.** Wait for about two milliseconds of silence, with a
maximum wait of about thirty as a bound, then present. Better than A for a frame
the server split, because it waits for the rest of it. Worse than A in the common
case, because those updates have already arrived and the wait is pure latency. For
continuous content there is no silence, so it degenerates to the maximum wait.
**Useful only as a refinement inside A's window, not on its own.**

**C. Push region deltas instead of whole frames.** The gateway appears to encode a
full screen JPEG per update, which is what makes a partially updated frame visible
as a discrete wrong image rather than as a partial improvement to a right one. If
the sink keeps a persistent surface and receives only the changed rectangles,
unchanged areas keep their previous content and a partial frame is visually
consistent. It also removes a full frame encode per update, which is most of the
gateway's cost per update. This is the same persistent surface A needs, so A and C
are one change.

**D. Use the frame markers when the server sends them.** The pipeline for these now
works on master: both forms arrive as one type and reach a caller through
`OnFrameMarker`. A bitmap session sends none, so this cannot be the only mechanism.
**Correct role: an accelerator on top of A.** When markers exist they are exact,
and when they do not, the cadence is the fallback.

**E. EGFX or AVC.** The only way to get a real frame boundary, since that stream is
organised in frames. It costs cgo, a hardware codec, and the single static binary,
and the target measured here has no H.264 encoder at all. **Correct role: a reason
to choose it if the goal is video-like presentation, not a requirement for removing
this tearing.**

**F. Drive the present from the sink's own vsync.** For a browser, that is
`requestAnimationFrame`; for X11, the display's own sync. It is what decides when
the sink can actually show something, so it is the natural clock for A. Not a
separate option so much as how A should be scheduled.

## What is recommended

**A and C together, with D as an accelerator and F as the clock.** B is a
refinement of A's window at most. E only if the direction is video-like
presentation rather than this tearing.

The reason A and C come first is that they fix the mechanism rather than
approximate it: the sink stops showing states it has no reason to show, and it
stops paying for a full frame encode each time it does. Neither needs a frame
boundary, which matters because there is none to be had in this session mode.

## Chosen: A, and what it needed

A is the decision. The library side of it is in on master as of `client.TakeDirty`:
the persistent surface and the merging already existed, and what was missing was the
area changed since a caller last showed the screen. It returns that, merged by the
same rules `OnFrame` uses and consumed by the call, so a caller can show the
framebuffer at a display's cadence instead of as updates arrive.

    dirty := c.TakeDirty()
    if len(dirty) == 0 { continue }        // a still screen costs nothing
    for _, r := range dirty { encode(pix, fb.Stride(), r) }
    present()

C is still worth doing and is a change on the other side of the connection rather
than in this library: if the gateway encodes a full frame per call rather than only
the rectangles it was given, the encode cost is paid whether or not the call was
needed. What `TakeDirty` returns is exactly the region list that makes the cheaper
version possible.

D and F remain as described: use the markers when a session has them, and take the
clock from the sink, which for a browser means `requestAnimationFrame`.

## What has to be answered before choosing

1. **Does the gateway encode a full frame per update?** The selftest output seen
   earlier reports a JPEG per frame, which suggests it does, and if so C is the
   root cause fix rather than an optimisation.
2. **Can the sink keep a persistent surface?** A canvas can, and so can an `img`
   that is blitted into a canvas. A sink that only ever receives whole images
   cannot, and then A is the only option of the two.
3. **Latency or coherence, when they conflict?** A's window is about one display
   interval; B's would add a couple of milliseconds on top. Both are settings
   rather than constants, but the default has to be one of them.
