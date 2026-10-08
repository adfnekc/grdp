// Package per implements the subset of PER encoding that MCS and the GCC blocks
// need: lengths, choices, enumerations, integers and octet streams.
//
// PER is not a general purpose encoding and this is not a general purpose
// implementation. It is the pieces RDP actually uses, written to be read
// alongside T.125's definitions rather than to be complete, and the names follow
// the specification's so that a reader can match them up.
//
// If a field here is wrong, everything after it is wrong too, because the
// encoding is positional and has no delimiters. That is why the readers return
// errors rather than zero values wherever a length or a tag comes off the wire.
package per
