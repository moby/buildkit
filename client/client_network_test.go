package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/testutil/echoserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func testBridgeNetworking(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}
	if sb.Rootless() { // bridge is not used by default, even with detach-netns
		t.SkipNow()
	}
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	s, err := echoserver.NewTestServer("foo")
	require.NoError(t, err)
	addrParts := strings.Split(s.Addr().String(), ":")

	def, err := llb.Image("busybox").Run(llb.Shlexf("sh -c 'nc 127.0.0.1 %s | grep foo'", addrParts[len(addrParts)-1])).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
}

func testBridgeNetworkingDNSNoRootless(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCNINetwork)
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	name := identity.NewID()
	server, err := llb.Image("busybox").
		Run(
			llb.Shlexf(`sh -c 'test "$(nc -l -p 1234)" = "foo"'`),
			llb.Hostname(name),
		).
		Marshal(sb.Context())
	require.NoError(t, err)

	client, err := llb.Image("busybox").
		Run(
			llb.Shlexf("sh -c 'until echo foo | nc " + name + " 1234 -w0; do sleep 0.1; done'"),
		).
		Marshal(sb.Context())
	require.NoError(t, err)

	eg, ctx := errgroup.WithContext(context.Background())
	eg.Go(func() error {
		_, err := c.Solve(ctx, server, SolveOpt{}, nil)
		return err
	})
	eg.Go(func() error {
		_, err := c.Solve(ctx, client, SolveOpt{}, nil)
		return err
	})
	err = eg.Wait()
	require.NoError(t, err)
}

/*
testExtraHosts verifies that custom host entries added via llb.AddExtraHost() are resolvable
during a RUN step. It adds "myhost" pointing to 1.2.3.4 and checks /etc/hosts for the entry.

Skipped on Windows because BuildKit for Windows does not support llb.AddExtraHost()
at the moment due to fundamental differences in how Linux and Windows containers handle hosts file injections
*/
func testExtraHosts(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "extra hosts not supported on BuildKit for Windows at the moment")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c 'cat /etc/hosts | grep myhost | grep 1.2.3.4'`), llb.AddExtraHost("myhost", net.ParseIP("1.2.3.4")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testHostNetworking(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}
	netMode := sb.Value("netmode")
	var allowedEntitlements []string
	if netMode == hostNetwork {
		allowedEntitlements = []string{entitlements.EntitlementNetworkHost.String()}
	}
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	s, err := echoserver.NewTestServer("foo")
	require.NoError(t, err)
	addrParts := strings.Split(s.Addr().String(), ":")

	def, err := llb.Image("busybox").Run(llb.Shlexf("sh -c 'nc 127.0.0.1 %s | grep foo'", addrParts[len(addrParts)-1]), llb.Network(llb.NetModeHost)).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		AllowedEntitlements: allowedEntitlements,
	}, nil)
	if netMode == hostNetwork {
		require.NoError(t, err)
	} else {
		require.Error(t, err)
	}
}

func testHostnameLookup(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() { // bridge is not used by default, even with detach-netns
		t.SkipNow()
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	cmdStr := integration.UnixOrWindows(
		`sh -c "ping -c 1 $(hostname)"`,
		"cmd /C ping -n 1 %COMPUTERNAME%",
	)
	st := llb.Image(imgName).Run(llb.Shlex(cmdStr))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// moby/buildkit#1301
func testHostnameSpecifying(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() { // bridge is not used by default, even with detach-netns
		t.SkipNow()
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type testVars struct {
		imgName string
		cmdStr1 string
		cmdStr2 string
	}

	v := integration.UnixOrWindows(
		testVars{
			imgName: "busybox:latest",
			cmdStr1: "sh -c 'echo $HOSTNAME | grep %s'",
			cmdStr2: "sh -c 'echo $(hostname) | grep %s'",
		},
		testVars{
			imgName: "nanoserver:latest",
			// NOTE: Windows capitalizes the hostname hence
			// case insensitive findstr (findstr /I)
			// testtest --> TESTTEST
			cmdStr1: "cmd /C echo %%COMPUTERNAME%% | findstr /I %s",
			cmdStr2: "cmd /C echo %%COMPUTERNAME%% | findstr /I %s",
		},
	)

	hostname := "testtest"
	st := llb.Image(v.imgName).With(llb.Hostname(hostname)).
		Run(llb.Shlexf(v.cmdStr1, hostname)).
		Run(llb.Shlexf(v.cmdStr2, hostname))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{"hostname": hostname},
	}, nil)
	require.NoError(t, err)
}

func testNetworkMode(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c 'wget https://example.com 2>&1 | grep "wget: bad address"'`), llb.Network(llb.NetModeNone))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	st2 := llb.Image("busybox:latest").
		Run(llb.Shlex(`ifconfig`), llb.Network(llb.NetModeHost))

	def, err = st2.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		// Currently disabled globally by default
		// AllowedEntitlements: []entitlements.Entitlement{entitlements.EntitlementNetworkHost},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "network.host is not allowed")
}

func testProxyEnv(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	scratch := func() llb.State {
		return integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	}
	base := llb.Image(imgName).Dir("/out")
	cmd := integration.UnixOrWindows(
		`sh -c "echo -n $HTTP_PROXY-$HTTPS_PROXY-$NO_PROXY-$no_proxy-$ALL_PROXY-$all_proxy > env"`,
		`cmd /C echo %HTTP_PROXY%-%HTTPS_PROXY%-%NO_PROXY%-%no_proxy%-%ALL_PROXY%-%all_proxy% > env`,
	)

	st := base.Run(llb.Shlex(cmd), llb.WithProxy(llb.ProxyEnv{
		HTTPProxy:  "httpvalue",
		HTTPSProxy: "httpsvalue",
		NoProxy:    "noproxyvalue",
		AllProxy:   "allproxyvalue",
	}))

	out := st.AddMount("/out", scratch())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "env"))
	require.NoError(t, err)
	newLine := integration.UnixOrWindows("", " \r\n")
	require.Equal(t, "httpvalue-httpsvalue-noproxyvalue-noproxyvalue-allproxyvalue-allproxyvalue"+newLine, string(dt))

	// repeat to make sure proxy doesn't change cache
	st = base.Run(llb.Shlex(cmd), llb.WithProxy(llb.ProxyEnv{
		HTTPSProxy: "httpsvalue2",
		NoProxy:    "noproxyvalue2",
	}))
	out = st.AddMount("/out", scratch())

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "env"))
	require.NoError(t, err)
	require.Equal(t, "httpvalue-httpsvalue-noproxyvalue-noproxyvalue-allproxyvalue-allproxyvalue"+newLine, string(dt))
}

func testResolveAndHosts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "cp /etc/resolv.conf ."`)
	run(`sh -c "cp /etc/hosts ."`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "resolv.conf"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "nameserver")

	dt, err = os.ReadFile(filepath.Join(destDir, "hosts"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "127.0.0.1	localhost")
}

type netModeHost struct{}

func (*netModeHost) UpdateConfigFile(in string) (string, func() error) {
	return in + "\n\ninsecure-entitlements = [\"network.host\"]\n", nil
}

type netModeProxyBridge struct{}

func (*netModeProxyBridge) UpdateConfigFile(in string) (string, func() error) {
	return in + `

insecure-entitlements = ["network.host"]

[worker.oci]
networkMode = "bridge"

[worker.containerd]
networkMode = "bridge"
	`, nil
}

type netModeProxyDefault struct{}

func (*netModeProxyDefault) UpdateConfigFile(in string) (string, func() error) {
	return in + `

insecure-entitlements = ["network.host"]
`, nil
}

type netModeProxyDefaultNoCNI struct{}

func (*netModeProxyDefaultNoCNI) UpdateConfigFile(in string) (string, func() error) {
	return in + `

insecure-entitlements = ["network.host"]

[worker.oci]
cniConfigPath = "/tmp/buildkit-missing-cni.json"

[worker.containerd]
cniConfigPath = "/tmp/buildkit-missing-cni.json"
`, nil
}

type netModeProxyHost struct{}

func (*netModeProxyHost) UpdateConfigFile(in string) (string, func() error) {
	return in + `

insecure-entitlements = ["network.host"]

[worker.oci]
networkMode = "host"

[worker.containerd]
networkMode = "host"
`, nil
}

type netModeDefault struct{}

func (*netModeDefault) UpdateConfigFile(in string) (string, func() error) {
	return in, nil
}

type netModeBridgeDNS struct{}

func (*netModeBridgeDNS) UpdateConfigFile(in string) (string, func() error) {
	return in + `
# configure bridge networking
[worker.oci]
networkMode = "cni"
cniConfigPath = "/etc/buildkit/dns-cni.conflist"

[worker.containerd]
networkMode = "cni"
cniConfigPath = "/etc/buildkit/dns-cni.conflist"

[dns]
nameservers = ["10.11.0.1"]
`, nil
}

var (
	hostNetwork              integration.ConfigUpdater = &netModeHost{}
	defaultNetwork           integration.ConfigUpdater = &netModeDefault{}
	proxyDefaultNetwork      integration.ConfigUpdater = &netModeProxyDefault{}
	proxyDefaultNetworkNoCNI integration.ConfigUpdater = &netModeProxyDefaultNoCNI{}
	proxyBridgeNetwork       integration.ConfigUpdater = &netModeProxyBridge{}
	proxyHostNetwork         integration.ConfigUpdater = &netModeProxyHost{}
	bridgeDNSNetwork         integration.ConfigUpdater = &netModeBridgeDNS{}
)
