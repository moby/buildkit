//go:build !windows

package llbsolver

func (b *provenanceBridge) GetFrontendID() string {
	return ""
}
