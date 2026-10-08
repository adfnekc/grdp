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
// This package used to import "C" while using no C, which made cgo a
// requirement for the whole module for no reason: the RDP client imports this
// package, so CGO_ENABLED=0 dropped channel.go entirely and the build failed
// with "undefined: plugin.CHANNEL_OPTION_INITIALIZED". The stray import is gone
// and the module builds without cgo now, which is what a library should do.
package plugin
