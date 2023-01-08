package madns

import (
	"context"
	"net"
	"strings"

	"github.com/miekg/dns"
	ma "github.com/multiformats/go-multiaddr"
)

var ResolvableProtocols = []ma.Protocol{DnsaddrProtocol, Dns4Protocol, Dns6Protocol, DnsProtocol}
var DefaultResolver = &Resolver{def: net.DefaultResolver}

const dnsaddrTXTPrefix = "dnsaddr="

// BasicResolver is a low level interface for DNS resolution
type BasicResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
	LookupTXT(context.Context, string) ([]string, error)
}

// Resolver is an object capable of resolving dns multiaddrs by using one or more BasicResolvers;
// it supports custom per domain/TLD resolvers.
// It also implements the BasicResolver interface so that it can act as a custom per domain/TLD
// resolver.
type Resolver struct {
	def    BasicResolver
	custom map[string]BasicResolver
}

var _ BasicResolver = (*Resolver)(nil)

// NewResolver creates a new Resolver instance with the specified options
func NewResolver(opts ...Option) (*Resolver, error) {
	r := &Resolver{def: net.DefaultResolver}
	for _, opt := range opts {
		err := opt(r)
		if err != nil {
			return nil, err
		}
	}

	return r, nil
}

type Option func(*Resolver) error

// WithDefaultResolver is an option that specifies the default basic resolver,
// which resolves any TLD that doesn't have a custom resolver.
// Defaults to net.DefaultResolver
func WithDefaultResolver(def BasicResolver) Option {
	return func(r *Resolver) error {
		r.def = def
		return nil
	}
}

// WithDomainResolver specifies a custom resolver for a domain/TLD.
// Custom resolver selection matches domains left to right, with more specific resolvers
// superseding generic ones.
func WithDomainResolver(domain string, rslv BasicResolver) Option {
	return func(r *Resolver) error {
		if r.custom == nil {
			r.custom = make(map[string]BasicResolver)
		}
		fqdn := dns.Fqdn(domain)
		r.custom[fqdn] = rslv
		return nil
	}
}

func (r *Resolver) getResolver(domain string) BasicResolver {
	fqdn := dns.Fqdn(domain)

	// we match left-to-right, with more specific resolvers superseding generic ones.
	// So for a domain a.b.c, we will try a.b,c, b.c, c, and fallback to the default if
	// there is no match
	rslv, ok := r.custom[fqdn]
	if ok {
		return rslv
	}

	for i := strings.Index(fqdn, "."); i != -1; i = strings.Index(fqdn, ".") {
		fqdn = fqdn[i+1:]
		if fqdn == "" {
			// the . is the default resolver
			break
		}

		rslv, ok = r.custom[fqdn]
		if ok {
			return rslv
		}
	}

	return r.def
}

// Resolve resolves a DNS multiaddr.
func (r *Resolver) Resolve(ctx context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	var results []ma.Multiaddr
	for i := 0; maddr != nil; i++ {
		var keep ma.Multiaddr

		// Find the next dns component.
		keep, maddr = ma.SplitFunc(maddr, func(c ma.Component) bool {
			switch c.Protocol().Code {
			case DnsProtocol.Code, Dns4Protocol.Code, Dns6Protocol.Code, DnsaddrProtocol.Code:
				return true
			default:
				return false
			}
		})

		// Keep everything before the dns component.
		if keep != nil {
			if len(results) == 0 {
				results = []ma.Multiaddr{keep}
			} else {
				for i, r := range results {
					results[i] = r.Encapsulate(keep)
				}
			}
		}

		// If the rest is empty, we've hit the end (there _was_ no dns component).
		if maddr == nil {
			break
		}

		// split off the dns component.
		var resolve *ma.Component
		resolve, maddr = ma.SplitFirst(maddr)

		proto := resolve.Protocol()
		value := resolve.Value()
		rslv := r.getResolver(value)

		// resolve the dns component
		var resolved []ma.Multiaddr
		switch proto.Code {
		case Dns4Protocol.Code, Dns6Protocol.Code, DnsProtocol.Code:
			// The dns, dns4, and dns6 resolver simply resolves each
			// dns* component into an ipv4/ipv6 address.

			v4only := proto.Code == Dns4Protocol.Code
			v6only := proto.Code == Dns6Protocol.Code

			// XXX: Unfortunately, go does a pretty terrible job of
			// differentiating between IPv6 and IPv4. A v4-in-v6
			// AAAA record will _look_ like an A record to us and
			// there's nothing we can do about that.
			records, err := rslv.LookupIPAddr(ctx, value)
			if err != nil {
				return nil, err
			}

			// Convert each DNS record into a multiaddr. If the
			// protocol is dns4, throw away any IPv6 addresses. If
			// the protocol is dns6, throw away any IPv4 addresses.

			for _, r := range records {
				var (
					rmaddr ma.Multiaddr
					err    error
				)
				ip4 := r.IP.To4()
				if ip4 == nil {
					if v4only {
						continue
					}
					rmaddr, err = ma.NewMultiaddr("/ip6/" + r.IP.String())
				} else {
					if v6only {
						continue
					}
					rmaddr, err = ma.NewMultiaddr("/ip4/" + ip4.String())
				}
				if err != nil {
					return nil, err
				}
				resolved = append(resolved, rmaddr)
			}
		case DnsaddrProtocol.Code:
			// The dnsaddr resolver is a bit more complicated. We:
			//
			// 1. Lookup the dnsaddr txt record on _dnsaddr.DOMAIN.TLD
			// 2. Take everything _after_ the `/dnsaddr/DOMAIN.TLD`
			//    part of the multiaddr.
			// 3. Find the dnsaddr records (if any) with suffixes
			//    matching the result of step 2.

			// First, lookup the TXT record
			records, err := rslv.LookupTXT(ctx, "_dnsaddr."+value)
			if err != nil {
				return nil, err
			}

			// Then, calculate the length of the suffix we're
			// looking for.
			length := 0
			if maddr != nil {
				length = addrLen(maddr)
			}

			for _, r := range records {
				// Ignore non dnsaddr TXT records.
				if !strings.HasPrefix(r, dnsaddrTXTPrefix) {
					continue
				}

				// Extract and decode the multiaddr.
				rmaddr, err := ma.NewMultiaddr(r[len(dnsaddrTXTPrefix):])
				if err != nil {
					// discard multiaddrs we don't understand.
					// XXX: Is this right? It's the best we
					// can do for now, really.
					continue
				}

				// If we have a suffix to match on.
				if maddr != nil {
					// Make sure the new address is at least
					// as long as the suffix we're looking
					// for.
					rmlen := addrLen(rmaddr)
					if rmlen < length {
						// not long enough.
						continue
					}

					// Matches everything after the /dnsaddr/... with the end of the
					// dnsaddr record:
					//
					// v----------rmlen-----------------v
					// /ip4/1.2.3.4/tcp/1234/p2p/QmFoobar
					//                      /p2p/QmFoobar
					// ^--(rmlen - length)--^---length--^
					if !maddr.Equal(offset(rmaddr, rmlen-length)) {
						continue
					}
				}

				resolved = append(resolved, rmaddr)
			}

			// consumes the rest of the multiaddr as part of the "match" process.
			maddr = nil
		default:
			panic("unreachable")
		}

		if len(resolved) == 0 {
			return nil, nil
		} else if len(results) == 0 {
			results = resolved
		} else {
			// We take the cross product here as we don't have any
			// better way to represent "ORs" in multiaddrs. For
			// example, `/dns/foo.com/p2p-circuit/dns/bar.com` could
			// resolve to:
			//
			// * /ip4/1.1.1.1/p2p-circuit/ip4/2.1.1.1
			// * /ip4/1.1.1.1/p2p-circuit/ip4/2.1.1.2
			// * /ip4/1.1.1.2/p2p-circuit/ip4/2.1.1.1
			// * /ip4/1.1.1.2/p2p-circuit/ip4/2.1.1.2
			results = cross(results, resolved)
		}
	}

	return results, nil
}

func (r *Resolver) LookupIPAddr(ctx context.Context, domain string) ([]net.IPAddr, error) {
	return r.getResolver(domain).LookupIPAddr(ctx, domain)
}

func (r *Resolver) LookupTXT(ctx context.Context, txt string) ([]string, error) {
	return r.getResolver(txt).LookupTXT(ctx, txt)
}
