// Package nla implements NTLMv2 and CredSSP, which is how RDP authenticates the
// account before the session exists.
//
// CredSSP is NTLM wrapped in a DER encoded TSRequest wrapped in TLS, and NLA is
// CredSSP used to prove the account to the server. The layers above see only a
// handshake that either completes or does not; everything about challenge
// responses, session keys, sealing and the public key binding hash lives here.
//
// # The parts that are easy to get wrong
//
// The binding hash is computed over the server's SubjectPublicKeyInfo, taken from
// the certificate the TLS handshake already validated, and over a client nonce,
// and the exact bytes matter: computing it over the wrong encoding of the
// certificate produces a hash that is the right length and wrong, which the
// server reports much later and less clearly.
//
// The session key is not invented. It comes out of the NTLM exchange and is then
// sealed with the exported session key; a client that generates its own random
// session key will complete the handshake and then fail at the first sealed
// message, because the two sides are using different keys.
//
// Sealing and unsealing are not symmetric in the way they look: the RC4 stream
// is not reset between messages, so the order of operations against the cipher
// has to match what the server does.
//
// # Testing it
//
// NTLMv2 has test vectors and they are checked. Beyond that this code was
// aligned against impacket's implementation field by field, which is what caught
// the faults listed above, and it has been run end to end against a real Windows
// 10 host that requires NLA.
package nla
