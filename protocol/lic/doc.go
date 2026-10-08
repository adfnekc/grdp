// Package lic holds the licensing PDUs, which an RDP client must answer before
// the session becomes usable.
//
// The exchange is a request for a licence, a response carrying the server's
// certificate, a second request with the client's hardware identifier, and a
// final response that is usually "there is nothing to license", which every
// server accepts. The details are in MS-RDPBCGR 5.5.
//
// Two things are easy to get wrong and were both wrong here once. The valid
// client licence sequence chooses between sending a stored licence and a new
// one, and sending the wrong one is refused rather than ignored. And the
// certificate parsing has to follow the server's own claims: a server that says
// it has no certificate is telling the truth, and treating it as an error stops
// a session that would otherwise have worked.
package lic
