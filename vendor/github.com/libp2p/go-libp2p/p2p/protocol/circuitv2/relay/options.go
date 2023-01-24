package relay

type Option func(*Relay) error

// WithResources is a Relay option that sets specific relay resources for the relay.
func WithResources(rc Resources) Option {
	return func(r *Relay) error {
		r.rc = rc
		return nil
	}
}

// WithLimit is a Relay option that sets only the relayed connection limits for the relay.
func WithLimit(limit *RelayLimit) Option {
	return func(r *Relay) error {
		r.rc.Limit = limit
		return nil
	}
}

// WithACL is a Relay option that supplies an ACLFilter for access control.
func WithACL(acl ACLFilter) Option {
	return func(r *Relay) error {
		r.acl = acl
		return nil
	}
}
