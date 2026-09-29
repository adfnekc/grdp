// Package plugin is the virtual channel framework.
//
// A virtual channel is a named byte stream multiplexed over the RDP connection
// and framed in chunks of at most CHANNEL_CHUNK_LENGTH bytes. Channels
// registers a ChannelTransport under the name it reports, reassembles the
// chunks of each channel separately, and hands each complete message to that
// transport's Process method.
//
// Most callers never use this package directly: the concrete channels built on
// it are plugin/cliprdr (clipboard), plugin/drdynvc (dynamic channels) and
// plugin/rdpgfx (graphics). Implement ChannelTransport to add another.
//
// The exported names here mirror the C virtual channel API that FreeRDP and
// Windows implement, which is why they keep its capitalisation.
//
// Trap: this package imports "C" and uses no C, so cgo is required for no
// reason, and with it the whole module: the RDP client imports this package,
// and CGO_ENABLED=0 go build ./... fails with "build constraints exclude all Go
// files" for plugin. Deleting the stray import in channel.go is what would let
// the module build without cgo again.
package plugin
