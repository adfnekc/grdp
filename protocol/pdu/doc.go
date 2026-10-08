// Package pdu is the RDP PDU layer: capabilities, session updates, input, and the
// drawing orders that the orders package renders.
//
// It sits on protocol/sec and turns its byte stream into structures. It answers
// the server's demand active with the client's capability set, which is where
// what the server sends next is decided, and it dispatches updates on the
// "bitmap", "pointer", "orders" and "surface-bits" events.
//
// # State, and why it is per connection
//
// Parsing an order needs to know what the previous ones were. Delta coordinates
// are relative to the last order of the same type, the drawing bounds carry over
// between orders that omit them, and the bitmap and glyph caches accumulate. That
// state lives in [OrderState] and is reset when the server deactivates and
// reactivates the session, which is how a new session begins on the same
// connection. It is per connection and must not be shared: two sessions sharing
// one would draw each other's orders, and the fault would look like a rendering
// bug in whichever one lost.
//
// # What is easy to get wrong
//
// The capability constants are not in numerical order of the types they name:
// CAPSTYPE_BITMAP is 0x0002 and CAPSTYPE_ORDER is 0x0003, and an earlier version
// of this package had them the other way round, which produced a session that
// negotiated warmly and drew the wrong thing.
//
// Order lengths are variable. A cached bitmap's width, height, cache index and
// length are compact encodings of one to four bytes each, and reading them as
// fixed widths steals a byte from the following field. A parser that cannot read
// what it needs must return an error rather than reading nothing, because the
// stream is positional: the next order will be read from the wrong place and the
// symptom will appear there instead.
package pdu
