// Package core is the layer everything else is built on: bounded readers and
// writers over io.Reader and io.Writer, a net.Conn wrapper that can be upgraded
// to TLS, small string and pixel conversions, and the RLE bitmap decompressor.
//
// The read and write helpers (ReadBytes, ReadUint16LE, WriteUInt32BE and the
// rest) read and write exactly the bytes they name and say which endianness
// they use, because the protocol mixes both.
//
// Traps:
//
//   - Decompress sizes its output from the width, height and bytes per pixel it
//     is given, so those must come from a bounded source; it also applies its
//     own ceiling. On malformed input it returns the part that decoded together
//     with an error, and only the interleaved form can tell that a stream ended
//     early, so a nil error does not always mean a complete picture.
//   - StartReadBytes runs its callback on a new goroutine and calls it exactly
//     once, including on error; the callback must not assume it runs on the
//     caller's goroutine.
package core
