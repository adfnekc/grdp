# Decoding AVC streams from EGFX

This is an exploration branch. Nothing here is finished, and the document is the
record of what the options are and what has been verified about them.

## The two problems, which are separate

When a server sends H.264 over EGFX, there are two things to solve, and only one
of them is hard to find a library for.

1. **Decoding the bitstream.** `plugin/rdpgfx` defines an `AVCDecoder` interface
   and ships nothing that implements it. A client without one declines the AVC
   codecs, which is why this library never receives AVC today.
2. **Merging the AVC444 layers.** An AVC444 update carries two H.264 sub-streams:
   a luma layer at full resolution and a chroma layer at a reduced one. No
   general purpose decoder does this, because the two are independent bitstreams
   that were never meant to be decoded together. FreeRDP does it itself, in
   `avc444_decompress` in `libfreerdp/codec/h264.c`, using a
   `YUV420CombineToYUV444` primitive, and it has had real bugs in exactly this
   code (FreeRDP issues 11040 and 6602). Writing it here is unavoidable whichever
   decoder is chosen.

## What the client already has

* `codec.ParseAVC420` and `codec.ParseAVC444` do the framing: the metablock, the
  region rectangles, the per region quantisation, the luma and chroma selector,
  and the streams as opaque Annex B bytes. The parser deliberately does not scan
  the bitstream, which is what the decoder is for.
* `plugin/rdpgfx.AVCDecoder` is the seam: `SupportsAVCCodec(codecID)` decides what
  the client advertises, and `DecodeAVC(codecID, au, width, height)` returns BGRA
  for the whole surface, from which the client copies only the regions the server
  marked as changed.
* The client advertises exactly the codecs its decoder reports, so installing a
  decoder is the only switch needed to start receiving AVC.

## Candidate decoders, and what was verified about them

Checked against the repositories rather than against the descriptions:

| Library | Licence | What matters |
| --- | --- | --- |
| `oops1/go.264` | BSD-2-Clause | The only candidate claiming a complete High profile decoder: CABAC complete in both directions, CAVLC, in-loop deblocking, all P partitions, B slices, weighted prediction, reference picture management. States it is bit exact against ffmpeg on twenty-nine clips, builds with `CGO_ENABLED=0`. New, no stars |
| `Eyevinn/hi264` | MIT | Describes itself as an **IDR** decoder and a bitstream generator. An RDP stream is mostly P slices, so this cannot decode one on its own |
| `mgvs/go-openh264` | BSD-2-Clause | Describes itself as implementing "just the subset needed to turn a keyframe into an image". Same limit as above |

Entropy coding decides this: a server encodes with hardware H.264 encoders and
the result is a main or high profile stream with CABAC. A baseline only or
keyframe only decoder cannot read it, however fast it is.

So there is one viable candidate, and a new library with no stars is a risk that
has to be paid down by measurement rather than by its own README, in the same way
the codecs in this repository are checked byte for byte against libfreerdp.

## Two architectures, and the second has a precedent in this protocol

**In process, returning pixels.** Implement `AVCDecoder` with the pure Go decoder
plus the AVC444 merge. The gateway gets BGRA it can encode or forward, and the
library keeps its property of being pure Go with no cgo.

**Passthrough to the browser.** For a headless web gateway there is no reason to
decode on the server side at all: forward the Annex B access unit and its geometry
to the browser and let `VideoDecoder` from WebCodecs decode it, possibly on the
GPU. This is not a theory: `wasm-rdp` does exactly this in commit `eb9cba5`,
titled "H.264/RDPGFX video via WebCodecs (AVC420 passthrough)", where the EGFX
client runs in raw passthrough mode and the browser decodes. The cost is that
this bypasses the `AVCDecoder` interface, which returns pixels, so it is a
different shape of change: the gateway forwards bitstreams instead of frames.

The two are not exclusive. A gateway that forwards is cheaper and better, and an
in library decoder is what makes the library useful to something that is not a
browser.

## What should be done first, in order

1. **Get a real stream.** The client cannot receive AVC without a decoder, so the
   first step is to advertise AVC420 and AVC444 without one and dump the
   bitstream to a file, against a server configured for H.264. That costs a
   temporary change and produces the test vector everything else needs.
2. **Decode it, and check it against a reference.** Feed the capture to the
   candidate decoder and compare the YUV against ffmpeg on the same bitstream.
   The claim to test is bit exactness on a stream this protocol actually carries,
   not on conformance clips.
3. **Write the AVC444 merge**, in pure Go, against FreeRDP's behaviour, and test
   it with two synthetic planes before trusting it on a capture.
4. **Then decide the architecture**, with the numbers from 1 to 3 rather than from
   this document.

## Open questions

* Does the candidate decoder handle the multiple slices and the reference
  structure a hardware encoder produces, on a stream of this kind rather than on
  conformance clips?
* How does an AVC444 v2 (`0x0F`) access unit differ in practice from v1 (`0x0E`)?
  The codec ids are known and the difference is in the chroma layer's handling,
  and it is the shape of the data that will settle it.
* Is a pure Go decoder fast enough to be worth having in process, or is the
  browser the only sensible place to decode? A 1080p stream is a lot of work per
  frame and the answer is a measurement, not an opinion.

## The first attempt, and what it found

Step 1 was tried. The tool is on this branch: a `avcCapture` decoder in
`cmd/rdpcli` that claims every AVC codec without decoding, writes each access unit
to a directory, and returns a blank surface so that a capture run draws black. It
is reached with `-capture-avc DIR` alongside `-egfx`. `client.Client.Gfx()` was
added so that a decoder can be installed before `Login`, and the graphics client
is now created in `newRdpClient` rather than during the connection sequence,
because what the client advertises is decided while it connects.

**The server resets the connection as soon as the AVC capabilities are
advertised.** Nothing arrives, no access unit is written, and the reset comes
before any surface data: the trace is a channel opening, the capability
advertisement going out, and then `read: connection reset by peer`. The control
run, with the same settings and without `-capture-avc`, runs to its timeout with
the session up. So the two are correlated, and the advertisement itself is what
the server refuses.

This is a more interesting result than a capture would have been: the client
cannot currently ask for AVC at all against this target, so the decoder question
cannot even be reached yet. What is not known is why the advertisement is
refused. The candidates, in the order worth checking:

* The capability versions offered. This client offers 8.0, 8.1, and 10.0 when
  AVC444 is claimed; AVC420 rides on the 8.1 flag. A server may require a
  different set or a different order.
* The flags. `THINCLIENT` is set on every set, which is what makes a server
  prefer RemoteFX, and it is not obvious that it may be combined with AVC.
* Whether AVC444 needs something else as well. Multitransport and network
  characteristics detection share the message channel that this client does not
  have, and AVC444 is the codec that goes with that family.

The way to settle it is FreeRDP's own capability advertisement, which is the
reference for what a working client sends, and then sending exactly that. The
first grep for it did not find the code, so this is where the next attempt starts
rather than something already answered.

## Deliberately not decided

Nothing here has been implemented. No dependency has been added. The branch exists
so that the capture in step 1 can be taken, and so that the numbers from steps 1
to 3 decide the rest.
