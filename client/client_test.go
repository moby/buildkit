package client

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	ctderrdefs "github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/echoserver"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sync/errgroup"
)

func init() {
	if integration.IsTestDockerd() {
		integration.InitDockerdWorker()
	} else {
		integration.InitOCIWorker()
		integration.InitContainerdWorker()
	}
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

func TestIntegration(t *testing.T) {
	mirroredImages := integration.OfficialImages("busybox:latest", "alpine:latest")
	mirroredImages["tonistiigi/test:nolayers"] = "docker.io/tonistiigi/test:nolayers"
	mirroredImages["cpuguy83/buildkit-foreign:latest"] = "docker.io/cpuguy83/buildkit-foreign:latest"
	mirrors := integration.WithMirroredImages(mirroredImages)

	tests := integration.TestFuncs(
		testCacheExportCacheKeyLoop,
		testRelativeWorkDir,
		testFileOpMkdirMkfile,
		testFileOpCopyRm,
		testFileOpCopyIncludeExclude,
		testFileOpRmWildcard,
		testCallDiskUsage,
		testBuildMultiMount,
		testBuildHTTPSource,
		testBuildPushAndValidate,
		testBuildExportWithUncompressed,
		testResolveAndHosts,
		testUser,
		testOCIExporter,
		testWhiteoutParentDir,
		testFrontendImageNaming,
		testDuplicateWhiteouts,
		testSchema1Image,
		testMountWithNoSource,
		testInvalidExporter,
		testReadonlyRootFS,
		testBasicRegistryCacheImportExport,
		testBasicLocalCacheImportExport,
		testCachedMounts,
		testCopyFromEmptyImage,
		testProxyEnv,
		testLocalSymlinkEscape,
		testTmpfsMounts,
		testSharedCacheMounts,
		testSharedCacheMountsNoScratch,
		testLockedCacheMounts,
		testDuplicateCacheMount,
		testRunCacheWithMounts,
		testParallelLocalBuilds,
		testSecretEnv,
		testSecretMounts,
		testExtraHosts,
		testShmSize,
		testUlimit,
		testCgroupParent,
		testNetworkMode,
		testFrontendMetadataReturn,
		testFrontendUseSolveResults,
		testSSHMount,
		testStdinClosed,
		testHostnameLookup,
		testHostnameSpecifying,
		testPushByDigest,
		testBasicInlineCacheImportExport,
		testExportBusyboxLocal,
		testBridgeNetworking,
		testCacheMountNoCache,
		testExporterTargetExists,
		testTarExporterWithSocket,
		testTarExporterWithSocketCopy,
		testTarExporterSymlink,
		testMultipleRegistryCacheImportExport,
		testSourceMap,
		testSourceMapFromRef,
		testLazyImagePush,
		testStargzLazyPull,
		testStargzLazyInlineCacheImportExport,
		testFileOpInputSwap,
		testRelativeMountpoint,
		testLocalSourceDiffer,
		testBuildExportZstd,
		testPullZstdImage,
		testMergeOp,
		testMergeOpCacheInline,
		testMergeOpCacheMin,
		testMergeOpCacheMax,
		testRmSymlink,
		testMoveParentDir,
		testBuildExportWithForeignLayer,
		testBuildInfoExporter,
		testBuildInfoInline,
		testBuildInfoNoExport,
		testZstdLocalCacheExport,
		testZstdRegistryCacheImportExport,
		testZstdLocalCacheImportExport,
		testUncompressedLocalCacheImportExport,
		testUncompressedRegistryCacheImportExport,
		testStargzLazyRegistryCacheImportExport,
		testCallInfo,
	)
	tests = append(tests, diffOpTestCases()...)
	integration.Run(t, tests, mirrors)

	integration.Run(t, integration.TestFuncs(
		testSecurityMode,
		testSecurityModeSysfs,
		testSecurityModeErrors,
	),
		mirrors,
		integration.WithMatrix("secmode", map[string]interface{}{
			"sandbox":  securitySandbox,
			"insecure": securityInsecure,
		}),
	)

	integration.Run(t, integration.TestFuncs(
		testHostNetworking,
	),
		mirrors,
		integration.WithMatrix("netmode", map[string]interface{}{
			"default": defaultNetwork,
			"host":    hostNetwork,
		}),
	)
}

func newContainerd(cdAddress string) (*containerd.Client, error) {
	return containerd.New(cdAddress, containerd.WithTimeout(60*time.Second))
}

// moby/buildkit#1336
func testCacheExportCacheKeyLoop(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tmpdir, err := os.MkdirTemp("", "buildkit-buildctl")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	err = os.WriteFile(filepath.Join(tmpdir, "foo"), []byte("foodata"), 0600)
	require.NoError(t, err)

	for _, mode := range []bool{false, true} {
		func(mode bool) {
			t.Run(fmt.Sprintf("mode=%v", mode), func(t *testing.T) {
				buildbase := llb.Image("alpine:latest").File(llb.Copy(llb.Local("mylocal"), "foo", "foo"))
				if mode { // same cache keys with a separating node go to different code-path
					buildbase = buildbase.Run(llb.Shlex("true")).Root()
				}
				intermed := llb.Image("alpine:latest").File(llb.Copy(buildbase, "foo", "foo"))
				final := llb.Scratch().File(llb.Copy(intermed, "foo", "foooooo"))

				def, err := final.Marshal(sb.Context())
				require.NoError(t, err)

				_, err = c.Solve(sb.Context(), def, SolveOpt{
					CacheExports: []CacheOptionsEntry{
						{
							Type: "local",
							Attrs: map[string]string{
								"dest": filepath.Join(tmpdir, "cache"),
							},
						},
					},
					LocalDirs: map[string]string{
						"mylocal": tmpdir,
					},
				}, nil)
				require.NoError(t, err)
			})
		}(mode)
	}
}

func testBridgeNetworking(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}
	if sb.Rootless() {
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
func testHostNetworking(t *testing.T, sb integration.Sandbox) {
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}
	netMode := sb.Value("netmode")
	var allowedEntitlements []entitlements.Entitlement
	if netMode == hostNetwork {
		allowedEntitlements = []entitlements.Entitlement{entitlements.EntitlementNetworkHost}
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

// #877
func testExportBusyboxLocal(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("busybox").Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	fi, err := os.Stat(filepath.Join(destDir, "bin/busybox"))
	require.NoError(t, err)

	fi2, err := os.Stat(filepath.Join(destDir, "bin/vi"))
	require.NoError(t, err)

	require.True(t, os.SameFile(fi, fi2))
}

func testHostnameLookup(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(llb.Shlex(`sh -c "ping -c 1 $(hostname)"`))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// moby/buildkit#1301
func testHostnameSpecifying(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	hostname := "testtest"
	st := llb.Image("busybox:latest").With(llb.Hostname(hostname)).
		Run(llb.Shlexf("sh -c 'echo $HOSTNAME | grep %s'", hostname)).
		Run(llb.Shlexf("sh -c 'echo $(hostname) | grep %s'", hostname))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{"hostname": hostname},
	}, nil)
	require.NoError(t, err)
}

// moby/buildkit#614
func testStdinClosed(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(llb.Shlex("cat"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testSSHMount(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	a := agent.NewKeyring()

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	err = a.Add(agent.AddedKey{PrivateKey: k})
	require.NoError(t, err)

	sockPath, clean, err := makeSSHAgentSock(a)
	require.NoError(t, err)
	defer clean()

	ssh, err := sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		Paths: []string{sockPath},
	}})
	require.NoError(t, err)

	// no ssh exposed
	st := llb.Image("busybox:latest").Run(llb.Shlex(`nosuchcmd`), llb.AddSSHSocket())
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no SSH key ")

	// custom ID not exposed
	st = llb.Image("busybox:latest").Run(llb.Shlex(`nosuchcmd`), llb.AddSSHSocket(llb.SSHID("customID")))
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{ssh},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unset ssh forward key customID")

	// missing custom ID ignored on optional
	st = llb.Image("busybox:latest").Run(llb.Shlex(`ls`), llb.AddSSHSocket(llb.SSHID("customID"), llb.SSHOptional))
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	// valid socket
	st = llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache openssh`)).
		Run(llb.Shlex(`sh -c 'echo -n $SSH_AUTH_SOCK > /out/sock && ssh-add -l > /out/out'`),
			llb.AddSSHSocket())

	out := st.AddMount("/out", llb.Scratch())
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "sock"))
	require.NoError(t, err)
	require.Equal(t, "/run/buildkit/ssh_agent.0", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "2048")
	require.Contains(t, string(dt), "(RSA)")

	// forbidden command
	st = llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache openssh`)).
		Run(llb.Shlex(`sh -c 'ssh-keygen -f /tmp/key -N "" && ssh-add -k /tmp/key 2> /out/out || true'`),
			llb.AddSSHSocket())

	out = st.AddMount("/out", llb.Scratch())
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "agent refused operation")

	// valid socket from key on disk
	st = llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache openssh`)).
		Run(llb.Shlex(`sh -c 'ssh-add -l > /out/out'`),
			llb.AddSSHSocket())

	out = st.AddMount("/out", llb.Scratch())
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	k, err = rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)

	dt = pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(k),
		},
	)

	tmpDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	err = os.WriteFile(filepath.Join(tmpDir, "key"), dt, 0600)
	require.NoError(t, err)

	ssh, err = sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		Paths: []string{filepath.Join(tmpDir, "key")},
	}})
	require.NoError(t, err)

	destDir, err = os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "1024")
	require.Contains(t, string(dt), "(RSA)")
}

func testExtraHosts(t *testing.T, sb integration.Sandbox) {
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

func testShmSize(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(
		llb.AddMount("/dev/shm", llb.Scratch(), llb.Tmpfs(llb.TmpfsSize(128*1024*1024))),
		llb.Shlex(`sh -c 'mount | grep /dev/shm > /out/out'`),
	)

	out := st.AddMount("/out", llb.Scratch())
	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), `size=131072k`)
}

func testUlimit(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c "ulimit -n > first"`, llb.AddUlimit(llb.UlimitNofile, 1062, 1062))
	run(`sh -c "ulimit -n > second"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.Equal(t, `1062`, strings.TrimSpace(string(dt)))

	dt2, err := os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.NotEqual(t, `1062`, strings.TrimSpace(string(dt2)))
}

func testCgroupParent(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	img := llb.Image("alpine:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = img.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c "cat /proc/self/cgroup > first"`, llb.WithCgroupParent("foocgroup"))
	run(`sh -c "cat /proc/self/cgroup > second"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `/foocgroup/buildkit/`)

	dt2, err := os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.NotContains(t, strings.TrimSpace(string(dt2)), `/foocgroup/buildkit/`)
}

func testNetworkMode(t *testing.T, sb integration.Sandbox) {
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

func testPushByDigest(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "direct push")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	st := llb.Scratch().File(llb.Mkfile("foo", 0600, []byte("data")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	name := registry + "/foo/bar"

	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name":           name,
					"push":           "true",
					"push-by-digest": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	_, _, err = contentutil.ProviderFromRef(name + ":latest")
	require.Error(t, err)

	desc, _, err := contentutil.ProviderFromRef(name + "@" + resp.ExporterResponse[exptypes.ExporterImageDigestKey])
	require.NoError(t, err)

	require.Equal(t, resp.ExporterResponse[exptypes.ExporterImageDigestKey], desc.Digest.String())
	require.Equal(t, images.MediaTypeDockerSchema2Manifest, desc.MediaType)
	require.True(t, desc.Size > 0)
}

func testSecurityMode(t *testing.T, sb integration.Sandbox) {
	command := `sh -c 'cat /proc/self/status | grep CapEff | cut -f 2 > /out'`
	mode := llb.SecurityModeSandbox
	var allowedEntitlements []entitlements.Entitlement
	var assertCaps func(caps uint64)
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		assertCaps = func(caps uint64) {
			/*
				$ capsh --decode=00000000a80425fb
				0x00000000a80425fb=cap_chown,cap_dac_override,cap_fowner,cap_fsetid,cap_kill,cap_setgid,cap_setuid,cap_setpcap,
				cap_net_bind_service,cap_net_raw,cap_sys_chroot,cap_mknod,cap_audit_write,cap_setfcap
			*/
			require.EqualValues(t, 0xa80425fb, caps)
		}
		allowedEntitlements = []entitlements.Entitlement{}
	} else {
		integration.SkipIfDockerd(t, sb)
		assertCaps = func(caps uint64) {
			/*
				$ capsh --decode=0000003fffffffff
				0x0000003fffffffff=cap_chown,cap_dac_override,cap_dac_read_search,cap_fowner,cap_fsetid,cap_kill,cap_setgid,
				cap_setuid,cap_setpcap,cap_linux_immutable,cap_net_bind_service,cap_net_broadcast,cap_net_admin,cap_net_raw,
				cap_ipc_lock,cap_ipc_owner,cap_sys_module,cap_sys_rawio,cap_sys_chroot,cap_sys_ptrace,cap_sys_pacct,cap_sys_admin,
				cap_sys_boot,cap_sys_nice,cap_sys_resource,cap_sys_time,cap_sys_tty_config,cap_mknod,cap_lease,cap_audit_write,
				cap_audit_control,cap_setfcap,cap_mac_override,cap_mac_admin,cap_syslog,cap_wake_alarm,cap_block_suspend,cap_audit_read
			*/

			// require that _at least_ minimum capabilities are granted
			require.EqualValues(t, 0x3fffffffff, caps&0x3fffffffff)
		}
		mode = llb.SecurityModeInsecure
		allowedEntitlements = []entitlements.Entitlement{entitlements.EntitlementSecurityInsecure}
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(command),
			llb.Security(mode))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		AllowedEntitlements: allowedEntitlements,
	}, nil)

	require.NoError(t, err)

	contents, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)

	caps, err := strconv.ParseUint(strings.TrimSpace(string(contents)), 16, 64)
	require.NoError(t, err)

	t.Logf("Caps: %x", caps)

	assertCaps(caps)
}

func testSecurityModeSysfs(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	mode := llb.SecurityModeSandbox
	var allowedEntitlements []entitlements.Entitlement
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		allowedEntitlements = []entitlements.Entitlement{}
	} else {
		integration.SkipIfDockerd(t, sb)
		mode = llb.SecurityModeInsecure
		allowedEntitlements = []entitlements.Entitlement{entitlements.EntitlementSecurityInsecure}
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	command := `mkdir /sys/fs/cgroup/cpuset/securitytest`
	st := llb.Image("busybox:latest").
		Run(llb.Shlex(command),
			llb.Security(mode))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		AllowedEntitlements: allowedEntitlements,
	}, nil)

	if secMode == securitySandbox {
		require.Error(t, err)
		require.Contains(t, err.Error(), "did not complete successfully")
		require.Contains(t, err.Error(), "mkdir /sys/fs/cgroup/cpuset/securitytest")
	} else {
		require.NoError(t, err)
	}
}

func testSecurityModeErrors(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		st := llb.Image("busybox:latest").
			Run(llb.Shlex(`sh -c 'echo sandbox'`))

		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{
			AllowedEntitlements: []entitlements.Entitlement{entitlements.EntitlementSecurityInsecure},
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "security.insecure is not allowed")
	}
	if secMode == securityInsecure {
		st := llb.Image("busybox:latest").
			Run(llb.Shlex(`sh -c 'echo insecure'`), llb.Security(llb.SecurityModeInsecure))

		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "security.insecure is not allowed")
	}
}

func testFrontendImageNaming(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci exporter", "direct push")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	checkImageName := map[string]func(out, imageName string, exporterResponse map[string]string){
		ExporterOCI: func(out, imageName string, exporterResponse map[string]string) {
			// Nothing to check
			return
		},
		ExporterDocker: func(out, imageName string, exporterResponse map[string]string) {
			require.Contains(t, exporterResponse, "image.name")
			require.Equal(t, exporterResponse["image.name"], "docker.io/library/"+imageName)

			dt, err := os.ReadFile(out)
			require.NoError(t, err)

			m, err := testutil.ReadTarToMap(dt, false)
			require.NoError(t, err)

			_, ok := m["oci-layout"]
			require.True(t, ok)

			var index ocispecs.Index
			err = json.Unmarshal(m["index.json"].Data, &index)
			require.NoError(t, err)
			require.Equal(t, 2, index.SchemaVersion)
			require.Equal(t, 1, len(index.Manifests))

			var dockerMfst []struct {
				RepoTags []string
			}
			err = json.Unmarshal(m["manifest.json"].Data, &dockerMfst)
			require.NoError(t, err)
			require.Equal(t, 1, len(dockerMfst))
			require.Equal(t, 1, len(dockerMfst[0].RepoTags))
			require.Equal(t, imageName, dockerMfst[0].RepoTags[0])
		},
		ExporterImage: func(_, imageName string, exporterResponse map[string]string) {
			require.Contains(t, exporterResponse, "image.name")
			require.Equal(t, exporterResponse["image.name"], imageName)

			// check if we can pull (requires containerd)
			cdAddress := sb.ContainerdAddress()
			if cdAddress == "" {
				return
			}

			// TODO: make public pull helper function so this can be checked for standalone as well

			client, err := containerd.New(cdAddress)
			require.NoError(t, err)
			defer client.Close()

			ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

			// check image in containerd
			_, err = client.ImageService().Get(ctx, imageName)
			require.NoError(t, err)

			// deleting image should release all content
			err = client.ImageService().Delete(ctx, imageName, images.SynchronousDelete())
			require.NoError(t, err)

			checkAllReleasable(t, c, sb, true)

			_, err = client.Pull(ctx, imageName)
			require.NoError(t, err)

			err = client.ImageService().Delete(ctx, imageName, images.SynchronousDelete())
			require.NoError(t, err)
		},
	}

	// A caller provided name takes precedence over one returned by the frontend. Iterate over both options.
	for _, winner := range []string{"frontend", "caller"} {
		winner := winner // capture loop variable.

		// The double layer of `t.Run` here is required so
		// that the inner-most tests (with the actual
		// functionality) have definitely completed before the
		// sandbox and registry cleanups (defered above) are run.
		t.Run(winner, func(t *testing.T) {
			for _, exp := range []string{ExporterOCI, ExporterDocker, ExporterImage} {
				exp := exp // capture loop variable.
				t.Run(exp, func(t *testing.T) {
					destDir, err := os.MkdirTemp("", "buildkit")
					require.NoError(t, err)
					defer os.RemoveAll(destDir)

					so := SolveOpt{
						Exports: []ExportEntry{
							{
								Type:  exp,
								Attrs: map[string]string{},
							},
						},
					}

					out := filepath.Join(destDir, "out.tar")

					imageName := "image-" + exp + "-fe:latest"

					switch exp {
					case ExporterOCI:
						t.Skip("oci exporter does not support named images")
					case ExporterDocker:
						outW, err := os.Create(out)
						require.NoError(t, err)
						so.Exports[0].Output = fixedWriteCloser(outW)
					case ExporterImage:
						imageName = registry + "/" + imageName
						so.Exports[0].Attrs["push"] = "true"
					}

					feName := imageName
					switch winner {
					case "caller":
						feName = "loser:latest"
						so.Exports[0].Attrs["name"] = imageName
					case "frontend":
						so.Exports[0].Attrs["name"] = "*"
					}

					frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
						res := gateway.NewResult()
						res.AddMeta("image.name", []byte(feName))
						return res, nil
					}

					resp, err := c.Build(sb.Context(), so, "", frontend, nil)
					require.NoError(t, err)

					checkImageName[exp](out, imageName, resp.ExporterResponse)
				})
			}
		})
	}

	checkAllReleasable(t, c, sb, true)
}

func testSecretMounts(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c 'mount | grep mysecret | grep "type tmpfs" && [ "$(cat /run/secrets/mysecret)" = 'foo-secret' ]'`), llb.AddSecret("/run/secrets/mysecret"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"/run/secrets/mysecret": []byte("foo-secret"),
		})},
	}, nil)
	require.NoError(t, err)

	// test optional
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`echo secret2`), llb.AddSecret("/run/secrets/mysecret2", llb.SecretOptional))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`echo secret3`), llb.AddSecret("/run/secrets/mysecret3"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.Error(t, err)

	// test id,perm,uid
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c '[ "$(stat -c "%u %g %f" /run/secrets/mysecret4)" = "1 1 81ff" ]' `), llb.AddSecret("/run/secrets/mysecret4", llb.SecretID("mysecret"), llb.SecretFileOpt(1, 1, 0777)))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte("pw"),
		})},
	}, nil)
	require.NoError(t, err)
}

func testSecretEnv(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c '[ "$(echo ${MY_SECRET})" = 'foo-secret' ]'`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true)))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"MY_SECRET": []byte("foo-secret"),
		})},
	}, nil)
	require.NoError(t, err)

	// test optional
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c '[ -z "${MY_SECRET}" ]'`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true), llb.SecretOptional))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`echo foo`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true)))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.Error(t, err)

	// test id
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c '[ "$(echo ${MYPASSWORD}-${MYTOKEN})" = "pw-token" ]' `),
			llb.AddSecret("MYPASSWORD", llb.SecretID("pass"), llb.SecretAsEnv(true)),
			llb.AddSecret("MYTOKEN", llb.SecretAsEnv(true)),
		)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"pass":    []byte("pw"),
			"MYTOKEN": []byte("token"),
		})},
	}, nil)
	require.NoError(t, err)
}

func testTmpfsMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c 'mount | grep /foobar | grep "type tmpfs" && touch /foobar/test'`), llb.AddMount("/foobar", llb.Scratch(), llb.Tmpfs()))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testLocalSymlinkEscape(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	test := []byte(`set -ex
[[ -L /mount/foo ]]
[[ -L /mount/sub/bar ]]
[[ -L /mount/bax ]]
[[ -f /mount/bay ]]
[[ -f /mount/sub/sub2/file ]]
[[ ! -f /mount/baz ]]
[[ ! -f /mount/etc/passwd ]]
[[ ! -f /mount/etc/group ]]
[[ $(readlink /mount/foo) == "/etc/passwd" ]]
[[ $(readlink /mount/sub/bar) == "../../../etc/group" ]]
`)

	dir, err := tmpdir(
		// point to absolute path that is not part of dir
		fstest.Symlink("/etc/passwd", "foo"),
		fstest.CreateDir("sub", 0700),
		// point outside of the dir
		fstest.Symlink("../../../etc/group", "sub/bar"),
		// regular valid symlink
		fstest.Symlink("bay", "bax"),
		// target for symlink (not requested)
		fstest.CreateFile("bay", []byte{}, 0600),
		// file with many subdirs
		fstest.CreateDir("sub/sub2", 0700),
		fstest.CreateFile("sub/sub2/file", []byte{}, 0600),
		// unused file that shouldn't be included
		fstest.CreateFile("baz", []byte{}, 0600),
		fstest.CreateFile("test.sh", test, 0700),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	local := llb.Local("mylocal", llb.FollowPaths([]string{
		"test.sh", "foo", "sub/bar", "bax", "sub/sub2/file",
	}))

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh /mount/test.sh`), llb.AddMount("/mount", local, llb.Readonly))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		LocalDirs: map[string]string{
			"mylocal": dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testRelativeWorkDir(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	pwd := llb.Image("docker.io/library/busybox:latest").
		Dir("test1").
		Dir("test2").
		Run(llb.Shlex(`sh -c "pwd > /out/pwd"`)).
		AddMount("/out", llb.Scratch())

	def, err := pwd.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "pwd"))
	require.NoError(t, err)
	require.Equal(t, []byte("/test1/test2\n"), dt)
}

func testFileOpMkdirMkfile(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().
		File(llb.Mkdir("/foo", 0700).Mkfile("bar", 0600, []byte("contents")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	fi, err := os.Stat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("contents"), dt)
}

func testFileOpCopyRm(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir, err := tmpdir(
		fstest.CreateFile("myfile", []byte("data0"), 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/foo", []byte("foo0"), 0600),
		fstest.CreateFile("sub/bar", []byte("bar0"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	dir2, err := tmpdir(
		fstest.CreateFile("file2", []byte("file2"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	st := llb.Scratch().
		File(
			llb.Copy(llb.Local("mylocal"), "myfile", "myfile2").
				Copy(llb.Local("mylocal"), "sub", "out").
				Rm("out/foo").
				Copy(llb.Local("mylocal2"), "file2", "/"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			"mylocal":  dir,
			"mylocal2": dir2,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "myfile2"))
	require.NoError(t, err)
	require.Equal(t, []byte("data0"), dt)

	fi, err := os.Stat(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())

	dt, err = os.ReadFile(filepath.Join(destDir, "out/bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("bar0"), dt)

	_, err = os.Stat(filepath.Join(destDir, "out/foo"))
	require.ErrorIs(t, err, os.ErrNotExist)

	dt, err = os.ReadFile(filepath.Join(destDir, "file2"))
	require.NoError(t, err)
	require.Equal(t, []byte("file2"), dt)
}

func testFileOpCopyIncludeExclude(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir, err := tmpdir(
		fstest.CreateFile("myfile", []byte("data0"), 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/foo", []byte("foo0"), 0600),
		fstest.CreateFile("sub/bar", []byte("bar0"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	st := llb.Scratch().File(
		llb.Copy(
			llb.Local("mylocal"), "/", "/", &llb.CopyInfo{
				IncludePatterns: []string{"sub/*"},
				ExcludePatterns: []string{"sub/bar"},
			},
		),
	)

	busybox := llb.Image("busybox:latest")
	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}
	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			"mylocal": dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "sub", "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("foo0"), dt)

	for _, name := range []string{"myfile", "sub/bar"} {
		_, err = os.Stat(filepath.Join(destDir, name))
		require.ErrorIs(t, err, os.ErrNotExist)
	}

	randBytes, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	// Create additional file which doesn't match the include pattern, and make
	// sure this doesn't invalidate the cache.

	err = fstest.Apply(fstest.CreateFile("unmatchedfile", []byte("data1"), 0600)).Apply(dir)
	require.NoError(t, err)

	st = llb.Scratch().File(
		llb.Copy(
			llb.Local("mylocal"), "/", "/", &llb.CopyInfo{
				IncludePatterns: []string{"sub/*"},
				ExcludePatterns: []string{"sub/bar"},
			},
		),
	)

	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err = os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			"mylocal": dir,
		},
	}, nil)
	require.NoError(t, err)

	randBytes2, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	require.Equal(t, randBytes, randBytes2)
}

// testFileOpInputSwap is a regression test that cache is invalidated when subset of fileop is built
func testFileOpInputSwap(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	base := llb.Scratch().File(llb.Mkfile("/foo", 0600, []byte("foo")))

	src := llb.Scratch().File(llb.Mkfile("/bar", 0600, []byte("bar")))

	st := base.File(llb.Copy(src, "/bar", "/baz"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// bar does not exist in base but index of all inputs remains the same
	st = base.File(llb.Copy(base, "/bar", "/baz"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bar: no such file")
}

func testLocalSourceDiffer(t *testing.T, sb integration.Sandbox) {
	for _, d := range []llb.DiffType{llb.DiffNone, llb.DiffMetadata} {
		t.Run(fmt.Sprintf("differ=%s", d), func(t *testing.T) {
			testLocalSourceWithDiffer(t, sb, d)
		})
	}
}

func testLocalSourceWithDiffer(t *testing.T, sb integration.Sandbox, d llb.DiffType) {
	requiresLinux(t)
	c, err := New(context.TODO(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir, err := tmpdir(
		fstest.CreateFile("foo", []byte("foo"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	tv := syscall.NsecToTimespec(time.Now().UnixNano())

	err = syscall.UtimesNano(filepath.Join(dir, "foo"), []syscall.Timespec{tv, tv})
	require.NoError(t, err)

	st := llb.Local("mylocal"+string(d), llb.Differ(d, false))

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			"mylocal" + string(d): dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), dt)

	err = os.WriteFile(filepath.Join(dir, "foo"), []byte("bar"), 0600)
	require.NoError(t, err)

	err = syscall.UtimesNano(filepath.Join(dir, "foo"), []syscall.Timespec{tv, tv})
	require.NoError(t, err)

	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			"mylocal" + string(d): dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	if d == llb.DiffMetadata {
		require.Equal(t, []byte("foo"), dt)
	}
	if d == llb.DiffNone {
		require.Equal(t, []byte("bar"), dt)
	}
}

func testFileOpRmWildcard(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir, err := tmpdir(
		fstest.CreateDir("foo", 0700),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("foo/target", []byte("foo0"), 0600),
		fstest.CreateFile("bar/target", []byte("bar0"), 0600),
		fstest.CreateFile("bar/remaining", []byte("bar1"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	st := llb.Scratch().File(
		llb.Copy(llb.Local("mylocal"), "foo", "foo").
			Copy(llb.Local("mylocal"), "bar", "bar"),
	).File(
		llb.Rm("*/target", llb.WithAllowWildcard(true)),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			"mylocal": dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "bar/remaining"))
	require.NoError(t, err)
	require.Equal(t, []byte("bar1"), dt)

	fi, err := os.Stat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())

	_, err = os.Stat(filepath.Join(destDir, "foo/target"))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = os.Stat(filepath.Join(destDir, "bar/target"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func testCallDiskUsage(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()
	_, err = c.DiskUsage(sb.Context())
	require.NoError(t, err)
}

func testBuildMultiMount(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	alpine := llb.Image("docker.io/library/alpine:latest")
	ls := alpine.Run(llb.Shlex("/bin/ls -l"))
	busybox := llb.Image("docker.io/library/busybox:latest")
	cp := ls.Run(llb.Shlex("/bin/cp -a /busybox/etc/passwd baz"))
	cp.AddMount("/busybox", busybox)

	def, err := cp.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testBuildHTTPSource(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid falso positive with current time

	resp := httpserver.Response{
		Etag:         identity.NewID(),
		Content:      []byte("content1"),
		LastModified: &modTime,
	}

	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	// invalid URL first
	st := llb.HTTP(server.URL + "/bar")

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response status 404")

	// first correct request
	st = llb.HTTP(server.URL + "/foo")

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	require.Equal(t, server.Stats("/foo").AllRequests, 1)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	tmpdir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, server.Stats("/foo").AllRequests, 2)
	require.Equal(t, server.Stats("/foo").CachedRequests, 1)

	dt, err := os.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	// test extra options
	st = llb.HTTP(server.URL+"/foo", llb.Filename("bar"), llb.Chmod(0741), llb.Chown(1000, 1000))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, server.Stats("/foo").AllRequests, 3)
	require.Equal(t, server.Stats("/foo").CachedRequests, 1)

	dt, err = os.ReadFile(filepath.Join(tmpdir, "bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	fi, err := os.Stat(filepath.Join(tmpdir, "bar"))
	require.NoError(t, err)
	require.Equal(t, fi.ModTime().Format(http.TimeFormat), modTime.Format(http.TimeFormat))
	require.Equal(t, int(fi.Mode()&0777), 0741)

	checkAllReleasable(t, c, sb, true)

	// TODO: check that second request was marked as cached
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

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

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

func testUser(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(llb.Shlex(`sh -c "mkdir -m 0777 /wd"`))

	run := func(user, cmd string) {
		if user != "" {
			st = st.Run(llb.Shlex(cmd), llb.Dir("/wd"), llb.User(user))
		} else {
			st = st.Run(llb.Shlex(cmd), llb.Dir("/wd"))
		}
	}

	run("daemon", `sh -c "id -nu > user"`)
	run("daemon:daemon", `sh -c "id -ng > group"`)
	run("daemon:nobody", `sh -c "id -ng > nobody"`)
	run("1:1", `sh -c "id -g > userone"`)
	run("root", `sh -c "id -Gn > root_supplementary"`)
	run("", `sh -c "id -Gn > default_supplementary"`)
	run("", `rm /etc/passwd /etc/group`) // test that default user still works
	run("", `sh -c "id -u > default_uid"`)

	st = st.Run(llb.Shlex("cp -a /wd/. /out/"))
	out := st.AddMount("/out", llb.Scratch())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "user"))
	require.NoError(t, err)
	require.Equal(t, "daemon", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "group"))
	require.NoError(t, err)
	require.Equal(t, "daemon", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "nobody"))
	require.NoError(t, err)
	require.Equal(t, "nobody", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "userone"))
	require.NoError(t, err)
	require.Equal(t, "1", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "root_supplementary"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(dt), "root "))
	require.True(t, strings.Contains(string(dt), "wheel"))

	dt2, err := os.ReadFile(filepath.Join(destDir, "default_supplementary"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))

	dt, err = os.ReadFile(filepath.Join(destDir, "default_uid"))
	require.NoError(t, err)
	require.Equal(t, "0", strings.TrimSpace(string(dt)))

	checkAllReleasable(t, c, sb, true)
}

func testOCIExporter(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci exporter")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n first > foo"`)
	run(`sh -c "echo -n second > bar"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	for _, exp := range []string{ExporterOCI, ExporterDocker} {
		destDir, err := os.MkdirTemp("", "buildkit")
		require.NoError(t, err)
		defer os.RemoveAll(destDir)

		out := filepath.Join(destDir, "out.tar")
		outW, err := os.Create(out)
		require.NoError(t, err)
		target := "example.com/buildkit/testoci:latest"
		attrs := map[string]string{}
		if exp == ExporterDocker {
			attrs["name"] = target
		}
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:   exp,
					Attrs:  attrs,
					Output: fixedWriteCloser(outW),
				},
			},
		}, nil)
		require.NoError(t, err)

		dt, err := os.ReadFile(out)
		require.NoError(t, err)

		m, err := testutil.ReadTarToMap(dt, false)
		require.NoError(t, err)

		_, ok := m["oci-layout"]
		require.True(t, ok)

		var index ocispecs.Index
		err = json.Unmarshal(m["index.json"].Data, &index)
		require.NoError(t, err)
		require.Equal(t, 2, index.SchemaVersion)
		require.Equal(t, 1, len(index.Manifests))

		var mfst ocispecs.Manifest
		err = json.Unmarshal(m["blobs/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
		require.NoError(t, err)
		require.Equal(t, 2, len(mfst.Layers))

		var ociimg ocispecs.Image
		err = json.Unmarshal(m["blobs/sha256/"+mfst.Config.Digest.Hex()].Data, &ociimg)
		require.NoError(t, err)
		require.Equal(t, "layers", ociimg.RootFS.Type)
		require.Equal(t, 2, len(ociimg.RootFS.DiffIDs))

		_, ok = m["blobs/sha256/"+mfst.Layers[0].Digest.Hex()]
		require.True(t, ok)
		_, ok = m["blobs/sha256/"+mfst.Layers[1].Digest.Hex()]
		require.True(t, ok)

		if exp != ExporterDocker {
			continue
		}

		var dockerMfst []struct {
			Config   string
			RepoTags []string
			Layers   []string
		}
		err = json.Unmarshal(m["manifest.json"].Data, &dockerMfst)
		require.NoError(t, err)
		require.Equal(t, 1, len(dockerMfst))

		_, ok = m[dockerMfst[0].Config]
		require.True(t, ok)
		require.Equal(t, 2, len(dockerMfst[0].Layers))
		require.Equal(t, 1, len(dockerMfst[0].RepoTags))
		require.Equal(t, target, dockerMfst[0].RepoTags[0])

		for _, l := range dockerMfst[0].Layers {
			_, ok := m[l]
			require.True(t, ok)
		}
	}

	checkAllReleasable(t, c, sb, true)
}

func testFrontendMetadataReturn(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		res.AddMeta("frontend.returned", []byte("true"))
		res.AddMeta("not-frontend.not-returned", []byte("false"))
		res.AddMeta("frontendnot.returned.either", []byte("false"))
		return res, nil
	}

	var exports []ExportEntry
	if integration.IsTestDockerd() {
		exports = []ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []ExportEntry{{
			Type:   ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := c.Build(sb.Context(), SolveOpt{
		Exports: exports,
	}, "", frontend, nil)
	require.NoError(t, err)
	require.Contains(t, res.ExporterResponse, "frontend.returned")
	require.Equal(t, res.ExporterResponse["frontend.returned"], "true")
	require.NotContains(t, res.ExporterResponse, "not-frontend.not-returned")
	require.NotContains(t, res.ExporterResponse, "frontendnot.returned.either")
	checkAllReleasable(t, c, sb, true)
}

func testFrontendUseSolveResults(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(
			llb.Mkfile("foo", 0600, []byte("data")),
		)

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}

		st2, err := ref.ToState()
		if err != nil {
			return nil, err
		}

		st = llb.Scratch().File(
			llb.Copy(st2, "foo", "foo2"),
		)

		def, err = st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo2"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("data"))
}

func testExporterTargetExists(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci exporter")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest")
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var mdDgst string
	res, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:  ExporterOCI,
				Attrs: map[string]string{},
				Output: func(m map[string]string) (io.WriteCloser, error) {
					mdDgst = m[exptypes.ExporterImageDigestKey]
					return nil, nil
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	dgst := res.ExporterResponse[exptypes.ExporterImageDigestKey]

	require.True(t, strings.HasPrefix(dgst, "sha256:"))
	require.Equal(t, dgst, mdDgst)

	require.True(t, strings.HasPrefix(res.ExporterResponse[exptypes.ExporterImageConfigDigestKey], "sha256:"))
}

func testTarExporterWithSocket(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	alpine := llb.Image("docker.io/library/alpine:latest")
	def, err := alpine.Run(llb.Args([]string{"sh", "-c", "nc -l -s local:/socket.sock & usleep 100000; kill %1"})).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:  ExporterTar,
				Attrs: map[string]string{},
				Output: func(m map[string]string) (io.WriteCloser, error) {
					return nopWriteCloser{io.Discard}, nil
				},
			},
		},
	}, nil)
	require.NoError(t, err)
}

func testTarExporterWithSocketCopy(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	alpine := llb.Image("docker.io/library/alpine:latest")
	state := alpine.Run(llb.Args([]string{"sh", "-c", "nc -l -s local:/root/socket.sock & usleep 100000; kill %1"})).Root()

	fa := llb.Copy(state, "/root", "/roo2", &llb.CopyInfo{})

	scratchCopy := llb.Scratch().File(fa)

	def, err := scratchCopy.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// moby/buildkit#1418
func testTarExporterSymlink(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n first > foo;ln -s foo bar"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(&nopWriteCloser{&buf}),
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	item, ok := m["foo"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("first"), item.Data)

	item, ok = m["bar"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeSymlink)
	require.Equal(t, "foo", item.Header.Linkname)
}

func testBuildExportWithForeignLayer(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "image exporter")

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("cpuguy83/buildkit-foreign:latest")
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	t.Run("propagate=1", func(t *testing.T) {
		registry, err := sb.NewRegistry()
		if errors.Is(err, integration.ErrRequirements) {
			t.Skip(err.Error())
		}
		require.NoError(t, err)

		target := registry + "/buildkit/build/exporter/foreign:latest"
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":                  target,
						"push":                  "true",
						"prefer-nondist-layers": "true",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

		resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})
		name, desc, err := resolver.Resolve(ctx, target)
		require.NoError(t, err)

		fetcher, err := resolver.Fetcher(ctx, name)
		require.NoError(t, err)
		mfst, err := images.Manifest(ctx, contentutil.FromFetcher(fetcher), desc, platforms.Any())
		require.NoError(t, err)

		require.Equal(t, 2, len(mfst.Layers))
		require.Equal(t, images.MediaTypeDockerSchema2LayerForeign, mfst.Layers[0].MediaType)
		require.Len(t, mfst.Layers[0].URLs, 1)
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[1].MediaType)

		rc, err := fetcher.Fetch(ctx, ocispecs.Descriptor{Digest: mfst.Layers[0].Digest, Size: mfst.Layers[0].Size})
		require.NoError(t, err)
		defer rc.Close()

		// `Fetch` doesn't error (in the docker resolver), it just returns a reader immediately and does not make a request.
		// The request is only made when we attempt to read from the reader.
		buf := make([]byte, 1)
		_, err = rc.Read(buf)
		require.Truef(t, ctderrdefs.IsNotFound(err), "expected error for blob that should not be in registry: %s, %v", mfst.Layers[0].Digest, err)
	})
	t.Run("propagate=0", func(t *testing.T) {
		registry, err := sb.NewRegistry()
		if errors.Is(err, integration.ErrRequirements) {
			t.Skip(err.Error())
		}
		require.NoError(t, err)
		target := registry + "/buildkit/build/exporter/noforeign:latest"
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name": target,
						"push": "true",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

		resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})
		name, desc, err := resolver.Resolve(ctx, target)
		require.NoError(t, err)

		fetcher, err := resolver.Fetcher(ctx, name)
		require.NoError(t, err)

		mfst, err := images.Manifest(ctx, contentutil.FromFetcher(fetcher), desc, platforms.Any())
		require.NoError(t, err)

		require.Equal(t, 2, len(mfst.Layers))
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[0].MediaType)
		require.Len(t, mfst.Layers[0].URLs, 0)
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[1].MediaType)

		rc, err := fetcher.Fetch(ctx, ocispecs.Descriptor{Digest: mfst.Layers[0].Digest, Size: mfst.Layers[0].Size})
		require.NoError(t, err)
		defer rc.Close()

		// `Fetch` doesn't error (in the docker resolver), it just returns a reader immediately and does not make a request.
		// The request is only made when we attempt to read from the reader.
		buf := make([]byte, 1)
		_, err = rc.Read(buf)
		require.NoError(t, err)
	})
}

func testBuildExportWithUncompressed(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "image exporter")

	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	cmd := `sh -e -c "echo -n uncompressed > data"`

	st := llb.Scratch()
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/build/exporter:withnocompressed"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":        target,
					"push":        "true",
					"compression": "uncompressed",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
	cdAddress := sb.ContainerdAddress()
	var client *containerd.Client
	if cdAddress != "" {
		client, err = newContainerd(cdAddress)
		require.NoError(t, err)
		defer client.Close()

		img, err := client.GetImage(ctx, target)
		require.NoError(t, err)
		mfst, err := images.Manifest(ctx, client.ContentStore(), img.Target(), nil)
		require.NoError(t, err)
		require.Equal(t, 1, len(mfst.Layers))
		require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[0].MediaType)
	}

	// new layer with gzip compression
	targetImg := llb.Image(target)
	cmd = `sh -e -c "echo -n gzip > data"`
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", targetImg)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	compressedTarget := registry + "/buildkit/build/exporter:withcompressed"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": compressedTarget,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	allCompressedTarget := registry + "/buildkit/build/exporter:withallcompressed"
	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              allCompressedTarget,
					"push":              "true",
					"compression":       "gzip",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	err = client.ImageService().Delete(ctx, target, images.SynchronousDelete())
	require.NoError(t, err)
	err = client.ImageService().Delete(ctx, compressedTarget, images.SynchronousDelete())
	require.NoError(t, err)
	err = client.ImageService().Delete(ctx, allCompressedTarget, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)

	// check if the new layer is compressed with compression option
	img, err := client.Pull(ctx, compressedTarget)
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, img.ContentStore(), img.Target())
	require.NoError(t, err)

	var mfst = struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispecs.Manifest
	}{}

	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)
	require.Equal(t, 2, len(mfst.Layers))
	require.Equal(t, images.MediaTypeDockerSchema2Layer, mfst.Layers[0].MediaType)
	require.Equal(t, images.MediaTypeDockerSchema2LayerGzip, mfst.Layers[1].MediaType)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[0].Digest})
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	item, ok := m["data"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("uncompressed"), item.Data)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[1].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["data"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("gzip"), item.Data)

	err = client.ImageService().Delete(ctx, compressedTarget, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)

	// check if all layers are compressed with force-compressoin option
	img, err = client.Pull(ctx, allCompressedTarget)
	require.NoError(t, err)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), img.Target())
	require.NoError(t, err)

	mfst = struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispecs.Manifest
	}{}

	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)
	require.Equal(t, 2, len(mfst.Layers))
	require.Equal(t, images.MediaTypeDockerSchema2LayerGzip, mfst.Layers[0].MediaType)
	require.Equal(t, images.MediaTypeDockerSchema2LayerGzip, mfst.Layers[1].MediaType)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[0].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["data"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("uncompressed"), item.Data)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[1].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["data"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("gzip"), item.Data)
}

func testBuildExportZstd(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci export")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	cmd := `sh -e -c "echo -n zstd > data"`

	st := llb.Scratch()
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
				Attrs: map[string]string{
					"compression": "zstd",
				},
			},
		},
		// compression option should work even with inline cache exports
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m["index.json"].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m["blobs/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]
	require.Equal(t, ocispecs.MediaTypeImageLayer+"+zstd", lastLayer.MediaType)

	zstdLayerDigest := lastLayer.Digest.Hex()
	require.Equal(t, m["blobs/sha256/"+zstdLayerDigest].Data[:4], []byte{0x28, 0xb5, 0x2f, 0xfd})

	// repeat without oci mediatype
	outW, err = os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
				Attrs: map[string]string{
					"compression":    "zstd",
					"oci-mediatypes": "false",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(out)
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	err = json.Unmarshal(m["index.json"].Data, &index)
	require.NoError(t, err)

	err = json.Unmarshal(m["blobs/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer = mfst.Layers[len(mfst.Layers)-1]
	require.Equal(t, images.MediaTypeDockerSchema2Layer+".zstd", lastLayer.MediaType)

	require.Equal(t, lastLayer.Digest.Hex(), zstdLayerDigest)
}

func testPullZstdImage(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "direct push")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	cmd := `sh -e -c "echo -n zstd > data"`

	st := llb.Scratch()
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/build/exporter:zstd"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":        target,
					"push":        "true",
					"compression": "zstd",

					// containerd applier supports only zstd with oci-mediatype.
					"oci-mediatypes": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	if sb.Name() == "containerd-1.4" {
		// containerd 1.4 doesn't support zstd compression
		return
	}

	ensurePruneAll(t, c, sb)

	st = llb.Scratch().File(llb.Copy(llb.Image(target), "/data", "/zdata"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "zdata"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("zstd"))
}
func testBuildPushAndValidate(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "direct push")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -e -c "mkdir -p foo/sub; echo -n first > foo/sub/bar; chmod 0741 foo;"`)
	run(`true`) // this doesn't create a layer
	run(`sh -c "echo -n second > foo/sub/baz"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testpush:latest"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// test existence of the image with next build
	firstBuild := llb.Image(target)

	def, err = firstBuild.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo/sub/bar"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("first"))

	dt, err = os.ReadFile(filepath.Join(destDir, "foo/sub/baz"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("second"))

	fi, err := os.Stat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, 0741, int(fi.Mode()&0777))

	checkAllReleasable(t, c, sb, false)

	// examine contents of exported tars (requires containerd)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	// TODO: make public pull helper function so this can be checked for standalone as well

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	// check image in containerd
	_, err = client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	// deleting image should release all content
	err = client.ImageService().Delete(ctx, target, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)

	img, err := client.Pull(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx)
	require.NoError(t, err)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.NotEqual(t, "", ociimg.OS)
	require.NotEqual(t, "", ociimg.Architecture)
	require.NotEqual(t, "", ociimg.Config.WorkingDir)
	require.Equal(t, "layers", ociimg.RootFS.Type)
	require.Equal(t, 3, len(ociimg.RootFS.DiffIDs))
	require.NotNil(t, ociimg.Created)
	require.True(t, time.Since(*ociimg.Created) < 2*time.Minute)
	require.Condition(t, func() bool {
		for _, env := range ociimg.Config.Env {
			if strings.HasPrefix(env, "PATH=") {
				return true
			}
		}
		return false
	})

	require.Equal(t, 3, len(ociimg.History))
	require.Contains(t, ociimg.History[0].CreatedBy, "foo/sub/bar")
	require.Contains(t, ociimg.History[1].CreatedBy, "true")
	require.Contains(t, ociimg.History[2].CreatedBy, "foo/sub/baz")
	require.False(t, ociimg.History[0].EmptyLayer)
	require.False(t, ociimg.History[1].EmptyLayer)
	require.False(t, ociimg.History[2].EmptyLayer)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), img.Target())
	require.NoError(t, err)

	var mfst = struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispecs.Manifest
	}{}

	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)

	require.Equal(t, images.MediaTypeDockerSchema2Manifest, mfst.MediaType)
	require.Equal(t, 3, len(mfst.Layers))
	require.Equal(t, images.MediaTypeDockerSchema2LayerGzip, mfst.Layers[0].MediaType)
	require.Equal(t, images.MediaTypeDockerSchema2LayerGzip, mfst.Layers[1].MediaType)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[0].Digest})
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok := m["foo/"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeDir)
	require.Equal(t, 0741, int(item.Header.Mode&0777))

	item, ok = m["foo/sub/"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeDir)

	item, ok = m["foo/sub/bar"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("first"), item.Data)

	_, ok = m["foo/sub/baz"]
	require.False(t, ok)

	dt, err = content.ReadBlob(ctx, img.ContentStore(), ocispecs.Descriptor{Digest: mfst.Layers[2].Digest})
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	item, ok = m["foo/sub/baz"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeReg)
	require.Equal(t, []byte("second"), item.Data)

	item, ok = m["foo/"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeDir)
	require.Equal(t, 0741, int(item.Header.Mode&0777))

	item, ok = m["foo/sub/"]
	require.True(t, ok)
	require.Equal(t, int32(item.Header.Typeflag), tar.TypeDir)

	_, ok = m["foo/sub/bar"]
	require.False(t, ok)
}

func testStargzLazyRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" || sb.Snapshotter() != "stargz" {
		t.Skip("test requires containerd worker with stargz snapshotter")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	// Prepare stargz registry cache
	orgImage := "docker.io/library/alpine:latest"
	sgzCache := registry + "/stargz/alpinecache:" + identity.NewID()
	baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/foo"}))
	def, err := baseDef.Marshal(sb.Context())
	require.NoError(t, err)
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref":               sgzCache,
					"compression":       "estargz",
					"oci-mediatypes":    "true",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// clear all local state out
	ensurePruneAll(t, c, sb)

	// stargz layers should be lazy even for executing something on them
	def, err = baseDef.
		Run(llb.Args([]string{"/bin/touch", "/bar"})).
		Marshal(sb.Context())
	require.NoError(t, err)
	target := registry + "/buildkit/testlazyimage:" + identity.NewID()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzCache,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	// Check if image layers are lazy.
	// The topmost(last) layer created by `Run` isn't lazy so we skip the check for the layer.
	var sgzLayers []ocispecs.Descriptor
	for i, layer := range manifest.Layers[:len(manifest.Layers)-1] {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v on layer %+v (%d)", err, layer, i)
		sgzLayers = append(sgzLayers, layer)
	}
	require.NotEqual(t, 0, len(sgzLayers), "no layer can be used for checking lazypull")

	// The topmost(last) layer created by `Run` shouldn't be lazy
	_, err = contentStore.Info(ctx, manifest.Layers[len(manifest.Layers)-1].Digest)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be exportable
	out = filepath.Join(destDir, "out2.tar")
	outW, err = os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzCache,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Check if image layers are un-lazied
	for _, layer := range sgzLayers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	ensurePruneAll(t, c, sb)
}

func testStargzLazyInlineCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" || sb.Snapshotter() != "stargz" {
		t.Skip("test requires containerd worker with stargz snapshotter")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Prepare stargz inline cache
	orgImage := "docker.io/library/alpine:latest"
	sgzImage := registry + "/stargz/alpine:" + identity.NewID()
	baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/foo"}))
	def, err := baseDef.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              sgzImage,
					"push":              "true",
					"compression":       "estargz",
					"oci-mediatypes":    "true",
					"force-compression": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, sgzImage, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be lazy even for executing something on them
	def, err = baseDef.
		Run(llb.Args([]string{"/bin/touch", "/bar"})).
		Marshal(sb.Context())
	require.NoError(t, err)
	target := registry + "/buildkit/testlazyimage:" + identity.NewID()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":           target,
					"push":           "true",
					"oci-mediatypes": "true",
					"compression":    "estargz",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzImage,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	// Check if image layers are lazy.
	// The topmost(last) layer created by `Run` isn't lazy so we skip the check for the layer.
	var sgzLayers []ocispecs.Descriptor
	for i, layer := range manifest.Layers[:len(manifest.Layers)-1] {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v on layer %+v (%d)", err, layer, i)
		sgzLayers = append(sgzLayers, layer)
	}
	require.NotEqual(t, 0, len(sgzLayers), "no layer can be used for checking lazypull")

	// The topmost(last) layer created by `Run` shouldn't be lazy
	_, err = contentStore.Info(ctx, manifest.Layers[len(manifest.Layers)-1].Digest)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be exportable
	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzImage,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Check if image layers are un-lazied
	for _, layer := range sgzLayers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	ensurePruneAll(t, c, sb)
}

func testStargzLazyPull(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" || sb.Snapshotter() != "stargz" {
		t.Skip("test requires containerd worker with stargz snapshotter")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Prepare stargz image
	orgImage := "docker.io/library/alpine:latest"
	sgzImage := registry + "/stargz/alpine:" + identity.NewID()
	def, err := llb.Image(orgImage).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              sgzImage,
					"push":              "true",
					"compression":       "estargz",
					"oci-mediatypes":    "true",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, sgzImage, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be lazy even for executing something on them
	def, err = llb.Image(sgzImage).
		Run(llb.Args([]string{"/bin/touch", "/foo"})).
		Marshal(sb.Context())
	require.NoError(t, err)
	target := registry + "/buildkit/testlazyimage:" + identity.NewID()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":           target,
					"push":           "true",
					"oci-mediatypes": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	// Check if image layers are lazy.
	// The topmost(last) layer created by `Run` isn't lazy so we skip the check for the layer.
	var sgzLayers []ocispecs.Descriptor
	for _, layer := range manifest.Layers[:len(manifest.Layers)-1] {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
		sgzLayers = append(sgzLayers, layer)
	}
	require.NotEqual(t, 0, len(sgzLayers), "no layer can be used for checking lazypull")

	// The topmost(last) layer created by `Run` shouldn't be lazy
	_, err = contentStore.Info(ctx, manifest.Layers[len(manifest.Layers)-1].Digest)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be exportable
	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	// Check if image layers are un-lazied
	for _, layer := range sgzLayers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	ensurePruneAll(t, c, sb)
}

func testLazyImagePush(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "direct push")
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// push the busybox image to the mutable registry
	sourceImage := "busybox:latest"
	def, err := llb.Image(sourceImage).Marshal(sb.Context())
	require.NoError(t, err)

	targetNoTag := registry + "/buildkit/testlazyimage:"
	target := targetNoTag + "latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	imageService := client.ImageService()
	contentStore := client.ContentStore()

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// retag the image we just pushed with no actual changes, which
	// should not result in the image getting un-lazied
	def, err = llb.Image(target).Marshal(sb.Context())
	require.NoError(t, err)

	target2 := targetNoTag + "newtag"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target2,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err = imageService.Get(ctx, target2)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// clear all local state out again
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// try a cross-repo push to same registry, which should still result in the
	// image remaining lazy
	target3 := registry + "/buildkit/testlazycrossrepo:latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target3,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err = imageService.Get(ctx, target3)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// check that a subsequent build can use the previously lazy image in an exec
	def, err = llb.Image(target2).Run(llb.Args([]string{"true"})).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testZstdLocalCacheExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	cmd := `sh -e -c "echo -n zstd > data"`

	st := llb.Scratch()
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	destOutDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destOutDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destOutDir,
			},
		},
		// compression option should work even with inline cache exports
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest":        destDir,
					"compression": "zstd",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	var index ocispecs.Index
	dt, err := os.ReadFile(filepath.Join(destDir, "index.json"))
	require.NoError(t, err)
	err = json.Unmarshal(dt, &index)
	require.NoError(t, err)

	var layerIndex ocispecs.Index
	dt, err = os.ReadFile(filepath.Join(destDir, "blobs/sha256/"+index.Manifests[0].Digest.Hex()))
	require.NoError(t, err)
	err = json.Unmarshal(dt, &layerIndex)
	require.NoError(t, err)

	lastLayer := layerIndex.Manifests[len(layerIndex.Manifests)-2]
	require.Equal(t, ocispecs.MediaTypeImageLayer+"+zstd", lastLayer.MediaType)

	zstdLayerDigest := lastLayer.Digest.Hex()
	dt, err = os.ReadFile(filepath.Join(destDir, "blobs/sha256/"+zstdLayerDigest))
	require.NoError(t, err)
	require.Equal(t, dt[:4], []byte{0x28, 0xb5, 0x2f, 0xfd})
}

func testUncompressedLocalCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	dir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	im := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"src": dir,
		},
	}
	ex := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"dest":              dir,
			"compression":       "uncompressed",
			"force-compression": "true",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testUncompressedRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	im := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	ex := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref":               target,
			"compression":       "uncompressed",
			"force-compression": "true",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testZstdLocalCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	if sb.Name() == "containerd-1.4" {
		// containerd 1.4 doesn't support zstd compression
		return
	}
	dir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	im := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"src": dir,
		},
	}
	ex := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"dest":              dir,
			"compression":       "zstd",
			"force-compression": "true",
			"oci-mediatypes":    "true", // containerd applier supports only zstd with oci-mediatype.
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testZstdRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	if sb.Name() == "containerd-1.4" {
		// containerd 1.4 doesn't support zstd compression
		return
	}
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	im := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	ex := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref":               target,
			"compression":       "zstd",
			"force-compression": "true",
			"oci-mediatypes":    "true", // containerd applier supports only zstd with oci-mediatype.
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testBasicCacheImportExport(t *testing.T, sb integration.Sandbox, cacheOptionsEntryImport, cacheOptionsEntryExport []CacheOptionsEntry) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n foobar > const"`)
	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheExports: cacheOptionsEntryExport,
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "foobar")

	dt, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	destDir, err = os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			}},
		CacheImports: cacheOptionsEntryImport,
	}, nil)
	require.NoError(t, err)

	dt2, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, string(dt2), "foobar")

	dt2, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))
}

func testBasicRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	o := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{o}, []CacheOptionsEntry{o})
}

func testMultipleRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	o := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	o2 := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target + "notexist",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{o, o2}, []CacheOptionsEntry{o})
}

func testBasicLocalCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "remote cache export")
	dir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	im := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"src": dir,
		},
	}
	ex := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"dest": dir,
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testBasicInlineCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "direct push")
	requiresLinux(t)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n foobar > const"`)
	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	target := registry + "/buildkit/testexportinline:latest"

	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, ok, true)

	unique, err := readFileInImage(sb.Context(), c, target+"@"+dgst, "/unique")
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		// specifying inline cache exporter is needed for reproducing containerimage.digest
		// (not needed for reproducing rootfs/unique)
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, ok, true)

	require.Equal(t, dgst, dgst2)

	ensurePruneAll(t, c, sb)

	// Export the cache again with compression
	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		// specifying inline cache exporter is needed for reproducing containerimage.digest
		// (not needed for reproducing rootfs/unique)
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              target,
					"push":              "true",
					"compression":       "uncompressed", // inline cache should work with compression
					"force-compression": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2uncompress, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, ok, true)

	// dgst2uncompress != dgst, because the compression type is different
	unique2uncompress, err := readFileInImage(sb.Context(), c, target+"@"+dgst2uncompress, "/unique")
	require.NoError(t, err)
	require.EqualValues(t, unique, unique2uncompress)

	ensurePruneAll(t, c, sb)

	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst3, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, ok, true)

	// dgst3 != dgst, because inline cache is not exported for dgst3
	unique3, err := readFileInImage(sb.Context(), c, target+"@"+dgst3, "/unique")
	require.NoError(t, err)
	require.EqualValues(t, unique, unique3)
}

func readFileInImage(ctx context.Context, c *Client, ref, path string) ([]byte, error) {
	def, err := llb.Image(ref).Marshal(ctx)
	if err != nil {
		return nil, err
	}
	destDir, err := os.MkdirTemp("", "buildkit")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(destDir)

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(destDir, filepath.Clean(path)))
}

func testCachedMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	// setup base for one of the cache sources
	st := busybox.Run(llb.Shlex(`sh -c "echo -n base > baz"`), llb.Dir("/wd"))
	base := st.AddMount("/wd", llb.Scratch())

	st = busybox.Run(llb.Shlex(`sh -c "echo -n first > foo"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))
	st = st.Run(llb.Shlex(`sh -c "cat foo && echo -n second > /wd2/bar"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))
	st.AddMount("/wd2", base, llb.AsPersistentCacheDir("mycache2", llb.CacheMountShared))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// repeat to make sure cache works
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// second build using cache directories
	st = busybox.Run(llb.Shlex(`sh -c "cp /src0/foo . && cp /src1/bar . && cp /src1/baz ."`), llb.Dir("/wd"))
	out := st.AddMount("/wd", llb.Scratch())
	st.AddMount("/src0", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))
	st.AddMount("/src1", base, llb.AsPersistentCacheDir("mycache2", llb.CacheMountShared))

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "first")

	dt, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "second")

	dt, err = os.ReadFile(filepath.Join(destDir, "baz"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "base")

	checkAllReleasable(t, c, sb, true)
}

func testSharedCacheMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.Run(llb.Shlex(`sh -e -c "touch one; while [[ ! -f two ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	st2 := busybox.Run(llb.Shlex(`sh -e -c "touch two; while [[ ! -f one ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st2.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	out := busybox.Run(llb.Shlex("true"))
	out.AddMount("/m1", st.Root())
	out.AddMount("/m2", st2.Root())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// #2334
func testSharedCacheMountsNoScratch(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.Run(llb.Shlex(`sh -e -c "touch one; while [[ ! -f two ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Image("busybox:latest"), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	st2 := busybox.Run(llb.Shlex(`sh -e -c "touch two; while [[ ! -f one ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st2.AddMount("/wd", llb.Image("busybox:latest"), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	out := busybox.Run(llb.Shlex("true"))
	out.AddMount("/m1", st.Root())
	out.AddMount("/m2", st2.Root())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testLockedCacheMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.Run(llb.Shlex(`sh -e -c "touch one; if [[ -f two ]]; then exit 0; fi; for i in $(seq 10); do if [[ -f two ]]; then exit 1; fi; usleep 200000; done"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	st2 := busybox.Run(llb.Shlex(`sh -e -c "touch two; if [[ -f one ]]; then exit 0; fi; for i in $(seq 10); do if [[ -f one ]]; then exit 1; fi; usleep 200000; done"`), llb.Dir("/wd"))
	st2.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	out := busybox.Run(llb.Shlex("true"))
	out.AddMount("/m1", st.Root())
	out.AddMount("/m2", st2.Root())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testDuplicateCacheMount(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")

	out := busybox.Run(llb.Shlex(`sh -e -c "[[ ! -f /m2/foo ]]; touch /m1/foo; [[ -f /m2/foo ]];"`))
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))
	out.AddMount("/m2", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testRunCacheWithMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")

	out := busybox.Run(llb.Shlex(`sh -e -c "[[ -f /m1/sbin/apk ]]"`))
	out.AddMount("/m1", llb.Image("alpine:latest"), llb.Readonly)

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	out = busybox.Run(llb.Shlex(`sh -e -c "[[ -f /m1/sbin/apk ]]"`))
	out.AddMount("/m1", llb.Image("busybox:latest"), llb.Readonly)

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
}

func testCacheMountNoCache(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")

	out := busybox.Run(llb.Shlex(`sh -e -c "touch /m1/foo; touch /m2/bar"`))
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))
	out.AddMount("/m2", llb.Scratch(), llb.AsPersistentCacheDir("mycache2", llb.CacheMountLocked))

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	out = busybox.Run(llb.Shlex(`sh -e -c "[[ ! -f /m1/foo ]]; touch /m1/foo2;"`), llb.IgnoreCache)
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	out = busybox.Run(llb.Shlex(`sh -e -c "[[ -f /m1/foo2 ]]; [[ -f /m2/bar ]];"`))
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))
	out.AddMount("/m2", llb.Scratch(), llb.AsPersistentCacheDir("mycache2", llb.CacheMountLocked))

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testCopyFromEmptyImage(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, image := range []llb.State{llb.Scratch(), llb.Image("tonistiigi/test:nolayers")} {
		st := llb.Scratch().File(llb.Copy(image, "/", "/"))
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.NoError(t, err)

		st = llb.Scratch().File(llb.Copy(image, "/foo", "/"))
		def, err = st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "/foo: no such file or directory")

		busybox := llb.Image("busybox:latest")

		out := busybox.Run(llb.Shlex(`sh -e -c '[ $(ls /scratch | wc -l) = '0' ]'`))
		out.AddMount("/scratch", image, llb.Readonly)

		def, err = out.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.NoError(t, err)
	}
}

// containerd/containerd#2119
func testDuplicateWhiteouts(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci exporter")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -e -c "mkdir -p d0 d1; echo -n first > d1/bar;"`)
	run(`sh -c "rm -rf d0 d1"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m["index.json"].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m["blobs/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]

	layer, ok := m["blobs/sha256/"+lastLayer.Digest.Hex()]
	require.True(t, ok)

	m, err = testutil.ReadTarToMap(layer.Data, true)
	require.NoError(t, err)

	_, ok = m[".wh.d0"]
	require.True(t, ok)

	_, ok = m[".wh.d1"]
	require.True(t, ok)

	// check for a bug that added whiteout for subfile
	_, ok = m["d1/.wh.bar"]
	require.True(t, !ok)
}

// #276
func testWhiteoutParentDir(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci exporter")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "mkdir -p foo; echo -n first > foo/bar;"`)
	run(`rm foo/bar`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m["index.json"].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m["blobs/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]

	layer, ok := m["blobs/sha256/"+lastLayer.Digest.Hex()]
	require.True(t, ok)

	m, err = testutil.ReadTarToMap(layer.Data, true)
	require.NoError(t, err)

	_, ok = m["foo/.wh.bar"]
	require.True(t, ok)

	_, ok = m["foo/"]
	require.True(t, ok)
}

// #2490
func testMoveParentDir(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "oci export")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "mkdir -p foo; echo -n first > foo/bar;"`)
	run(`mv foo foo2`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m["index.json"].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m["blobs/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]

	layer, ok := m["blobs/sha256/"+lastLayer.Digest.Hex()]
	require.True(t, ok)

	m, err = testutil.ReadTarToMap(layer.Data, true)
	require.NoError(t, err)

	_, ok = m[".wh.foo"]
	require.True(t, ok)

	_, ok = m["foo2/"]
	require.True(t, ok)

	_, ok = m["foo2/bar"]
	require.True(t, ok)
}

// #296
func testSchema1Image(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("gcr.io/google_containers/pause:3.0@sha256:0d093c962a6c2dd8bb8727b661e2b5f13e9df884af9945b4cc7088d9350cd3ee")

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

// #319
func testMountWithNoSource(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("docker.io/library/busybox:latest")
	st := llb.Scratch()

	var nilState llb.State

	// This should never actually be run, but we want to succeed
	// if it was, because we expect an error below, or a daemon
	// panic if the issue has regressed.
	run := busybox.Run(
		llb.Args([]string{"/bin/true"}),
		llb.AddMount("/nil", nilState, llb.SourcePath("/"), llb.Readonly))

	st = run.AddMount("/mnt", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

// #324
func testReadonlyRootFS(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("docker.io/library/busybox:latest")
	st := llb.Scratch()

	// The path /foo should be unwriteable.
	run := busybox.Run(
		llb.ReadonlyRootFS(),
		llb.Args([]string{"/bin/touch", "/foo"}))
	st = run.AddMount("/mnt", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	// Would prefer to detect more specifically "Read-only file
	// system" but that isn't exposed here (it is on the stdio
	// which we don't see).
	require.Contains(t, err.Error(), "process \"/bin/touch /foo\" did not complete successfully")

	checkAllReleasable(t, c, sb, true)
}

func testSourceMap(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	sm1 := llb.NewSourceMap(nil, "foo", []byte("data1"))
	sm2 := llb.NewSourceMap(nil, "bar", []byte("data2"))

	st := llb.Scratch().Run(
		llb.Shlex("not-exist"),
		sm1.Location([]*pb.Range{{Start: pb.Position{Line: 7}}}),
		sm2.Location([]*pb.Range{{Start: pb.Position{Line: 8}}}),
		sm1.Location([]*pb.Range{{Start: pb.Position{Line: 9}}}),
	)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)

	srcs := errdefs.Sources(err)
	require.Equal(t, 3, len(srcs))

	// Source errors are wrapped in the order provided as llb.ConstraintOpts, so
	// when they are unwrapped, the first unwrapped error is the last location
	// provided.
	require.Equal(t, "foo", srcs[0].Info.Filename)
	require.Equal(t, []byte("data1"), srcs[0].Info.Data)
	require.Nil(t, srcs[0].Info.Definition)

	require.Equal(t, 1, len(srcs[0].Ranges))
	require.Equal(t, int32(9), srcs[0].Ranges[0].Start.Line)
	require.Equal(t, int32(0), srcs[0].Ranges[0].Start.Character)

	require.Equal(t, "bar", srcs[1].Info.Filename)
	require.Equal(t, []byte("data2"), srcs[1].Info.Data)
	require.Nil(t, srcs[1].Info.Definition)

	require.Equal(t, 1, len(srcs[1].Ranges))
	require.Equal(t, int32(8), srcs[1].Ranges[0].Start.Line)
	require.Equal(t, int32(0), srcs[1].Ranges[0].Start.Character)

	require.Equal(t, "foo", srcs[2].Info.Filename)
	require.Equal(t, []byte("data1"), srcs[2].Info.Data)
	require.Nil(t, srcs[2].Info.Definition)

	require.Equal(t, 1, len(srcs[2].Ranges))
	require.Equal(t, int32(7), srcs[2].Ranges[0].Start.Line)
	require.Equal(t, int32(0), srcs[2].Ranges[0].Start.Character)
}

func testSourceMapFromRef(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	srcState := llb.Scratch().File(
		llb.Mkfile("foo", 0600, []byte("data")))
	sm := llb.NewSourceMap(&srcState, "bar", []byte("bardata"))

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(
			llb.Mkdir("foo/bar", 0600), //fails because /foo doesn't exist
			sm.Location([]*pb.Range{{Start: pb.Position{Line: 3, Character: 1}}}),
		)

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}

		st2, err := ref.ToState()
		if err != nil {
			return nil, err
		}

		st = llb.Scratch().File(
			llb.Copy(st2, "foo", "foo2"),
		)

		def, err = st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	_, err = c.Build(sb.Context(), SolveOpt{}, "", frontend, nil)
	require.Error(t, err)

	srcs := errdefs.Sources(err)
	require.Equal(t, 1, len(srcs))

	require.Equal(t, "bar", srcs[0].Info.Filename)
	require.Equal(t, []byte("bardata"), srcs[0].Info.Data)
	require.NotNil(t, srcs[0].Info.Definition)

	require.Equal(t, 1, len(srcs[0].Ranges))
	require.Equal(t, int32(3), srcs[0].Ranges[0].Start.Line)
	require.Equal(t, int32(1), srcs[0].Ranges[0].Start.Character)
}

func testRmSymlink(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Test that if FileOp.Rm is called on a symlink, then
	// the symlink is removed rather than the target
	mnt := llb.Image("alpine").
		Run(llb.Shlex("touch /mnt/target")).
		AddMount("/mnt", llb.Scratch())

	mnt = llb.Image("alpine").
		Run(llb.Shlex("ln -s target /mnt/link")).
		AddMount("/mnt", mnt)

	def, err := mnt.File(llb.Rm("link")).Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, fstest.CheckDirectoryEqualWithApplier(destDir, fstest.CreateFile("target", nil, 0644)))
}

func testProxyEnv(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	base := llb.Image("docker.io/library/busybox:latest").Dir("/out")
	cmd := `sh -c "echo -n $HTTP_PROXY-$HTTPS_PROXY-$NO_PROXY-$no_proxy-$ALL_PROXY-$all_proxy > env"`

	st := base.Run(llb.Shlex(cmd), llb.WithProxy(llb.ProxyEnv{
		HTTPProxy:  "httpvalue",
		HTTPSProxy: "httpsvalue",
		NoProxy:    "noproxyvalue",
		AllProxy:   "allproxyvalue",
	}))
	out := st.AddMount("/out", llb.Scratch())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

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
	require.Equal(t, string(dt), "httpvalue-httpsvalue-noproxyvalue-noproxyvalue-allproxyvalue-allproxyvalue")

	// repeat to make sure proxy doesn't change cache
	st = base.Run(llb.Shlex(cmd), llb.WithProxy(llb.ProxyEnv{
		HTTPSProxy: "httpsvalue2",
		NoProxy:    "noproxyvalue2",
	}))
	out = st.AddMount("/out", llb.Scratch())

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err = os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

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
	require.Equal(t, string(dt), "httpvalue-httpsvalue-noproxyvalue-noproxyvalue-allproxyvalue-allproxyvalue")
}

func testMergeOp(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	ctx := sb.Context()
	registry, err := sb.NewRegistry()
	if !errors.Is(err, integration.ErrRequirements) {
		require.NoError(t, err)
	}

	var imageTarget string
	if integration.IsTestDockerd() {
		// do image export but use a fake url as the image should just end up in moby's
		// local store
		imageTarget = "fake.invalid:33333/buildkit/testmergeop:latest"
	} else if registry != "" {
		imageTarget = registry + "/buildkit/testmergeop:latest"
	}

	stateA := llb.Scratch().
		File(llb.Mkfile("/foo", 0755, []byte("A"))).
		File(llb.Mkfile("/a", 0755, []byte("A"))).
		File(llb.Mkdir("/bar", 0700)).
		File(llb.Mkfile("/bar/A", 0755, []byte("A")))
	stateB := stateA.
		File(llb.Rm("/foo")).
		File(llb.Mkfile("/b", 0755, []byte("B"))).
		File(llb.Mkfile("/bar/B", 0754, []byte("B")))
	stateC := llb.Scratch().
		File(llb.Mkfile("/foo", 0755, []byte("C"))).
		File(llb.Mkfile("/c", 0755, []byte("C"))).
		File(llb.Mkdir("/bar", 0755)).
		File(llb.Mkfile("/bar/A", 0400, []byte("C")))

	mergeA := llb.Merge([]llb.State{stateA, stateC})
	requireContents(ctx, t, c, sb, mergeA, nil, nil, imageTarget,
		fstest.CreateFile("foo", []byte("C"), 0755),
		fstest.CreateFile("c", []byte("C"), 0755),
		fstest.CreateDir("bar", 0755),
		fstest.CreateFile("bar/A", []byte("C"), 0400),
		fstest.CreateFile("a", []byte("A"), 0755),
	)

	mergeB := llb.Merge([]llb.State{stateC, stateB})
	requireContents(ctx, t, c, sb, mergeB, nil, nil, imageTarget,
		fstest.CreateFile("a", []byte("A"), 0755),
		fstest.CreateFile("b", []byte("B"), 0755),
		fstest.CreateFile("c", []byte("C"), 0755),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("bar/A", []byte("A"), 0755),
		fstest.CreateFile("bar/B", []byte("B"), 0754),
	)

	stateD := llb.Scratch().File(llb.Mkdir("/qaz", 0755))
	mergeC := llb.Merge([]llb.State{mergeA, mergeB, stateD})
	requireContents(ctx, t, c, sb, mergeC, nil, nil, imageTarget,
		fstest.CreateFile("a", []byte("A"), 0755),
		fstest.CreateFile("b", []byte("B"), 0755),
		fstest.CreateFile("c", []byte("C"), 0755),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("bar/A", []byte("A"), 0755),
		fstest.CreateFile("bar/B", []byte("B"), 0754),
		fstest.CreateDir("qaz", 0755),
	)

	runA := runShell(llb.Merge([]llb.State{llb.Image("alpine"), mergeC}),
		// turn /a file into a dir, mv b and c into it
		"rm /a",
		"mkdir /a",
		"mv /b /c /a/",
		// remove+recreate /bar to make it opaque on overlay snapshotters
		"rm -rf /bar",
		"mkdir -m 0755 /bar",
		"echo -n D > /bar/D",
		// turn /qaz dir into a file
		"rm -rf /qaz",
		"touch /qaz",
	)
	stateE := llb.Scratch().
		File(llb.Mkfile("/foo", 0755, []byte("E"))).
		File(llb.Mkdir("/bar", 0755)).
		File(llb.Mkfile("/bar/A", 0755, []byte("A"))).
		File(llb.Mkfile("/bar/E", 0755, nil))
	mergeD := llb.Merge([]llb.State{stateE, runA})
	requireEqualContents(ctx, t, c, mergeD, llb.Image("alpine").
		File(llb.Mkdir("a", 0755)).
		File(llb.Mkfile("a/b", 0755, []byte("B"))).
		File(llb.Mkfile("a/c", 0755, []byte("C"))).
		File(llb.Mkdir("bar", 0755)).
		File(llb.Mkfile("bar/D", 0644, []byte("D"))).
		File(llb.Mkfile("bar/E", 0755, nil)).
		File(llb.Mkfile("qaz", 0644, nil)),
	// /foo from stateE is not here because it is deleted in stateB, which is part of a submerge of mergeD
	)
}

func testMergeOpCacheInline(t *testing.T, sb integration.Sandbox) {
	testMergeOpCache(t, sb, "inline")
}

func testMergeOpCacheMin(t *testing.T, sb integration.Sandbox) {
	testMergeOpCache(t, sb, "min")
}

func testMergeOpCacheMax(t *testing.T, sb integration.Sandbox) {
	testMergeOpCache(t, sb, "max")
}

func testMergeOpCache(t *testing.T, sb integration.Sandbox, mode string) {
	t.Helper()
	integration.SkipIfDockerd(t, sb, "direct push")
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// push the busybox image to the mutable registry
	sourceImage := "busybox:latest"
	def, err := llb.Image(sourceImage).Marshal(sb.Context())
	require.NoError(t, err)

	busyboxTargetNoTag := registry + "/buildkit/testlazyimage:"
	busyboxTarget := busyboxTargetNoTag + "latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": busyboxTarget,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	imageService := client.ImageService()
	contentStore := client.ContentStore()

	busyboxImg, err := imageService.Get(ctx, busyboxTarget)
	require.NoError(t, err)

	busyboxManifest, err := images.Manifest(ctx, contentStore, busyboxImg.Target, nil)
	require.NoError(t, err)

	for _, layer := range busyboxManifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	// clear all local state out
	err = imageService.Delete(ctx, busyboxImg.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range busyboxManifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// make a new merge that includes the lazy busybox as a base and exports inline cache
	input1 := llb.Scratch().
		File(llb.Mkdir("/dir", 0777)).
		File(llb.Mkfile("/dir/1", 0777, nil))
	input1Copy := llb.Scratch().File(llb.Copy(input1, "/dir/1", "/foo/1", &llb.CopyInfo{CreateDestPath: true}))

	// put random contents in the file to ensure it's not re-run later
	input2 := runShell(llb.Image("alpine:latest"),
		"mkdir /dir",
		"cat /dev/urandom | head -c 100 | sha256sum > /dir/2")
	input2Copy := llb.Scratch().File(llb.Copy(input2, "/dir/2", "/bar/2", &llb.CopyInfo{CreateDestPath: true}))

	merge := llb.Merge([]llb.State{llb.Image(busyboxTarget), input1Copy, input2Copy})

	def, err = merge.Marshal(sb.Context())
	require.NoError(t, err)

	target := registry + "/buildkit/testmerge:latest"
	cacheTarget := registry + "/buildkit/testmergecache:latest"

	var cacheExports []CacheOptionsEntry
	var cacheImports []CacheOptionsEntry
	switch mode {
	case "inline":
		cacheExports = []CacheOptionsEntry{{
			Type: "inline",
		}}
		cacheImports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": target,
			},
		}}
	case "min":
		cacheExports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": cacheTarget,
			},
		}}
		cacheImports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": cacheTarget,
			},
		}}
	case "max":
		cacheExports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref":  cacheTarget,
				"mode": "max",
			},
		}}
		cacheImports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": cacheTarget,
			},
		}}
	default:
		require.Fail(t, "unknown cache mode: %s", mode)
	}

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	// verify that the busybox image stayed lazy
	for _, layer := range busyboxManifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// get the random value at /bar/2
	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	bar2Contents, err := os.ReadFile(filepath.Join(destDir, "bar", "2"))
	require.NoError(t, err)

	// clear all local state out
	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// re-run the same build with cache imports and verify everything stays lazy
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name": target,
				"push": "true",
			},
		}},
		CacheImports: cacheImports,
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	// verify everything from before stayed lazy
	img, err = imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for i, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v for index %d", err, i)
	}

	// re-run the build with a change only to input1 using the remote cache
	input1 = llb.Scratch().
		File(llb.Mkdir("/dir", 0777)).
		File(llb.Mkfile("/dir/1", 0444, nil))
	input1Copy = llb.Scratch().File(llb.Copy(input1, "/dir/1", "/foo/1", &llb.CopyInfo{CreateDestPath: true}))

	merge = llb.Merge([]llb.State{llb.Image(busyboxTarget), input1Copy, input2Copy})

	def, err = merge.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name": target,
				"push": "true",
			},
		}},
		CacheExports: cacheExports,
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	// verify everything from before stayed lazy except the middle layer for input1Copy
	img, err = imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for i, layer := range manifest.Layers {
		switch i {
		case 0, 2:
			// bottom and top layer should stay lazy as they didn't change
			_, err = contentStore.Info(ctx, layer.Digest)
			require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v for index %d", err, i)
		case 1:
			// middle layer had to be rebuilt, should exist locally
			_, err = contentStore.Info(ctx, layer.Digest)
			require.NoError(t, err)
		default:
			require.Fail(t, "unexpected layer index %d", i)
		}
	}

	// check the random value at /bar/2 didn't change
	destDir, err = os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	newBar2Contents, err := os.ReadFile(filepath.Join(destDir, "bar", "2"))
	require.NoError(t, err)

	require.Equalf(t, bar2Contents, newBar2Contents, "bar/2 contents changed")

	// Now test the case with a layer on top of a merge.
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	mergePlusLayer := merge.File(llb.Mkfile("/3", 0444, nil))

	def, err = mergePlusLayer.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name": target,
				"push": "true",
			},
		}},
		CacheExports: cacheExports,
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	// check the random value at /bar/2 didn't change
	destDir, err = os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	newBar2Contents, err = os.ReadFile(filepath.Join(destDir, "bar", "2"))
	require.NoError(t, err)

	require.Equalf(t, bar2Contents, newBar2Contents, "bar/2 contents changed")

	// clear local state, repeat the build, verify everything stays lazy
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v", err)
	}

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name": target,
				"push": "true",
			},
		}},
		CacheImports: cacheImports,
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	img, err = imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for i, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, ctderrdefs.ErrNotFound, "unexpected error %v for index %d", err, i)
	}
}

func requireContents(ctx context.Context, t *testing.T, c *Client, sb integration.Sandbox, state llb.State, cacheImports, cacheExports []CacheOptionsEntry, imageTarget string, files ...fstest.Applier) {
	t.Helper()

	def, err := state.Marshal(ctx)
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheImports,
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	require.NoError(t, fstest.CheckDirectoryEqualWithApplier(destDir, fstest.Apply(files...)))

	if imageTarget != "" {
		var exports []ExportEntry
		if integration.IsTestDockerd() {
			exports = []ExportEntry{{
				Type: "moby",
				Attrs: map[string]string{
					"name": imageTarget,
				},
			}}
		} else {
			exports = []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": imageTarget,
					"push": "true",
				},
			}}
		}

		_, err = c.Solve(ctx, def, SolveOpt{Exports: exports, CacheImports: cacheImports, CacheExports: cacheExports}, nil)
		require.NoError(t, err)
		resetState(t, c, sb)
		requireContents(ctx, t, c, sb, llb.Image(imageTarget, llb.ResolveModePreferLocal), cacheImports, nil, "", files...)
	}
}

func requireEqualContents(ctx context.Context, t *testing.T, c *Client, stateA, stateB llb.State) {
	t.Helper()

	defA, err := stateA.Marshal(ctx)
	require.NoError(t, err)

	destDirA, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDirA)

	_, err = c.Solve(ctx, defA, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDirA,
			},
		},
	}, nil)
	require.NoError(t, err)

	defB, err := stateB.Marshal(ctx)
	require.NoError(t, err)

	destDirB, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDirB)

	_, err = c.Solve(ctx, defB, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDirB,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, fstest.CheckDirectoryEqual(destDirA, destDirB))
}

func runShellExecState(base llb.State, cmds ...string) llb.ExecState {
	return base.Run(llb.Args([]string{"sh", "-c", strings.Join(cmds, " && ")}))
}

func runShell(base llb.State, cmds ...string) llb.State {
	return runShellExecState(base, cmds...).Root()
}

func chainRunShells(base llb.State, cmdss ...[]string) llb.State {
	for _, cmds := range cmdss {
		base = runShell(base, cmds...)
	}
	return base
}

func requiresLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
}

// ensurePruneAll tries to ensure Prune completes with retries.
// Current cache implementation defers release-related logic using goroutine so
// there can be situation where a build has finished but the following prune doesn't
// cleanup cache because some records still haven't been released.
// This function tries to ensure prune by retrying it.
func ensurePruneAll(t *testing.T, c *Client, sb integration.Sandbox) {
	for i := 0; i < 2; i++ {
		require.NoError(t, c.Prune(sb.Context(), nil, PruneAll))
		for j := 0; j < 20; j++ {
			du, err := c.DiskUsage(sb.Context())
			require.NoError(t, err)
			if len(du) == 0 {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Logf("retrying prune(%d)", i)
	}
	t.Fatalf("failed to ensure prune")
}

func checkAllReleasable(t *testing.T, c *Client, sb integration.Sandbox, checkContent bool) {
	retries := 0
loop0:
	for {
		require.True(t, 20 > retries)
		retries++
		du, err := c.DiskUsage(sb.Context())
		require.NoError(t, err)
		for _, d := range du {
			if d.InUse {
				time.Sleep(500 * time.Millisecond)
				continue loop0
			}
		}
		break
	}

	err := c.Prune(sb.Context(), nil, PruneAll)
	require.NoError(t, err)

	du, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)
	require.Equal(t, 0, len(du))

	// examine contents of exported tars (requires containerd)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Logf("checkAllReleasable: skipping check for exported tars in non-containerd test")
		return
	}

	// TODO: make public pull helper function so this can be checked for standalone as well

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
	snapshotService := client.SnapshotService("overlayfs")

	retries = 0
	for {
		count := 0
		err = snapshotService.Walk(ctx, func(context.Context, snapshots.Info) error {
			count++
			return nil
		})
		require.NoError(t, err)
		if count == 0 {
			break
		}
		require.True(t, 20 > retries)
		retries++
		time.Sleep(500 * time.Millisecond)
	}

	if !checkContent {
		return
	}

	retries = 0
	for {
		count := 0
		var infos []content.Info
		err = client.ContentStore().Walk(ctx, func(info content.Info) error {
			count++
			infos = append(infos, info)
			return nil
		})
		require.NoError(t, err)
		if count == 0 {
			break
		}
		if retries >= 20 {
			require.FailNowf(t, "content still exists", "%+v", infos)
		}
		retries++
		time.Sleep(500 * time.Millisecond)
	}
}

func testInvalidExporter(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("busybox:latest").Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	target := "example.com/buildkit/testoci:latest"
	attrs := map[string]string{
		"name": target,
	}
	for _, exp := range []string{ExporterOCI, ExporterDocker} {
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:  exp,
					Attrs: attrs,
				},
			},
		}, nil)
		// output file writer is required
		require.Error(t, err)
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:      exp,
					Attrs:     attrs,
					OutputDir: destDir,
				},
			},
		}, nil)
		// output directory is not supported
		require.Error(t, err)
	}

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:  ExporterLocal,
				Attrs: attrs,
			},
		},
	}, nil)
	// output directory is required
	require.Error(t, err)

	f, err := os.Create(filepath.Join(destDir, "a"))
	require.NoError(t, err)
	defer f.Close()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterLocal,
				Attrs:  attrs,
				Output: fixedWriteCloser(f),
			},
		},
	}, nil)
	// output file writer is not supported
	require.Error(t, err)

	checkAllReleasable(t, c, sb, true)
}

// moby/buildkit#492
func testParallelLocalBuilds(t *testing.T, sb integration.Sandbox) {
	ctx, cancel := context.WithCancel(sb.Context())
	defer cancel()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	eg, ctx := errgroup.WithContext(ctx)

	for i := 0; i < 3; i++ {
		func(i int) {
			eg.Go(func() error {
				fn := fmt.Sprintf("test%d", i)
				srcDir, err := tmpdir(
					fstest.CreateFile(fn, []byte("contents"), 0600),
				)
				require.NoError(t, err)
				defer os.RemoveAll(srcDir)

				def, err := llb.Local("source").Marshal(sb.Context())
				require.NoError(t, err)

				destDir, err := os.MkdirTemp("", "buildkit")
				require.NoError(t, err)
				defer os.RemoveAll(destDir)

				_, err = c.Solve(ctx, def, SolveOpt{
					Exports: []ExportEntry{
						{
							Type:      ExporterLocal,
							OutputDir: destDir,
						},
					},
					LocalDirs: map[string]string{
						"source": srcDir,
					},
				}, nil)
				require.NoError(t, err)

				act, err := os.ReadFile(filepath.Join(destDir, fn))
				require.NoError(t, err)

				require.Equal(t, "contents", string(act))
				return nil
			})
		}(i)
	}

	err = eg.Wait()
	require.NoError(t, err)
}

// testRelativeMountpoint is a test that relative paths for mountpoints don't
// fail when runc is upgraded to at least rc95, which introduces an error when
// mountpoints are not absolute. Relative paths should be transformed to
// absolute points based on the llb.State's current working directory.
func testRelativeMountpoint(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	id := identity.NewID()

	st := llb.Image("busybox:latest").Dir("/root").Run(
		llb.Shlexf("sh -c 'echo -n %s > /root/relpath/data'", id),
	).AddMount("relpath", llb.Scratch())

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "data"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte(id))
}

// moby/buildkit#2476
func testBuildInfoExporter(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Image("busybox:latest").Run(
			llb.Args([]string{"/bin/sh", "-c", `echo hello`}),
		)
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return c.Solve(ctx, gateway.SolveRequest{
			Definition:  def.ToPB(),
			FrontendOpt: map[string]string{"build-arg:foo": "bar"},
		})
	}

	var exports []ExportEntry
	if integration.IsTestDockerd() {
		exports = []ExportEntry{{
			Type: "moby",
			Attrs: map[string]string{
				"name": "reg.dummy:5000/buildkit/test:latest",
			},
		}}
	} else {
		exports = []ExportEntry{{
			Type:   ExporterOCI,
			Attrs:  map[string]string{},
			Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
		}}
	}

	res, err := c.Build(sb.Context(), SolveOpt{
		Exports: exports,
	}, "", frontend, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	decbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var exbi binfotypes.BuildInfo
	err = json.Unmarshal(decbi, &exbi)
	require.NoError(t, err)

	attrval := "bar"
	require.Equal(t, exbi.Attrs, map[string]*string{"build-arg:foo": &attrval})
	require.Equal(t, len(exbi.Sources), 1)
	require.Equal(t, exbi.Sources[0].Type, binfotypes.SourceTypeDockerImage)
	require.Equal(t, exbi.Sources[0].Ref, "docker.io/library/busybox:latest")
}

// moby/buildkit#2476
func testBuildInfoInline(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "direct push")
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(
		llb.Args([]string{"/bin/sh", "-c", `echo hello`}),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	for _, tt := range []struct {
		name       string
		buildAttrs bool
	}{{
		"attrsEnabled",
		true,
	}, {
		"attrsDisabled",
		false,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			target := registry + "/buildkit/test-buildinfo:latest"

			_, err = c.Solve(sb.Context(), def, SolveOpt{
				Exports: []ExportEntry{
					{
						Type: ExporterImage,
						Attrs: map[string]string{
							"name":            target,
							"push":            "true",
							"buildinfo-attrs": strconv.FormatBool(tt.buildAttrs),
						},
					},
				},
				FrontendAttrs: map[string]string{
					"build-arg:foo": "bar",
				},
			}, nil)
			require.NoError(t, err)

			img, err := client.GetImage(ctx, target)
			require.NoError(t, err)

			desc, err := img.Config(ctx)
			require.NoError(t, err)

			dt, err := content.ReadBlob(ctx, img.ContentStore(), desc)
			require.NoError(t, err)

			var config binfotypes.ImageConfig
			require.NoError(t, json.Unmarshal(dt, &config))

			dec, err := base64.StdEncoding.DecodeString(config.BuildInfo)
			require.NoError(t, err)

			var bi binfotypes.BuildInfo
			require.NoError(t, json.Unmarshal(dec, &bi))

			if tt.buildAttrs {
				attrval := "bar"
				require.Contains(t, bi.Attrs, "build-arg:foo")
				require.Equal(t, bi.Attrs["build-arg:foo"], &attrval)
			} else {
				require.NotContains(t, bi.Attrs, "build-arg:foo")
			}
			require.Equal(t, len(bi.Sources), 1)
			require.Equal(t, bi.Sources[0].Type, binfotypes.SourceTypeDockerImage)
			require.Equal(t, bi.Sources[0].Ref, "docker.io/library/busybox:latest")
		})
	}
}

func testBuildInfoNoExport(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Image("busybox:latest").Run(
			llb.Args([]string{"/bin/sh", "-c", `echo hello`}),
		)
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return c.Solve(ctx, gateway.SolveRequest{
			Definition:  def.ToPB(),
			FrontendOpt: map[string]string{"build-arg:foo": "bar"},
		})
	}

	res, err := c.Build(sb.Context(), SolveOpt{}, "", frontend, nil)
	require.NoError(t, err)

	require.Contains(t, res.ExporterResponse, exptypes.ExporterBuildInfo)
	decbi, err := base64.StdEncoding.DecodeString(res.ExporterResponse[exptypes.ExporterBuildInfo])
	require.NoError(t, err)

	var exbi binfotypes.BuildInfo
	err = json.Unmarshal(decbi, &exbi)
	require.NoError(t, err)

	attrval := "bar"
	require.Equal(t, exbi.Attrs, map[string]*string{"build-arg:foo": &attrval})
	require.Equal(t, len(exbi.Sources), 1)
	require.Equal(t, exbi.Sources[0].Type, binfotypes.SourceTypeDockerImage)
	require.Equal(t, exbi.Sources[0].Ref, "docker.io/library/busybox:latest")
}

func testCallInfo(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Info(sb.Context())
	require.NoError(t, err)
}

func tmpdir(appliers ...fstest.Applier) (string, error) {
	tmpdir, err := os.MkdirTemp("", "buildkit-client")
	if err != nil {
		return "", err
	}
	if err := fstest.Apply(appliers...).Apply(tmpdir); err != nil {
		return "", err
	}
	return tmpdir, nil
}

func makeSSHAgentSock(agent agent.Agent) (p string, cleanup func() error, err error) {
	tmpDir, err := os.MkdirTemp("", "buildkit")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmpDir)
		}
	}()

	sockPath := filepath.Join(tmpDir, "ssh_auth_sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return "", nil, err
	}

	s := &server{l: l}
	go s.run(agent)

	return sockPath, func() error {
		l.Close()
		return os.RemoveAll(tmpDir)
	}, nil
}

type server struct {
	l net.Listener
}

func (s *server) run(a agent.Agent) error {
	for {
		c, err := s.l.Accept()
		if err != nil {
			return err
		}

		go agent.ServeAgent(a, c)
	}
}

type secModeSandbox struct{}

func (*secModeSandbox) UpdateConfigFile(in string) string {
	return in
}

type secModeInsecure struct{}

func (*secModeInsecure) UpdateConfigFile(in string) string {
	return in + "\n\ninsecure-entitlements = [\"security.insecure\"]\n"
}

var securitySandbox integration.ConfigUpdater = &secModeSandbox{}
var securityInsecure integration.ConfigUpdater = &secModeInsecure{}

type netModeHost struct{}

func (*netModeHost) UpdateConfigFile(in string) string {
	return in + "\n\ninsecure-entitlements = [\"network.host\"]\n"
}

type netModeDefault struct{}

func (*netModeDefault) UpdateConfigFile(in string) string {
	return in
}

var hostNetwork integration.ConfigUpdater = &netModeHost{}
var defaultNetwork integration.ConfigUpdater = &netModeDefault{}

func fixedWriteCloser(wc io.WriteCloser) func(map[string]string) (io.WriteCloser, error) {
	return func(map[string]string) (io.WriteCloser, error) {
		return wc, nil
	}
}
