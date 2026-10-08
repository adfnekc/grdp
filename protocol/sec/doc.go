// Package sec is RDP security: the encryption of the session, and the licensing
// exchange that runs on top of it.
//
// With TLS or NLA this is thin. With standard RDP security it is the whole
// protection of the session, and that means RC4 with a key derived from the
// client's random values and the server's certificate, plus a rule that is easy
// to miss: the encryption keys are replaced after every 4096 packets, and both
// sides must do it in step. A client that does not will work for a while and
// then produce garbage, which looks like anything but a key rotation.
//
// The licensing PDUs travel encrypted even though the handshake that sets the
// encryption up is not finished when they are sent, so the flags on that first
// packet are their own small trap.
package sec
