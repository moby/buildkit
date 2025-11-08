//go:build windows

package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	_ "github.com/moby/buildkit/solver/llbsolver/ops"
	_ "github.com/moby/buildkit/util/system/getuserinfo"
)

const socketScheme = "npipe://"

func listenFD(_ string, _ *tls.Config) (net.Listener, error) {
	return nil, errors.New("listening server on fd not supported on windows")
}

func getLocalListener(listenerPath, secDescriptor string) (net.Listener, error) {
	if secDescriptor == "" {
		// Allow generic read and generic write access to authenticated users
		// and system users. On Linux, this pipe seems to be given rw access to
		// user, group and others (666).
		// TODO(gabriel-samfira): should we restrict access to this pipe to just
		// authenticated users? Or Administrators group?
		secDescriptor = "D:P(A;;GRGW;;;AU)(A;;GRGW;;;SY)"
	}

	pc := &winio.PipeConfig{
		SecurityDescriptor: secDescriptor,
	}

	listener, err := winio.ListenPipe(listenerPath, pc)
	if err != nil {
		return nil, fmt.Errorf("creating listener"+": %w", err)
	}
	return listener, nil
}

func groupToSecurityDescriptor(group string) (string, error) {
	sddl := "D:P(A;;GA;;;BA)(A;;GA;;;SY)"
	if group != "" {
		for g := range strings.SplitSeq(group, ",") {
			sid, err := winio.LookupSidByName(g)
			if err != nil {
				return "", fmt.Errorf("failed to lookup sid for group %s: %w", g, err)
			}
			sddl += fmt.Sprintf("(A;;GRGW;;;%s)", sid)
		}
	}
	return sddl, nil
}
