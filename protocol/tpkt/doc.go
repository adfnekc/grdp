// Package tpkt is the bottom of the RDP stack: the TPKT framing that every
// packet sits in, and the TLS and CredSSP handshake that runs before any RDP
// PDU does.
//
// A TPKT header is four bytes and says how long the packet is, which is what
// turns a byte stream into messages. Above that this package knows two things
// the layers above should not have to: how to wrap the connection in TLS, and
// how to run CredSSP over it for NLA, which is NTLM inside DER inside TLS.
//
// # Why it reports three kinds of handshake failure
//
// The information a caller needs about a failed connection is which stage it
// failed in, and this is the only layer that knows. Before the credentials are
// sent a failure is [TLSError] or [CredSSPError], and the account has not been
// proven wrong. After they are sent and the server stops answering it is
// [CredentialsError], which is what a refused logon looks like: Windows does not
// reply with an NTLM status, it drops the TLS connection, so what arrives is a
// TLS alert that on its own reads like a certificate problem.
//
// # Fast path
//
// Fast path packets have no TPKT header at all, only a two byte length. A
// session uses fast path for input and for most output, and the slow path for
// the rest, so both are handled here.
package tpkt
