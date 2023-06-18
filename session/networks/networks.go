package networks

import (
	context "context"
	strings "strings"

	"github.com/moby/buildkit/session"
)

func (config *Config) ExtraHosts() string {
	out := ""
	for _, ipHost := range config.IpHosts {
		out += ipHost.Ip + " " + strings.Join(ipHost.Hosts, " ") + "\n"
	}
	return out
}

func MergeConfig(ctx context.Context, c session.Caller, base *Config, id string) (*Config, error) {
	nw, err := NewNetworksClient(c.Conn()).GetNetwork(ctx, &GetNetworkRequest{ID: id})
	if err != nil {
		return nil, err
	}

	if nw.Config == nil {
		return base, nil
	}

	cp := *base

	cp.IpHosts = append(nw.Config.IpHosts, cp.IpHosts...)

	dns := nw.Config.Dns
	if dns != nil {
		var dnsCp DNSConfig
		if cp.Dns != nil {
			dnsCp = *cp.Dns
		}

		cp.Dns = &dnsCp

		// override individual fields
		// TODO append? should match behavior with git source

		if len(dns.Nameservers) > 0 {
			cp.Dns.Nameservers = dns.Nameservers
		}
		if len(dns.Options) > 0 {
			cp.Dns.Options = dns.Options
		}
		if len(dns.SearchDomains) > 0 {
			cp.Dns.SearchDomains = dns.SearchDomains
		}
	}

	return &cp, nil
}
