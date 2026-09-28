# Captured graphics messages

These are whole RDPGFX PDUs taken off the graphics channel of a real Windows 10
server: the decompressed bytes, with the ZGFX framing removed, as they appeared
on the wire.

* `windows_createsurface.bin` - creates surface 0 at 1024x768, ARGB8888.
* `windows_wiretosurface.bin` - places a 48x40 RemoteFX tile at (971,728).

They are here because hand written PDUs in the tests and the parser shared a
misreading of the WireToSurface header (an extra byte between the pixel format
and the destination rectangle), so the tests agreed with the bug. Real bytes do
not have that problem, and the rectangle in the second file is off centre
enough that reading it at the wrong offset is obvious.

Nothing regenerates them. They are a capture, and that is the point.
