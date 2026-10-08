// Package x224 carries the X.224 connection request and confirm, which is the
// first thing an RDP client says and the point at which the security protocol
// is chosen.
//
// The client requests one of three: standard RDP security, TLS, or CredSSP
// (NLA). The server answers with the one it will use, or with a failure code.
// The codes are the useful part of this package: a server that requires NLA can
// only be talked to with it, and the failure says so precisely, which is why
// [NegotiationFailure] carries the code rather than a sentence.
//
// A connection using standard RDP security sends no negotiation block at all,
// so the server cannot upgrade the connection, and the RC4 path in protocol/sec
// is used instead of TLS. That path works and has been verified, but it is the
// one that carries credentials inside an encryption scheme nobody would choose
// today.
package x224
