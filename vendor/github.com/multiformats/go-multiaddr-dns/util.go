package madns

import (
	"context"

	ma "github.com/multiformats/go-multiaddr"
)

func Matches(maddr ma.Multiaddr) (matches bool) {
	ma.ForEach(maddr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case DnsProtocol.Code, Dns4Protocol.Code, Dns6Protocol.Code, DnsaddrProtocol.Code:
			matches = true
		}
		return !matches
	})
	return matches
}

func Resolve(ctx context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	return DefaultResolver.Resolve(ctx, maddr)
}

// counts the number of components in the multiaddr
func addrLen(maddr ma.Multiaddr) int {
	length := 0
	ma.ForEach(maddr, func(_ ma.Component) bool {
		length++
		return true
	})
	return length
}

// trims `offset` components from the beginning of the multiaddr.
func offset(maddr ma.Multiaddr, offset int) ma.Multiaddr {
	_, after := ma.SplitFunc(maddr, func(c ma.Component) bool {
		if offset == 0 {
			return true
		}
		offset--
		return false
	})
	return after
}

// takes the cross product of two sets of multiaddrs
//
// assumes `a` is non-empty.
func cross(a, b []ma.Multiaddr) []ma.Multiaddr {
	res := make([]ma.Multiaddr, 0, len(a)*len(b))
	for _, x := range a {
		for _, y := range b {
			res = append(res, x.Encapsulate(y))
		}
	}
	return res
}
