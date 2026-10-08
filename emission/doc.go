// Package emission is an event emitter: named events with listener functions,
// and an optional panic recoverer.
//
// It is vendored from chuckpreslar/emission rather than imported, with one local
// fix, so that this library's dependency list stays short and its behaviour is
// not changed by an upstream release. The events the protocol layers emit are
// documented on the layers that emit them, and client.On is the surface most
// callers use.
//
// Listeners run on the goroutine that emitted the event, which for anything
// carrying network data is the session's read goroutine. A listener that blocks
// stops the session.
package emission
