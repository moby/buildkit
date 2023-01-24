package control

// DisconnectReason communicates the reason why a connection is being closed.
//
// A zero value stands for "no reason" / NA.
//
// This is an EXPERIMENTAL type. It will change in the future. Refer to the
// connmgr.ConnectionGater godoc for more info.
type DisconnectReason int
