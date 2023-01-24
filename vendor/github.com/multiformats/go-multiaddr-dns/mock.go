package madns

import (
	"context"
	"net"
)

type MockResolver struct {
	IP  map[string][]net.IPAddr
	TXT map[string][]string
}

var _ BasicResolver = (*MockResolver)(nil)

func (r *MockResolver) LookupIPAddr(ctx context.Context, name string) ([]net.IPAddr, error) {
	results, ok := r.IP[name]
	if ok {
		return results, nil
	} else {
		return []net.IPAddr{}, nil
	}
}

func (r *MockResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	results, ok := r.TXT[name]
	if ok {
		return results, nil
	} else {
		return []string{}, nil
	}
}
