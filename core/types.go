package core

import "github.com/adfnekc/grdp/emission"

// Transport is the byte stream a protocol layer sits on: a socket, or a TLS
// wrapper around one.
type Transport interface {
	Read(b []byte) (n int, err error)
	Write(b []byte) (n int, err error)
	Close() error

	On(event, listener interface{}) *emission.Emitter
	Once(event, listener interface{}) *emission.Emitter
	Emit(event interface{}, arguments ...interface{}) *emission.Emitter
}

// FastPathListener receives a decoded fast path update.
type FastPathListener interface {
	RecvFastPath(secFlag byte, s []byte)
}

// FastPathSender sends a fast path update.
type FastPathSender interface {
	SendFastPath(secFlag byte, s []byte) (int, error)
	// SendFastPathInput writes a client-to-server fast-path input PDU. The
	// first header byte carries both the action and the event count.
	SendFastPathInput(numEvents byte, s []byte) (int, error)
}

// ChannelSender writes to a named virtual channel.
type ChannelSender interface {
	SendToChannel(channel string, s []byte) (int, error)
}
