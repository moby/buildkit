package oci

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/libnetwork/resolvconf"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/pkg/errors"
)

var g flightcontrol.Group
var notFirstRun bool
var lastNotEmpty bool

// overridden by tests
var resolvconfGet = resolvconf.Get

type DNSConfig struct {
	Nameservers   []string
	Options       []string
	SearchDomains []string
}

func (dns *DNSConfig) Hash() string {
	hash := sha256.New()
	for _, x := range dns.Nameservers {
		fmt.Fprintf(hash, "nameserver %s\n", x)
	}
	for _, x := range dns.Options {
		fmt.Fprintf(hash, "options %s\n", x)
	}
	for _, x := range dns.SearchDomains {
		fmt.Fprintf(hash, "search %s\n", x)
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func GetResolvConf(ctx context.Context, stateDir string, idmap *idtools.IdentityMapping, dns *DNSConfig) (string, error) {
	p := filepath.Join(stateDir, "resolv.conf")
	if idmap != nil {
		// each file is chmod'd to match the container's root
		root := idmap.RootPair()
		p += "." + fmt.Sprintf("%d.%d", root.UID, root.GID)
	}
	if dns != nil {
		p += "." + dns.Hash()
	}

	_, err := g.Do(ctx, stateDir, func(ctx context.Context) (interface{}, error) {
		generate := !notFirstRun
		notFirstRun = true

		if !generate {
			fi, err := os.Stat(p)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return "", err
				}
				generate = true
			}
			if !generate {
				fiMain, err := os.Stat(resolvconf.Path())
				if err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						return nil, err
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
			return "", nil
		}

		var dt []byte
		f, err := resolvconfGet()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		} else {
			dt = f.Content
		}

		f, err = resolvconf.FilterResolvDNS(dt, true)
		if err != nil {
			return "", err
		}

		dt = f.Content

		tmpPath := p + ".tmp"
		if dns != nil {
			var (
				dnsNameservers   = resolvconf.GetNameservers(dt, resolvconf.IP)
				dnsSearchDomains = resolvconf.GetSearchDomains(dt)
				dnsOptions       = resolvconf.GetOptions(dt)
			)
			if len(dns.Nameservers) > 0 {
				dnsNameservers = dns.Nameservers
			}
			if len(dns.SearchDomains) > 0 {
				dnsSearchDomains = dns.SearchDomains
			}
			if len(dns.Options) > 0 {
				dnsOptions = dns.Options
			}

			f, err = resolvconf.Build(tmpPath, dnsNameservers, dnsSearchDomains, dnsOptions)
			if err != nil {
				return "", err
			}
			dt = f.Content
		}

		if err := os.WriteFile(tmpPath, dt, 0644); err != nil {
			return "", err
		}

		if idmap != nil {
			root := idmap.RootPair()
			if err := os.Chown(tmpPath, root.UID, root.GID); err != nil {
				return "", err
			}
		}

		if err := os.Rename(tmpPath, p); err != nil {
			return "", err
		}
		return "", nil
	})
	if err != nil {
		return "", err
	}
	return p, nil
}
