// Package t125 is MCS, the layer that carries RDP's channels over the connection
// X.224 established.
//
// It joins the global channel and one channel per virtual channel, and it frames
// what travels on them. Almost all of it is bookkeeping. The parts a reader
// usually wants are here:
//
//   - [MCSClient.SetClientDynvcProtocol] and friends, which are how a session
//     opts into a channel. Each also sets the early capability flag the server
//     needs in order to offer that channel at all, so these are not only a
//     channel list.
//   - [DisconnectError], which is how a session ends when the server decides it
//     should. Its reason byte is carried but not interpreted, because the byte a
//     real server sends does not decode as any of the values T.125 defines.
//
// The channel identifiers are assigned by the server and are per connection.
// Nothing here may be shared between sessions.
package t125
