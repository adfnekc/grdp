# File transfer performance

A file read from a remote over the clipboard runs at roughly 9 to 13 MB/s. This
file records what was measured while trying to change that, including five
client-side attempts that were measured and did not help, and one that did.

It is written down because the useful result here is negative, and negative
results are the ones that get rediscovered. Every number below was measured on
the machine and against the target named, not inferred.

## What the transfer costs, and what it does not

Measured with a 256 MiB file on a Windows 10 LTSC 19041 target, read over the
clipboard channel in 1 MiB ranges:

| | |
| --- | --- |
| RDP clipboard read | 8 to 13 MB/s, depending on how busy the virtual machine is |
| Raw TCP over the same path | **330 to 420 Mbit/s** (41 to 52 MB/s) |
| The target's negotiated link speed | **1 Gbps** on both adapters |
| Idle round trip time | 0.9 ms |
| Round trip time during a bulk transfer | 54 ms average, 262 ms peak |

The raw figure was taken with a TCP listener on the target sending a fixed block
and a reader here counting bytes. It needs nothing from this library, which is
the point: it is what the path can do when RDP is not in it.

## What is ruled out

* **The link is not 100 Mbit/s.** The adapters report 1 Gbps and the path carries
  330 to 420 Mbit/s. The RDP figure happens to sit near 100 Mbit/s, which is what
  suggested the link, and the suggestion is wrong.
* **It is not this library's CPU cost.** Removing the reflection and the per
  listener goroutine from event delivery cut user CPU by about 20 percent on the
  same transfer (6.55 and 6.00 seconds to 5.09 and 5.14) and moved throughput not
  at all. See below.
* **It is not the client's syscall count.** Reading through a socket buffer, so
  that one call serves many PDUs instead of the two reads per PDU the TPKT framing
  costs, changed neither throughput nor system time.
* **It is not a fixed per range cost.** Reading in 64 KiB, 256 KiB, 1 MiB and
  4 MiB ranges, a 64 fold span, gave 8.03, 8.13, 8.43 and 9.25 MB/s: throughput
  is flat, and the time per range is proportional to its size. That is a
  bandwidth shaped limit rather than a latency shaped one, and it eliminated the
  queueing delay theory that the round trip time inflation had suggested.
* **It is not the remote's disk.** A second pass over the same file, fully cached,
  was only about 1.14 times faster.
* **It is not the client's request cadence.** Keeping 1, 2, 4 and 8 range
  requests in flight gave 8.68, 10.01, 9.53 and 8.35 MB/s. It does not scale.
  `ReadRemoteFile` keeps a pending table keyed by stream id, so this needs no
  change to the library, which makes it a clean negative.

## The five client-side attempts

| Attempt | Result |
| --- | --- |
| Remove reflection and the per listener goroutine from event delivery | User CPU down about 20 percent. Throughput unchanged. **Kept**, because CPU is worth having on its own, and it also removed a class of concurrency bug |
| Read through a socket buffer | No change in throughput or system time. Reverted |
| Pipeline range requests | No scaling from 1 to 8 in flight |
| Declare `CONNECTION_TYPE_AUTODETECT` with `RNS_UD_CS_VALID_CONNECTION_TYPE` | Slightly slower (8.83 and 8.96 against 9.28 and 9.69). Reverted |
| Declare and answer network characteristics detection | The server accepted the request and named channel 0, so no channel exists and the phase cannot happen. See below |

The first of these is the only one that measured as worth keeping, and it is
worth keeping for the CPU rather than for the transfer: on a gateway that hosts
several sessions per process, bytes per second per session is not the number that
matters, CPU per session is.

## Network characteristics detection

RDP has a phase where the server measures the connection and learns its
bandwidth: it sends RTT and bandwidth probes and the client answers with what it
counted, and the results come back in `RDP_NETCHAR_RESULTS`. mstsc always asks
for it, and this client did not implement any of it. That looked like the most
promising remaining direction, and it was the hypothesis this work started from.

What the specification and FreeRDP settle, and what was verified:

* The message channel is **not** `TS_UD_SC_NET.MCSChannelId`. That field is the
  I/O channel, which is the global channel 1003, and reading it as the message
  channel joins a channel that is already joined and breaks the connect sequence.
  This mistake was made here and the connection failed until it was reverted.
* The message channel is asked for by the **client**, in a `CS_MCS_MSGCHANNEL`
  block, and named by the server in `SC_MCS_MSGCHANNEL`. FreeRDP's function for
  reading it is `gcc_read_client_message_channel_data`, and the direction in that
  name is the whole answer to "can the client declare its network quality at
  connect time".
* The client block is eight bytes, which is a four byte header and four bytes of
  flags, and carries no channel id.
* The specification says the detection PDUs MUST only be sent over the message
  channel, so a client that does not join one cannot be measured at all.

Implemented and measured against the target: the server **replies** to the
client block, which confirms the block's type and layout, and the reply names
**channel 0**. There is no message channel, so the phase cannot happen, and no
probe PDU appeared on any channel. The code was not kept: it is correct, and it
would make this client match mstsc, but on the only target available it bought
nothing, and "another Windows version might assign a channel" is a guess rather
than a measurement.

## What the numbers say the limit is

Twelve times slower than the same path's raw capacity, flat across range sizes,
unaffected by anything the client does: the limit is inside the RDP layer or on
the remote's side of it. The candidates that remain are the remote's clipboard
file producer and whatever pacing the RDP stack applies above the socket. Neither
is reachable from this library.

To separate them, the next useful measurement is not here. It is a raw test on
the host, an SMB copy or `iperf3` between the target and the host, which says
whether the virtual switch itself is slow. If SMB also lands near 10 MB/s then
the problem is the virtual network and RDP is innocent; if SMB reaches 100 MB/s
then RDP is pacing itself, and the place to look is the server's clipboard
handling rather than the client.

## Mistakes made, and what they cost

Recorded because several of them are the same mistake.

* **The 100 Mbit/s link was assumed to be the answer** because 12.5 MB/s is
  almost exactly 100 Mbit/s. It was in the first paragraph of the investigation
  and it was wrong; one command asking the adapters for their link speed would
  have settled it at the start.
* **A six second capture was read as "the target has no desktop"**. The shell had
  not drawn yet. A control run of the unmodified code, and a longer wait, both
  said the target was fine. The conclusion was stated before it was checked.
* **`TS_UD_SC_NET.MCSChannelId` was read as the message channel**, from the
  specification's prose rather than from FreeRDP's code, and the resulting change
  broke the connect sequence. The rule about settling against FreeRDP is in the
  porting guide for exactly this reason, and it was broken here.
* **`-log 4` was read as DEBUG**. It is ERROR: TRACE is 0 and DEBUG is 1. Four
  runs were spent concluding "no logs appeared, so nothing happened" from output
  that had been filtered away.
* **Too much load was put on the target.** About twenty 256 MiB transfers, five
  gigabytes of traffic, thirty logons, and a 256 MiB file left behind for hours.
  The question did not need a fraction of that. The file has been removed and the
  target confirms it, and the lesson is to bound an experiment before running it
  rather than after.
* **Throughput drift was noticed and worked around rather than stopped.** When
  the figures fell from 13 to 8.5 MB/s, re-running the baseline in the same
  conditions was the right control and it was done, but the session should have
  paused there rather than continuing four more configurations.

## If this is picked up again

Measurement first, and cheaply:

1. Ask the adapters for their link speed. It is one command and it eliminates the
   first wrong theory immediately.
2. Take a raw figure over the same path before touching the client. Without it
   there is no way to tell an RDP limit from a network one.
3. Sweep the range size before theorising about latency or bandwidth. The shape of
   that curve is the answer, and it cost four runs.
4. Bound the load, and count it. Twenty transfers was ten times more than needed.
5. When a change is proposed, write down in advance what measurement would show
   it worked. Four of the five attempts here could not have shown anything,
   because throughput was never going to move.
