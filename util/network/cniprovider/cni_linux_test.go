//go:build linux

package cniprovider

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	netns "github.com/containernetworking/plugins/pkg/ns"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

func createTestNetNS(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	nsPath := filepath.Join(t.TempDir(), "netns")
	f, err := os.Create(nsPath)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	errCh := make(chan error)
	go func() {
		defer close(errCh)
		runtime.LockOSThread()
		errCh <- unshareAndMountNetNS(nsPath)
		// the thread stays locked so the go runtime terminates it
	}()
	require.NoError(t, <-errCh)
	t.Cleanup(func() {
		require.NoError(t, unmountNetNS(nsPath))
	})
	return nsPath
}

func acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

// Dialing a dual-stack name must not reach listeners in the host namespace.
// net.Dialer runs happy-eyeballs connect attempts on goroutines that are not
// pinned to the namespace, so a plain dialer inside WithNetNSPath escapes to
// the host namespace for any destination that resolves to both families.
func TestDialContextDoesNotEscapeNetNS(t *testing.T) {
	ns := &cniNS{nativeID: createTestNetNS(t)}

	ln4, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln4.Close()
	_, port, err := net.SplitHostPort(ln4.Addr().String())
	require.NoError(t, err)
	ln6, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp6", "[::1]:"+port)
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln6.Close()
	go acceptLoop(ln4)
	go acceptLoop(ln6)

	ctx, cancel := context.WithTimeoutCause(t.Context(), 3*time.Second, nil)
	defer cancel()
	conn, err := ns.DialContext(ctx, "tcp", "localhost:"+port)
	if conn != nil {
		conn.Close()
	}
	require.Error(t, err, "dial escaped the network namespace")
}

func TestDialContextDialsInsideNetNS(t *testing.T) {
	nsPath := createTestNetNS(t)

	var ln net.Listener
	require.NoError(t, netns.WithNetNSPath(nsPath, func(_ netns.NetNS) error {
		lo, err := netlink.LinkByName("lo")
		if err != nil {
			return err
		}
		if err := netlink.LinkSetUp(lo); err != nil {
			return err
		}
		ln, err = (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
		return err
	}))
	defer ln.Close()
	go acceptLoop(ln)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	ns := &cniNS{nativeID: nsPath}
	ctx, cancel := context.WithTimeoutCause(t.Context(), 3*time.Second, nil)
	defer cancel()
	// a hostname destination exercises resolution and the serial
	// IPv6->IPv4 fallback inside the namespace
	conn, err := ns.DialContext(ctx, "tcp", "localhost:"+port)
	require.NoError(t, err)
	conn.Close()
}

func TestIsLoopbackHost(t *testing.T) {
	require.True(t, isLoopbackHost("127.0.0.11:53"))
	require.True(t, isLoopbackHost("127.0.0.53:53"))
	require.True(t, isLoopbackHost("[::1]:53"))
	require.False(t, isLoopbackHost("8.8.8.8:53"))
	require.False(t, isLoopbackHost("[2001:4860:4860::8888]:53"))
	require.False(t, isLoopbackHost("example.com:53"))
}
