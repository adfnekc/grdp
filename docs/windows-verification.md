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
