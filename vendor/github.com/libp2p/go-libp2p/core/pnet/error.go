package pnet

// ErrNotInPrivateNetwork is an error that should be returned by libp2p when it
// tries to dial with ForcePrivateNetwork set and no PNet Protector
var ErrNotInPrivateNetwork = NewError("private network was not configured but" +
	" is enforced by the environment")

// Error is error type for ease of detecting PNet errors
type Error interface {
	IsPNetError() bool
}

// NewError creates new Error
func NewError(err string) error {
	return pnetErr("privnet: " + err)
}

// IsPNetError checks if given error is PNet Error
func IsPNetError(err error) bool {
	v, ok := err.(Error)
	return ok && v.IsPNetError()
}

type pnetErr string

var _ Error = (*pnetErr)(nil)

func (p pnetErr) Error() string {
	return string(p)
}

func (pnetErr) IsPNetError() bool {
	return true
}
