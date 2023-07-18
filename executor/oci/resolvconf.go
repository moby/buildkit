package oci

import (
	"context"
	"os"
	"path/filepath"

	"github.com/docker/docker/libnetwork/resolvconf"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session/networks"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/pkg/errors"
)

var g flightcontrol.Group[struct{}]
var notFirstRun bool
var lastNotEmpty bool

// overridden by tests
var resolvconfPath = resolvconf.Path

func GetResolvConf(ctx context.Context, stateDir string, idmap *idtools.IdentityMapping, workerDNS, customDNS *networks.DNSConfig) (string, func(), error) {
	resolvPath := filepath.Join(stateDir, "resolv.conf")
	if customDNS != nil {
		resolvPath += "." + identity.NewID()
		err := makeResolvFile(resolvPath, stateDir, idmap, customDNS)
		if err != nil {
			return "", nil, err
		}

		return resolvPath, func() { os.Remove(resolvPath) }, nil
	}

	_, err := g.Do(ctx, stateDir, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, makeResolvFile(resolvPath, stateDir, idmap, workerDNS)
	})
	if err != nil {
		return "", nil, err
	}
	return resolvPath, func() {}, nil
}

func makeResolvFile(resolvPath, stateDir string, idmap *idtools.IdentityMapping, dns *networks.DNSConfig) error {
	generate := !notFirstRun
	notFirstRun = true

	if !generate {
		fi, err := os.Stat(resolvPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			generate = true
		}
		if !generate {
			fiMain, err := os.Stat(resolvconfPath())
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if lastNotEmpty {
					generate = true
					lastNotEmpty = false
				}
			} else {
				if fi.ModTime().Before(fiMain.ModTime()) {
					generate = true
				}
			}
		}
	}

	if !generate {
		return nil
	}

	dt, err := os.ReadFile(resolvconfPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	var f *resolvconf.File
	tmpPath := resolvPath + ".tmp"
	if dns != nil {
		var (
			dnsNameservers   = dns.Nameservers
			dnsSearchDomains = dns.SearchDomains
			dnsOptions       = dns.Options
		)
		if len(dns.Nameservers) == 0 {
			dnsNameservers = resolvconf.GetNameservers(dt, resolvconf.IP)
		}
		if len(dns.SearchDomains) == 0 {
			dnsSearchDomains = resolvconf.GetSearchDomains(dt)
		}
		if len(dns.Options) == 0 {
			dnsOptions = resolvconf.GetOptions(dt)
		}

		f, err = resolvconf.Build(tmpPath, dnsNameservers, dnsSearchDomains, dnsOptions)
		if err != nil {
			return err
		}
		dt = f.Content
	}

	f, err = resolvconf.FilterResolvDNS(dt, true)
	if err != nil {
		return err
	}

	if err := os.WriteFile(tmpPath, f.Content, 0644); err != nil {
		return err
	}

	if idmap != nil {
		root := idmap.RootPair()
		if err := os.Chown(tmpPath, root.UID, root.GID); err != nil {
			return err
		}
	}

	if err := os.Rename(tmpPath, resolvPath); err != nil {
		return err
	}

	return nil
}
