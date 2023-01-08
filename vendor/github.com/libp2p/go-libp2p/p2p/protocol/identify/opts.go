package identify

type config struct {
	userAgent               string
	disableSignedPeerRecord bool
}

// Option is an option function for identify.
type Option func(*config)

// UserAgent sets the user agent this node will identify itself with to peers.
func UserAgent(ua string) Option {
	return func(cfg *config) {
		cfg.userAgent = ua
	}
}

// DisableSignedPeerRecord disables populating signed peer records on the outgoing Identify response
// and ONLY sends the unsigned addresses.
func DisableSignedPeerRecord() Option {
	return func(cfg *config) {
		cfg.disableSignedPeerRecord = true
	}
}
