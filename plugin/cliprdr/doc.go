// Package cliprdr implements the clipboard virtual channel: text and files, in
// both directions.
//
// The channel is one of the oldest in RDP and the format list is the interesting
// part. Each side advertises what it can produce, and a format is advertised only
// when its data can actually be produced, because the other side will ask and
// then wait. An advertised format with nothing behind it hangs the paste rather
// than failing it.
//
// # Files
//
// File transfer used to open a path that arrived from the remote file descriptor
// list as a path on the local filesystem, which only ever worked on the machine
// whose own Explorer was sharing the clipboard. It now goes through
// [FileProvider]: the application supplies the manifest and the bytes, the
// application decides where they come from, and reads happen by range so that a
// file of any size costs no more memory than the range requested. Nothing file
// shaped is advertised until a provider is set.
//
// # Where the platform comes in
//
// The Windows build talks to the real clipboard. Everywhere else the clipboard
// lives in this process and the application owns it, which is what makes the
// channel usable from a headless client; see the !windows files for what that
// means for each method.
package cliprdr
