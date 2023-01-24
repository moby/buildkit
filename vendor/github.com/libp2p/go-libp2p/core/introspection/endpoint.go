package introspection

// Endpoint is the interface to be implemented by introspection endpoints.
//
// An introspection endpoint makes introspection data accessible to external
// consumers, over, for example, WebSockets, or TCP, or libp2p itself.
//
// Experimental.
type Endpoint interface {
	// Start starts the introspection endpoint. It must only be called once, and
	// once the server is started, subsequent calls made without first calling
	// Close will error.
	Start() error

	// Close stops the introspection endpoint. Calls to Close on an already
	// closed endpoint (or an unstarted endpoint) must noop.
	Close() error

	// ListenAddrs returns the listen addresses of this endpoint.
	ListenAddrs() []string

	// Sessions returns the ongoing sessions of this endpoint.
	Sessions() []*Session
}

// Session represents an introspection session.
type Session struct {
	// RemoteAddr is the remote address of the session.
	RemoteAddr string
}
