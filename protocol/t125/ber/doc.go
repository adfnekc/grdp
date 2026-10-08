// Package ber implements the BER/DER primitives that CredSSP's TSRequest is
// built from: tags, lengths, integers, octet strings and sequences.
//
// DER is the subset of BER with one encoding for each value, which is what makes
// it possible to hash a structure and have both sides agree. CredSSP uses it to
// carry NTLM messages and to bind the authentication to the TLS certificate.
//
// The length encoding is the part worth reading carefully: a DER length is one
// byte below 128 and, above that, a first byte saying how many follow. A reader
// that assumes one byte will misread every message large enough to matter, and
// the NTLM messages here are large enough.
package ber
