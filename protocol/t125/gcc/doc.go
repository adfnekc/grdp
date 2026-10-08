// Package gcc holds the GCC user data blocks that travel inside the MCS connect
// initial: the client's core settings, the set of virtual channels it wants, and
// its security settings.
//
// The core block is where the desktop size, the colour depth and the keyboard
// layout are stated, and those values are not merely a request. Several of them
// have to agree with the capability set sent later in the PDU layer, and the
// comments on the fields here say which. A mismatch is not always an error the
// server reports; sometimes it simply draws differently.
//
// The network block is the channel list, and it is built up by the layers that
// speak each channel through MCSClient.AddVirtualChannel, so that nothing is
// advertised without something behind it. A channel that is advertised and then
// not answered is worse than one that was never asked for.
package gcc
