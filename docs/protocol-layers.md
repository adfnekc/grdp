# The protocol layers

A reference for someone porting or extending this library. Each package doc says
what its own layer does; this says how the layers fit together, what each one
relies on the one beneath it for, and what to look at first when something breaks
at a given layer.

The stack, from the socket upwards. Each package knows only about the one below
it, and the RDP packages share nothing with the VNC one except the socket.

    client          the API, and the framebuffer
    orders          rendering the drawing orders pdu parses
    codec           NSCodec, RemoteFX, ZGFX, AVC framing
    plugin/*        the virtual channels: clipboard, dynamic, graphics, display
    protocol/pdu    capabilities, updates, input, orders
    protocol/sec    encryption, licensing
    protocol/t125   MCS: channels, and the GCC blocks
    protocol/x224   the connection request, and the security choice
    protocol/nla    NTLMv2 and CredSSP, used by tpkt
    protocol/tpkt   framing, TLS, and the CredSSP handshake
    protocol/rfb    RFB (VNC), a different protocol, in parallel with all of the above
    core            sockets, readers, writers, RLE
    emission, glog  the event emitter and the logger

## What each layer is responsible for

### core

Sockets, bounded readers and writers over `io.Reader`, and the RLE decoder for
bitmap updates. It knows nothing about RDP.

Two things about it are load bearing. `Decompress` bounds its own output, because
the width, height and pixel format it is given come off the wire and are used to
size an allocation before any input is read. And the read and write helpers are
explicit about endianness, because the protocol mixes both; `ReadUint16LE` and
`ReadUint16BE` both exist on purpose.

`core` is where a wire value most often becomes an allocation. When adding to it,
bound first.

### protocol/tpkt

TPKT framing, and the TLS and CredSSP handshake. Fast path packets have no TPKT
header at all, only a two byte length, so both framings live here.

It is the only layer that knows which stage a handshake failure happened in, and
it reports that as three typed errors: `TLSError`, `CredSSPError` and
`CredentialsError`. Anything above it that wants to tell "the certificate" from
"the account" relies on that, and nothing above it can work it out for itself.

**Two of those three used to never reach a caller, and that was a bug. One of
them is fixed.** TPKT armed its first read in `New`, which is before TLS exists:

    func New(s *core.SocketLayer, ntlm *nla.NTLMv2) *TPKT {
        ...
        core.StartReadBytes(2, s, t.recvHeader)   // before the handshake
        ...
    }

Reading before the handshake is necessary, because the X.224 reply has to arrive
first. Not handing the socket over cleanly was not. When the handshake failed the
read loop was usually the one holding the socket, so it reported the failure as a
plain transport error while `StartTLS` and `StartNLA` were still inside their own
reads, and the typed errors were never constructed. It also meant the read loop
was reading the raw socket while the handshake needed those bytes, a race that had
been winning by luck.

The loop is now held back from construction and resumed by the one place that
knows what it should be reading, which is the security exchange in x224. Only the
read that starts a new message is held back; the reads that make up a single
message proceed, because a TPKT header arrives over two or three of them.
`armRead` in TPKT is the single arming point and it refuses to arm while one is
outstanding, which is what stops a resume during a delivery from starting a second
reader. Two readers split the stream between them and the session desynchronises,
which looks like a malformed server rather than a client with two readers.

Verified after the change: xrdp over TLS delivers a full frame, both drawing paths
composite to the same pixels as the independent composite, and the suite passes
with and without cgo and under the race detector.

`ErrTLSFailure` is now reached and was verified with a stub that completes the
X.224 exchange, selects TLS, and then fails the handshake:

    kind: TLS — the handshake failed, so check the certificate before the credentials

`ErrCredSSP` is verified now, with the same stub making the other choice: complete
the TLS handshake, then send nothing.

    kind: CredSSP — the exchange failed before the credentials were sent, so it is
          not the account

Both sentinels took a stub that gets three things right, and all three went wrong
here first, each producing a result that pointed at the client instead:

  - the confirm has to be a complete 19 byte packet. One byte short and the client
    waits for a reply that never comes, then fails in X.224.
  - the RDP_NEG_RSP fields are type, flags, length, then selectedProtocol, and the
    protocol is the last four bytes little endian. Building it in the other order
    selects standard RDP security, so no handshake runs at all and the bare
    transport error that results looks like the client refusing to negotiate.
  - 0 is standard RDP security, 1 is TLS, 2 is HYBRID. A stub answering a HYBRID
    request with 1 tests the TLS path, not the CredSSP one, and says so only at
    debug level.

The client was right in all three cases. That is worth remembering when a test
harness and the code disagree: this repository's history is that the harness is
wrong more often than the code, and the way to tell is to capture what a real
server sends rather than to build it from the specification, which is how the
confirm above was eventually got right.

### protocol/nla

NTLMv2 and CredSSP. The layer above sees a handshake that completes or does not;
challenge responses, session keys, sealing and the public key binding hash are
all here.

The binding hash is computed over the server's SubjectPublicKeyInfo and a client
nonce, and the exact bytes matter. The session key comes out of the NTLM exchange
and is then sealed; it is not generated locally, and a client that generates one
completes the handshake and then fails at the first sealed message.

NTLMv2 has test vectors and they are checked. The rest was aligned against
impacket field by field, which is what caught the session key and sealing faults.

### protocol/x224

The X.224 connection request, and the point where the security protocol is
chosen. Standard RDP security sends no negotiation block at all, which is what
stops the server upgrading the connection; the RC4 path in `sec` is then used
instead of TLS.

The failure codes are the useful part. `NegotiationFailure` carries the code so
that a caller can tell "this server requires NLA" from "this server refuses TLS"
without parsing a sentence.

### protocol/t125

MCS. It joins the global channel and one channel per virtual channel, and frames
what travels on them. Almost all of it is bookkeeping.

The `SetClient*` methods are not just a channel list: each sets the early
capability flag the server needs in order to offer that channel, so adding a
channel means both. `DisconnectError` is how a session ends when the server
decides, and its reason byte is carried but deliberately not interpreted, because
the byte a real server sends does not decode as any value T.125 defines.

The `gcc` subpackage holds the GCC blocks: the client's core settings, its
channel list and its security settings. Several of those values have to agree
with the capability set sent later by `pdu`, and the field comments say which. A
mismatch is not always reported; sometimes the server simply draws differently.

The `per` and `ber` subpackages are encoding primitives, not general
implementations. PER is positional and has no delimiters, so a wrong field size
misaligns everything after it, which is why the readers return errors rather than
zero values.

### protocol/sec

Encryption, and the licensing exchange. With TLS or NLA this is thin. With
standard RDP security it is the whole protection of the session.

Two traps live here. The encryption keys are replaced every 4096 packets and both
sides must do it in step; a client that does not works for a while and then
produces garbage, which looks like anything but a key rotation. And the licensing
PDUs travel encrypted even though the handshake that sets the encryption up is
not finished when the first of them is sent, so the flags on that packet are
their own small oddity.

`sec` also carries the session key rotation counter and the licensing state, both
per connection.

### protocol/pdu

Capabilities, updates, input and orders. It answers the server's demand active
with the client's capability set, which is where what the server sends next is
decided.

This is the layer with the most state and the most ways to be subtly wrong:

* **The parse state is per connection.** Delta coordinates refer back to the
  previous order of the same type, the drawing bounds carry over between orders
  that omit them, and the caches accumulate. That state is `OrderState`, and it
  is reset when the server deactivates and reactivates a session. Sharing one
  between two sessions would draw each other's orders, and the fault would look
  like a rendering bug in whichever session lost.
* **The capability constants are not in numerical order of the types they name.**
  `CAPSTYPE_BITMAP` is `0x0002` and `CAPSTYPE_ORDER` is `0x0003`. An earlier
  version had them the other way round, which produced a session that negotiated
  warmly and drew the wrong thing.
* **Order field lengths are variable.** A cached bitmap's width, height, cache
  index and length are compact encodings of one to four bytes each. Reading them
  as fixed widths steals a byte from the following field.
* **Field flag bytes can be shortened.** `TS_ZERO_FIELD_BYTE_BIT0` and `BIT1`
  remove the unused low bits of the flag byte, and the remaining bytes are read
  from the low end upwards. Reading them the other way turns `0x02` into `0x200`,
  which is outside the field list.
* **Colours in orders are three bytes**, BGR. Reading four misaligns everything
  after, and the colour table entries beside them are four byte quads, because
  the protocol is not consistent about this.
* **The pointer cache size is a contract.** Advertising twenty slots makes the
  server stop resending shapes, so a client with nowhere to put them loses the
  cursor.

### codec

NSCodec, RemoteFX (RFX), ZGFX and the framing of AVC420 and AVC444.

Every decoder here is checked byte for byte against libfreerdp: the streams are
produced by FreeRDP's encoders and the expected output by FreeRDP's decoders, via
`scripts/gen-codec-vectors.sh`. That is why these can be trusted further than
their own tests, and it is worth keeping when adding one. ZGFX is the exception in
one detail: FreeRDP's compressor is a stub, so the streams come from this
repository's test encoder and FreeRDP's decoder still supplies the expected
output.

The decoders do not trust their inputs. Widths, heights, counts and compressed
sizes all come off the wire and are bounded before they are used.

H.264 is framed and not decoded. `AVCDecoder` is the hook, and the codecs are
advertised only when a caller has set one.

### plugin

The virtual channels. `plugin` is the framework: channel names, options, and the
chunked transport that wraps a payload. The channels themselves are
`plugin/cliprdr` for the clipboard and files, `plugin/drdynvc` for the dynamic
channel layer, `plugin/rdpgfx` for EGFX and `plugin/disp` for Display Control.

Two things here surprise people. A dynamic channel can be created by **either**
side, and Display Control is a client to server one, so a client that only waits
for channels the server opens never gets one; it has to ask. And the channel name
in a create request needs its NUL terminator, without which the request is
answered with silence.

`plugin` is also why this module used to require cgo. It imported `"C"` while
using no C, and because the RDP client imports it, `CGO_ENABLED=0` dropped the
whole file. The stray import is gone and the module builds without cgo now.

### orders

Rendering. It takes the orders `pdu` parsed and draws them into a BGRA buffer,
handling the bitmap and glyph caches, clipping to each order's bounds, and the
alternate secondary orders.

The cache is the part to understand first: MEMBLT draws from cached bitmaps, the
server fills the cache with separate cache orders, and a client that ignores those
draws nothing or draws the previous contents. The server decides what to cache,
so this cannot be skipped.

Unsupported orders are counted rather than dropped silently, and `Unsupported`
exposes the breakdown, because "rendered" and "rendered what I could" look the
same on screen.

### protocol/rfb

RFB (VNC), which is a different protocol. It shares no framing, no security and
no PDU layer with the RDP side; only the socket and `core`.

It keeps its own framebuffer, because an RFB server chooses the pixel format per
connection rather than sending BGRA. CopyRect is the reason a framebuffer is
needed rather than optional: it carries source coordinates and no pixels, so it
cannot be resolved without retaining the last frame.

### client

The API. It builds the stack, drives the connection, and composites every drawing
path into one `Framebuffer` so that a caller does not have to care which path the
server chose.

It is also where the failure kinds are assembled from the typed errors the lower
layers produce, because the stage information is in the transport and the caller
cannot see it.

## When something breaks, look here first

| Symptom | Start at |
| --- | --- |
| Nothing connects, no RDP bytes at all | `core`, then `tpkt` framing |
| Handshake dies, or looks like a TLS problem | `tpkt`, `nla`; check which of the three typed errors it is |
| Connects but the session never becomes ready | `x224` security choice, then `t125` channel joins |
| Session ready but the screen never draws | which drawing path was advertised, then `pdu` dispatch |
| Screen draws but is subtly wrong | `pdu` parse state: order lengths, flag bytes, colours |
| Screen is blank after ordering EGFX or orders | a codec or order that is not implemented, which the counters and the log name |
| Cursor missing, or a shape that never changes | the pointer cache: advertised slots against what the client stores |
| Input lands nowhere | fast path against slow path, then the scancode conventions |
| Something works for a while and then corrupts | `sec` key rotation at 4096 packets |
| A size or a value disagrees with another implementation | check FreeRDP's source or a capture before changing anything |

## Adding a layer or a channel

1. Read FreeRDP's implementation of that part first. Every question this
   repository has settled by reading it went right, and every one settled by
   reasoning from the specification went wrong.
2. Bound anything that came off the wire before it sizes anything.
3. Return an error rather than reading nothing when a field cannot be read.
4. Feed the parser real bytes in a test. A test that builds its input in Go
   cannot see a misalignment.
5. Add the capability or the channel entry only when something is behind it, and
   remember that both the channel list and the early capability flag are usually
   needed.
6. Add a doc comment for every exported identifier, and run
   `go run ./cmd/docaudit ./...`.
