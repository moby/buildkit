package oci

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/identity"
)

const hostsContent = `
127.0.0.1	localhost
::1	localhost ip6-localhost ip6-loopback
`

func GetHostsFile(ctx context.Context, stateDir string, extraHosts []executor.HostIP) (string, error) {
	if len(extraHosts) == 0 {
		_, err := g.Do(ctx, stateDir, func(ctx context.Context) (interface{}, error) {
			_, err := makeHostsFile(stateDir, nil)
			return nil, err
		})
		if err != nil {
			return "", err
		}
		return filepath.Join(stateDir, "hosts"), nil
	}
	return makeHostsFile(stateDir, extraHosts)
}

func makeHostsFile(stateDir string, extraHosts []executor.HostIP) (string, error) {
	p := filepath.Join(stateDir, "hosts")
	if len(extraHosts) != 0 {
		p += "." + identity.NewID()
	}
	_, err := os.Stat(p)
	if err == nil {
		return "", nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	b := &bytes.Buffer{}

	if _, err := b.Write([]byte(hostsContent)); err != nil {
		return "", err
	}

	for _, h := range extraHosts {
		if _, err := b.Write([]byte(fmt.Sprintf("%s\t%s\n", h.IP.String(), h.Host))); err != nil {
			return "", err
		}
	}

	if err := ioutil.WriteFile(p+".tmp", b.Bytes(), 0644); err != nil {
		return "", err
	}

	if err := os.Rename(p+".tmp", p); err != nil {
		return "", err
	}
	return p, nil
}
