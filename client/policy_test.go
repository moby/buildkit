package client

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"hash"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/platforms"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	solvererrdefs "github.com/moby/buildkit/solver/errdefs"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	opspb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	sourcepolicypb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/pgpsign"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	policyimage "github.com/moby/policy-helpers/image"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testProxyNetworkNoRootless(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	payload := []byte("buildkit proxy ok\n")
	convertedPayload := []byte("buildkit proxy converted ok\n")
	httpSrv, httpURL := newProxyHTTPServer(t, "0.0.0.0", testHostIP(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/allowed":
			_, _ = w.Write(payload)
		case "/bar":
			_, _ = w.Write(convertedPayload)
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer httpSrv.Close()
	var leakHit atomic.Int32
	leakSrv, leakURL := newProxyHTTPServer(t, "0.0.0.0", testHostIP(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakHit.Add(1)
		_, _ = w.Write([]byte("host namespace leak\n"))
	}))
	defer leakSrv.Close()
	_, leakPort, err := net.SplitHostPort(strings.TrimPrefix(leakURL, "http://"))
	require.NoError(t, err)

	st := llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c 'wget -q -O- %s/allowed | grep "buildkit proxy ok"'`, httpURL)).
		Root().
		Run(llb.Shlex(`sh -c '! wget -S -O- https://buildkit-ca-test.invalid/denied 2>/tmp/wget.log; grep "HTTP/1.1 502 Bad Gateway" /tmp/wget.log'`)).
		Root().
		Run(llb.Shlex(`sh -c 'unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy ALL_PROXY all_proxy NO_PROXY no_proxy; ! wget -T 2 -q -O- http://1.1.1.1/'`)).
		Root().
		Run(llb.Shlexf(`sh -c 'proxy=${HTTP_PROXY#http://}; host=${proxy%%:*}; unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy ALL_PROXY all_proxy NO_PROXY no_proxy; ! wget -T 2 -q -O- http://$host:%s/'`, leakPort)).
		Root().
		Run(llb.Shlex(`sh -c 'grep "buildkit proxy CA begin" /etc/ssl/certs/ca-certificates.crt'`)).
		Root().
		Run(llb.Shlex(`sh -c '! grep "buildkit proxy CA begin" /etc/ssl/certs/ca-certificates.crt'`), llb.Network(llb.NetModeNone))

	def, err := st.Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		ProxyNetwork: true,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, int32(0), leakHit.Load())

	var checked atomic.Int32
	denyProvider := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		if req.Source.Source.Identifier != httpURL+"/allowed" {
			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_ALLOW,
			}, nil, nil
		}
		checked.Add(1)
		return &policysession.DecisionResponse{
			Action: sourcepolicypb.PolicyAction_DENY,
		}, nil, nil
	})

	deny := llb.Image("alpine:latest").
		Run(llb.Shlexf(`wget -q -O- %s/allowed`, httpURL), llb.IgnoreCache)
	def, err = deny.Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		ProxyNetwork:         true,
		SourcePolicyProvider: denyProvider,
	}, nil)
	require.Error(t, err)
	require.Equal(t, int32(1), checked.Load())

	destDir := t.TempDir()
	withProvenance := llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c 'wget -q -O /out/proxy-material %s/allowed'`, httpURL)).
		AddMount("/out", llb.Scratch())
	def, err = withProvenance.Marshal(ctx)
	require.NoError(t, err)
	materialURL := httpURL + "/allowed"
	statusCh := make(chan *SolveStatus)
	logsCh := make(chan string, 1)
	go func() {
		var b strings.Builder
		for st := range statusCh {
			for _, l := range st.Logs {
				b.Write(l.Data)
			}
		}
		logsCh <- b.String()
	}()
	_, err = c.Solve(ctx, def, SolveOpt{
		ProxyNetwork: true,
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1",
		},
		Exports: []ExportEntry{{
			Type:      ExporterLocal,
			OutputDir: destDir,
		}},
	}, statusCh)
	require.NoError(t, err)
	logOutput := <-logsCh
	require.Contains(t, logOutput, "proxy network requests:\n- GET "+materialURL)

	dt, err := os.ReadFile(filepath.Join(destDir, "proxy-material"))
	require.NoError(t, err)
	require.Equal(t, payload, dt)

	provDt, err := os.ReadFile(filepath.Join(destDir, "provenance.json"))
	require.NoError(t, err)
	var stmt struct {
		intoto.StatementHeader
		Predicate provenancetypes.ProvenancePredicateSLSA1 `json:"predicate"`
	}
	require.NoError(t, json.Unmarshal(provDt, &stmt))
	foundMaterial := false
	expectedDigest := digest.FromBytes(payload)
	for _, m := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		if m.URI == materialURL {
			foundMaterial = true
			require.Equal(t, expectedDigest.Hex(), m.Digest["sha256"])
		}
	}
	require.True(t, foundMaterial, "expected to find %q in %+v", materialURL, stmt.Predicate.BuildDefinition.ResolvedDependencies)
	require.False(t, stmt.Predicate.RunDetails.Metadata.Hermetic)
	require.True(t, stmt.Predicate.RunDetails.Metadata.Completeness.ResolvedDependencies)
	require.NotNil(t, stmt.Predicate.RunDetails.Metadata.BuildKitMetadata.Network)
	require.Equal(t, "proxy", stmt.Predicate.RunDetails.Metadata.BuildKitMetadata.Network.Mode)

	convertDestDir := t.TempDir()
	convertFooURL := httpURL + "/foo"
	convertBarURL := httpURL + "/bar"
	convert := llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c 'wget -q -O /out/proxy-material %s'`, convertFooURL)).
		AddMount("/out", llb.Scratch())
	def, err = convert.Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		ProxyNetwork: true,
		SourcePolicy: &sourcepolicypb.Policy{
			Rules: []*sourcepolicypb.Rule{
				{
					Action: sourcepolicypb.PolicyAction_CONVERT,
					Selector: &sourcepolicypb.Selector{
						Identifier: convertFooURL,
					},
					Updates: &sourcepolicypb.Update{
						Identifier: convertBarURL,
					},
				},
			},
		},
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1",
		},
		Exports: []ExportEntry{{
			Type:      ExporterLocal,
			OutputDir: convertDestDir,
		}},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(convertDestDir, "proxy-material"))
	require.NoError(t, err)
	require.Equal(t, convertedPayload, dt)

	provDt, err = os.ReadFile(filepath.Join(convertDestDir, "provenance.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(provDt, &stmt))
	foundMaterial = false
	expectedDigest = digest.FromBytes(convertedPayload)
	for _, m := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		require.NotEqual(t, convertFooURL, m.URI)
		if m.URI == convertBarURL {
			foundMaterial = true
			require.Equal(t, expectedDigest.Hex(), m.Digest["sha256"])
		}
	}
	require.True(t, foundMaterial, "expected to find %q in %+v", convertBarURL, stmt.Predicate.BuildDefinition.ResolvedDependencies)

	strict := llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c 'wget -q -O- %s/missing || true'`, httpURL)).
		AddMount("/out", llb.Scratch())
	def, err = strict.Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		ProxyNetwork: true,
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1,complete-materials=true",
		},
		Exports: []ExportEntry{{
			Type:      ExporterLocal,
			OutputDir: t.TempDir(),
		}},
	}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "provenance materials are incomplete")
	require.ErrorContains(t, err, "/missing")
	var materialsErr *solvererrdefs.ProvenanceMaterialsIncompleteError
	require.ErrorAs(t, err, &materialsErr)
	require.Len(t, materialsErr.Incomplete, 1)
	require.Equal(t, httpURL+"/missing", materialsErr.Incomplete[0].Uri)
	require.Equal(t, "unsuccessful_response", materialsErr.Incomplete[0].Reason)
}

func testProxyNetworkModesNoRootless(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCNINetwork)
	if os.Getenv("BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS") == "" {
		t.SkipNow()
	}
	if sb.Rootless() {
		t.SkipNow()
	}

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	hostPayload := []byte("proxy host ok\n")
	var hostHit atomic.Int32
	hostSrv, hostURL := newProxyHTTPServer(t, "127.0.0.1", "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostHit.Add(1)
		if r.URL.Path != "/host" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(hostPayload)
	}))
	defer hostSrv.Close()
	hostPath := hostURL + "/host"

	internetURL := "http://example.com/"
	allowedHost := []string{entitlements.EntitlementNetworkHost.String()}
	defaultHasHostLoopback := proxyNetModeDefaultHasHostLoopback(sb)

	// Host mode without the network.host entitlement should be rejected before exec starts.
	hostWithoutEntitlement := llb.Image("alpine:latest").
		Run(llb.Shlexf(`wget -q -O- %s`, hostPath), llb.Network(llb.NetModeHost), llb.IgnoreCache).
		Root()
	def, err := hostWithoutEntitlement.Marshal(ctx)
	require.NoError(t, err)
	_, err = c.Solve(ctx, def, SolveOpt{
		ProxyNetwork: true,
	}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "network.host is not allowed")
	require.Equal(t, int32(0), hostHit.Load())

	runProxySolve := func(name string, st llb.State, allowedEntitlements []string) (string, error) {
		t.Helper()
		def, err := st.Marshal(ctx)
		require.NoError(t, err, name)

		statusCh := make(chan *SolveStatus)
		logsCh := make(chan string, 1)
		go func() {
			var b strings.Builder
			for st := range statusCh {
				for _, l := range st.Logs {
					b.Write(l.Data)
				}
			}
			logsCh <- b.String()
		}()

		_, err = c.Solve(ctx, def, SolveOpt{
			ProxyNetwork:        true,
			AllowedEntitlements: allowedEntitlements,
		}, statusCh)
		return <-logsCh, err
	}

	// Default proxy egress should allow normal external network access.
	logOutput, err := runProxySolve("default internet", llb.Image("alpine:latest").
		Run(llb.Shlexf(`wget -q -O /tmp/internet %s`, internetURL), llb.IgnoreCache).
		Root(), nil)
	require.NoError(t, err)
	require.Contains(t, logOutput, "proxy network requests:\n- GET "+internetURL+" -> 200")
	require.Equal(t, int32(0), hostHit.Load())

	expectedHostHits := int32(0)
	if defaultHasHostLoopback {
		// Proxy UNSET should follow the worker default provider, including host fallback/default.
		logOutput, err = runProxySolve("default host fallback", llb.Image("alpine:latest").
			Run(llb.Shlexf(`sh -c 'env NO_PROXY= no_proxy= wget -q -O- %s | grep "proxy host ok"'`, hostPath), llb.IgnoreCache).
			Root(), nil)
		require.NoError(t, err)
		require.Contains(t, logOutput, "proxy network requests:\n- GET "+hostPath+" -> 200")
		expectedHostHits = 1
		require.Equal(t, expectedHostHits, hostHit.Load())
	} else {
		// Bridge proxy egress should not reach services bound to buildkitd host loopback.
		logOutput, err = runProxySolve("bridge host", llb.Image("alpine:latest").
			// Clear NO_PROXY so the localhost URL is forced through the BuildKit proxy.
			Run(llb.Shlexf(`sh -c '! env NO_PROXY= no_proxy= wget -T 2 -q -O- %s'`, hostPath), llb.IgnoreCache).
			Root(), nil)
		require.NoError(t, err)
		require.Contains(t, logOutput, "proxy network requests:\n- GET "+hostPath+" -> 502")
		require.Equal(t, expectedHostHits, hostHit.Load())
	}

	// Host proxy egress is allowed only with the network.host entitlement.
	logOutput, err = runProxySolve("host allowed", llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c 'env NO_PROXY= no_proxy= wget -q -O- %s | grep "proxy host ok"'`, hostPath), llb.Network(llb.NetModeHost), llb.IgnoreCache).
		Root(), allowedHost)
	require.NoError(t, err)
	require.Contains(t, logOutput, "proxy network requests:\n- GET "+hostPath+" -> 200")
	expectedHostHits++
	require.Equal(t, expectedHostHits, hostHit.Load())

	// None mode should not inject proxy env or allow external network access.
	logOutput, err = runProxySolve("none internet", llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c '! env | grep -i "^HTTP_PROXY="; ! wget -T 2 -q -O- %s'`, internetURL), llb.Network(llb.NetModeNone), llb.IgnoreCache).
		Root(), nil)
	require.NoError(t, err)
	require.NotContains(t, logOutput, internetURL)

	// None mode should also be unable to reach buildkitd host loopback.
	logOutput, err = runProxySolve("none host", llb.Image("alpine:latest").
		Run(llb.Shlexf(`sh -c '! env | grep -i "^HTTP_PROXY="; ! wget -T 2 -q -O- %s'`, hostPath), llb.Network(llb.NetModeNone), llb.IgnoreCache).
		Root(), nil)
	require.NoError(t, err)
	require.NotContains(t, logOutput, hostPath)
	require.Equal(t, expectedHostHits, hostHit.Load())
}

func testProxyNetworkDefaultEgressNoRootless(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	if sb.Rootless() {
		t.SkipNow()
	}

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	hostSrv, hostURL := newProxyHTTPServer(t, "127.0.0.1", "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host-default", "/denied":
		default:
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("proxy host default ok\n"))
	}))
	defer hostSrv.Close()

	runProxySolve := func(st llb.State, opt SolveOpt) (string, error) {
		t.Helper()
		def, err := st.Marshal(ctx)
		require.NoError(t, err)

		statusCh := make(chan *SolveStatus)
		logsCh := make(chan string, 1)
		go func() {
			var b strings.Builder
			for st := range statusCh {
				for _, l := range st.Logs {
					b.Write(l.Data)
				}
			}
			logsCh <- b.String()
		}()

		_, err = c.Solve(ctx, def, opt, statusCh)
		return <-logsCh, err
	}

	defaultHasHostLoopback := proxyNetModeDefaultHasHostLoopback(sb)
	hostDefaultCmd := llb.Shlexf(`sh -c 'env NO_PROXY= no_proxy= wget -q -O- %s/host-default | grep "proxy host default ok"'`, hostURL)
	if !defaultHasHostLoopback {
		hostDefaultCmd = llb.Shlexf(`sh -c '! env NO_PROXY= no_proxy= wget -T 2 -q -O- %s/host-default'`, hostURL)
	}
	st := llb.Image("alpine:latest").
		Run(hostDefaultCmd, llb.IgnoreCache).
		Root()
	logOutput, err := runProxySolve(st, SolveOpt{
		ProxyNetwork: true,
	})
	require.NoError(t, err)
	if defaultHasHostLoopback {
		require.Contains(t, logOutput, "proxy network requests:\n- GET "+hostURL+"/host-default -> 200")
	} else {
		require.Contains(t, logOutput, "proxy network requests:\n- GET "+hostURL+"/host-default -> 502")
	}

	var checked atomic.Int32
	denyProvider := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		if req.Source.Source.Identifier == hostURL+"/denied" {
			checked.Add(1)
			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_DENY,
			}, nil, nil
		}
		return &policysession.DecisionResponse{
			Action: sourcepolicypb.PolicyAction_ALLOW,
		}, nil, nil
	})

	deny := llb.Image("alpine:latest").
		Run(llb.Shlexf(`env NO_PROXY= no_proxy= wget -S -O- %s/denied`, hostURL), llb.IgnoreCache).
		Root()
	logOutput, err = runProxySolve(deny, SolveOpt{
		ProxyNetwork:         true,
		SourcePolicyProvider: denyProvider,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "exit code: 1")
	require.Contains(t, logOutput, "HTTP/1.1 403 Forbidden")
	require.Equal(t, int32(1), checked.Load())
	require.NotContains(t, err.Error(), "unknown proxy egress network mode UNSET")
}

func proxyNetModeDefaultHasHostLoopback(sb integration.Sandbox) bool {
	if sb.DockerAddress() != "" {
		return false
	}
	switch sb.Value("netmode").(type) {
	case *netModeProxyDefaultNoCNI, *netModeProxyHost:
		return true
	default:
		return false
	}
}

func newProxyHTTPServer(t *testing.T, listenHost, urlHost string, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp4", net.JoinHostPort(listenHost, "0"))
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return srv, "http://" + net.JoinHostPort(urlHost, port)
}

func testHostIP(t *testing.T) string {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "udp4", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && !addr.IP.IsLoopback() {
			return addr.IP.String()
		}
	}
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		require.NoError(t, err)
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	t.Fatal("could not find non-loopback host IP for proxy integration test")
	return ""
}

func testSourcePolicySession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type tcase struct {
		name          string
		state         func() llb.State
		callbacks     []policysession.PolicyCallback
		expectedError string
	}

	tcases := []tcase{
		{
			name:  "basic alpine",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, runtime.GOOS, req.Platform.OS)
					require.Equal(t, runtime.GOARCH, req.Platform.Architecture)

					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "alpine with attrs",
			state: func() llb.State { return llb.Image("alpine", llb.WithLayerLimit(1)) },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Equal(t, map[string]string{
						"image.layerlimit": "1",
					}, req.Source.Source.Attrs)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "deny alpine",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return nil, nil, errors.New("policy denied")
				},
			},
			expectedError: "policy denied",
		},
		{
			name:  "alpine with digest policy",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					return nil, &pb.ResolveSourceMetaRequest{
						Source:   req.Source.Source,
						Platform: req.Platform,
					}, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.NotEmpty(t, req.Source.Image.Digest)
					_, err := digest.Parse(req.Source.Image.Digest)
					require.NoError(t, err)
					require.NotEmpty(t, req.Source.Image.Config)
					var cfg ocispecs.Image
					err = json.Unmarshal(req.Source.Image.Config, &cfg)
					require.NoError(t, err)
					require.NotEmpty(t, cfg.RootFS)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.state()
			def, err := st.Marshal(ctx)
			require.NoError(t, err)

			callCounter := 0

			p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
				if callCounter >= len(tc.callbacks) {
					return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
				}
				cb := tc.callbacks[callCounter]
				callCounter++
				return cb(ctx, req)
			})

			_, err = c.Solve(ctx, def, SolveOpt{
				SourcePolicyProvider: p,
			}, nil)
			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}
			require.NoError(t, err)

			require.Equal(t, len(tc.callbacks), callCounter, "not all policy callbacks were called")
		})
	}
}

func testSourcePolicySessionDenyMessages(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("alpine").Marshal(ctx)
	require.NoError(t, err)

	p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
		return &policysession.DecisionResponse{
			Action: sourcepolicypb.PolicyAction_DENY,
			DenyMessages: []*policysession.DenyMessage{
				{Message: "policy blocked alpine"},
				{Message: "use busybox instead"},
			},
		}, nil, nil
	})

	_, err = c.Solve(ctx, def, SolveOpt{
		SourcePolicyProvider: p,
	}, nil)
	require.Error(t, err)

	denyMessages := policysession.DenyMessages(err)
	require.Len(t, denyMessages, 2)
	require.Equal(t, "policy blocked alpine", denyMessages[0].GetMessage())
	require.Equal(t, "use busybox instead", denyMessages[1].GetMessage())
}

func testSourceMetaPolicySession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type tcase struct {
		name          string
		source        func() (*opspb.SourceOp, sourceresolver.Opt)
		callbacks     []policysession.PolicyCallback
		expectedError string
	}
	tcases := []tcase{
		{
			name: "basic alpine",
			source: func() (*opspb.SourceOp, sourceresolver.Opt) {
				p := platforms.DefaultSpec()
				return &opspb.SourceOp{
						Identifier: "docker-image://docker.io/library/alpine:latest",
					}, sourceresolver.Opt{
						ImageOpt: &sourceresolver.ResolveImageOpt{
							Platform: &p,
						},
					}
			},
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, runtime.GOOS, req.Platform.OS)
					require.Equal(t, runtime.GOARCH, req.Platform.Architecture)

					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name: "alpine denied",
			source: func() (*opspb.SourceOp, sourceresolver.Opt) {
				return &opspb.SourceOp{
					Identifier: "docker-image://docker.io/library/alpine:latest",
				}, sourceresolver.Opt{}
			},
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return nil, nil, errors.New("policy denied")
				},
			},
			expectedError: "policy denied",
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			callCounter := 0

			p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
				if callCounter >= len(tc.callbacks) {
					return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
				}
				cb := tc.callbacks[callCounter]
				callCounter++
				return cb(ctx, req)
			})
			_, err = c.Build(ctx, SolveOpt{
				SourcePolicyProvider: p,
			}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
				sop, opts := tc.source()
				_, err = c.ResolveSourceMetadata(ctx, sop, opts)
				return nil, err
			}, nil)

			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}
			require.NoError(t, err)

			require.Equal(t, len(tc.callbacks), callCounter, "not all policy callbacks were called")
		})
	}
}

func testSourceMetaPolicySessionResolveAttestations(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureProvenance)
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target, platform := buildProvenanceImage(ctx, t, c, sb)
	sourceID := "docker-image://" + target
	requestedPredicateType := policyimage.SLSAProvenancePredicateType1

	callbackCalls := 0
	p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		switch callbackCalls {
		case 0:
			callbackCalls++
			require.Equal(t, sourceID, req.Source.Source.Identifier)
			require.Nil(t, req.Source.Image)
			return nil, &pb.ResolveSourceMetaRequest{
				Source:   req.Source.Source,
				Platform: req.Platform,
				Image: &pb.ResolveSourceImageRequest{
					NoConfig:            true,
					ResolveAttestations: []string{requestedPredicateType},
				},
			}, nil
		case 1:
			callbackCalls++
			require.Equal(t, sourceID, req.Source.Source.Identifier)
			require.NotNil(t, req.Source.Image)
			require.Empty(t, req.Source.Image.Config)
			require.NotNil(t, req.Source.Image.AttestationChain)
			ac := req.Source.Image.AttestationChain
			require.NotEmpty(t, ac.AttestationManifest)

			att, ok := ac.Blobs[ac.AttestationManifest]
			require.True(t, ok)
			require.NotEmpty(t, att.Data)

			var manifest ocispecs.Manifest
			require.NoError(t, json.Unmarshal(att.Data, &manifest))
			require.NotEmpty(t, manifest.Layers)

			foundRequestedType := false

			imageManifestDigest, err := digest.Parse(ac.ImageManifest)
			require.NoError(t, err)

			for _, layer := range manifest.Layers {
				layerPredicateType := layer.Annotations["in-toto.io/predicate-type"]
				if layerPredicateType != requestedPredicateType {
					continue
				}
				foundRequestedType = true

				blob, ok := ac.Blobs[string(layer.Digest)]
				require.True(t, ok, "missing blob for requested predicate type %q", layerPredicateType)
				require.NotEmpty(t, blob.Data, "empty blob data for requested predicate type %q", layerPredicateType)

				var stmt intoto.Statement
				require.NoError(t, json.Unmarshal(blob.Data, &stmt))
				require.Equal(t, intoto.StatementInTotoV1, stmt.Type)
				require.Equal(t, layerPredicateType, stmt.PredicateType)
				require.NotEmpty(t, stmt.Subject)
				require.Equal(t, imageManifestDigest.Hex(), stmt.Subject[0].Digest["sha256"])
			}
			require.True(t, foundRequestedType, "requested predicate type %q not found in attestation manifest layers", requestedPredicateType)

			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_ALLOW,
			}, nil, nil
		default:
			return nil, nil, errors.Errorf("too many policy callbacks: %d", callbackCalls)
		}
	})

	_, err = c.Build(ctx, SolveOpt{
		SourcePolicyProvider: p,
	}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		_, err := c.ResolveSourceMetadata(ctx, &opspb.SourceOp{
			Identifier: sourceID,
		}, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				Platform: &platform,
			},
		})
		return nil, err
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, callbackCalls)
}

func testSourcePolicyParallelSession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("alpine").File(llb.Copy(llb.Image("busybox"), "/etc/passwd", "passwd2")).Marshal(ctx)
	require.NoError(t, err)

	countAlpine := 0
	countBusybox := 0
	waitBusyboxStart := make(chan struct{})
	waitAlpineDone := make(chan struct{})

	p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		switch req.Source.Source.Identifier {
		case "docker-image://docker.io/library/alpine:latest":
			switch countAlpine {
			case 0:
				<-waitBusyboxStart
				require.Nil(t, req.Source.Image)
				countAlpine++
				return nil, &pb.ResolveSourceMetaRequest{
					Source:   req.Source.Source,
					Platform: req.Platform,
				}, nil
			case 1:
				require.NotNil(t, req.Source.Image)
				require.True(t, strings.HasPrefix(req.Source.Image.Digest, "sha256:"))
				countAlpine++
				close(waitAlpineDone)
				return &policysession.DecisionResponse{
					Action: sourcepolicypb.PolicyAction_ALLOW,
				}, nil, nil
			default:
				require.Fail(t, "too many calls for alpine")
			}
		case "docker-image://docker.io/library/busybox:latest":
			time.Sleep(200 * time.Millisecond)
			close(waitBusyboxStart)
			countBusybox++
			<-waitAlpineDone
			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_ALLOW,
			}, nil, nil
		}
		return nil, nil, errors.Errorf("unexpected source %q", req.Source.Source.Identifier)
	})

	_, err = c.Solve(ctx, def, SolveOpt{
		SourcePolicyProvider: p,
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 2, countAlpine)
	require.Equal(t, 1, countBusybox)
}

func testSourcePolicySignedCommit(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	signFixturesPath, ok := os.LookupEnv("BUILDKIT_TEST_SIGN_FIXTURES")
	if !ok {
		t.Skip("missing BUILDKIT_TEST_SIGN_FIXTURES")
	}

	withSign := func(user, method string) []string {
		return []string{
			"GIT_CONFIG_GLOBAL=" + filepath.Join(signFixturesPath, user+"."+method+".gitconfig"),
		}
	}

	gitDir := t.TempDir()
	gitCommands := []string{
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo a > a",
		"git add a",
		"git commit -m a",
		"git tag -a v0.1 -m v0.1",
	}
	err = runInDir(gitDir, gitCommands...)
	require.NoError(t, err)
	gitCommands = []string{
		"echo b > b",
		"git add b",
		"git commit -m b",
		"git checkout -B v2",
	}
	err = runInDirEnv(gitDir, withSign("user1", "gpg"), gitCommands...)
	require.NoError(t, err)
	gitCommands = []string{
		"git tag -s -a v2.0 -m v2.0-tag",
		"git update-server-info",
	}
	err = runInDirEnv(gitDir, withSign("user2", "ssh"), gitCommands...)
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	pubKeyUser1gpg, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.gpg.pub"))
	require.NoError(t, err)

	pubKeyUser2ssh, err := os.ReadFile(filepath.Join(signFixturesPath, "user2.ssh.pub"))
	require.NoError(t, err)

	type testCase struct {
		state       func() llb.State
		name        string
		srcPol      *sourcepolicypb.Policy
		expectedErr string
	}

	gitURL := "git://" + strings.TrimPrefix(server.URL, "http://") + "/.git"

	tests := []testCase{
		{
			name: "unsigned commit fails",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v0.1",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v0.1",
							Attrs: map[string]string{
								"git.sig.pubkey": string(pubKeyUser1gpg),
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v0.1"))
			},
			expectedErr: "git object is not signed",
		},
		{
			name: "valid gpg signature for branch",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2",
							Attrs: map[string]string{
								"git.sig.pubkey":          string(pubKeyUser1gpg),
								"git.sig.rejectexpired":   "true",
								"git.sig.ignoresignedtag": "false",
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2"))
			},
		},
		{
			name: "valid ssh signature for signed tag",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":           string(pubKeyUser2ssh),
								"git.sig.requiresignedtag": "true",
								"git.sig.rejectexpired":    "true",
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
		{
			name: "invalid ssh signature for signed tag",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":           string(pubKeyUser1gpg),
								"git.sig.requiresignedtag": "true",
								"git.sig.rejectexpired":    "true",
							},
						},
					},
				},
			},
			expectedErr: "failed to parse ssh public key",
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
		{
			name: "commit ssh signature for signed tag",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":           string(pubKeyUser1gpg),
								"git.sig.requiresignedtag": "false",
								"git.sig.rejectexpired":    "true",
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
		{
			name: "invalid tag signature for commit",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":          string(pubKeyUser2ssh),
								"git.sig.rejectexpired":   "true",
								"git.sig.ignoresignedtag": "true",
							},
						},
					},
				},
			},
			expectedErr: "failed to read armored public key",
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
				st := llb.Scratch().File(
					llb.Copy(tt.state(), "a", "/a2"),
				)
				def, err := st.Marshal(sb.Context())
				if err != nil {
					return nil, err
				}
				return c.Solve(ctx, gateway.SolveRequest{
					Definition: def.ToPB(),
				})
			}

			_, err := c.Build(sb.Context(), SolveOpt{
				SourcePolicy: tt.srcPol,
			}, "", frontend, nil)
			if tt.expectedErr == "" {
				require.NoError(t, err, "test case %q failed", tt.name)
				return
			}
			require.ErrorContains(t, err, tt.expectedErr, "test case %q failed", tt.name)
		})
	}

	// session policy based test cases

	type tcase struct {
		name          string
		state         func() llb.State
		callbacks     []policysession.PolicyCallback
		expectedError string
	}

	tcases := []tcase{
		{
			name:  "gitchecksum",
			state: func() llb.State { return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0")) },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Git)
					return nil, &pb.ResolveSourceMetaRequest{
						Source:   req.Source.Source,
						Platform: req.Platform,
					}, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.NotNil(t, req.Source.Git)
					require.Len(t, req.Source.Git.Checksum, 40)
					require.Len(t, req.Source.Git.CommitChecksum, 40)
					require.NotEqual(t, req.Source.Git.Checksum, req.Source.Git.CommitChecksum)
					require.Nil(t, req.Source.Git.CommitObject)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "gitobjects",
			state: func() llb.State { return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0")) },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Git)
					return nil, &pb.ResolveSourceMetaRequest{
						Source:   req.Source.Source,
						Platform: req.Platform,
						Git: &pb.ResolveSourceGitRequest{
							ReturnObject: true,
						},
					}, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.NotNil(t, req.Source.Git)
					require.Len(t, req.Source.Git.Checksum, 40)
					require.Len(t, req.Source.Git.CommitChecksum, 40)
					require.NotEqual(t, req.Source.Git.Checksum, req.Source.Git.CommitChecksum)
					require.NotNil(t, req.Source.Git.CommitObject)
					require.Greater(t, len(req.Source.Git.CommitObject), 50)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.state()
			def, err := st.Marshal(ctx)
			require.NoError(t, err)

			callCounter := 0

			p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
				if callCounter >= len(tc.callbacks) {
					return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
				}
				cb := tc.callbacks[callCounter]
				callCounter++
				return cb(ctx, req)
			})

			_, err = c.Solve(ctx, def, SolveOpt{
				SourcePolicyProvider: p,
			}, nil)
			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}
			require.NoError(t, err)

			require.Equal(t, len(tc.callbacks), callCounter, "not all policy callbacks were called")
		})
	}
}

func testSourcePolicySessionConvert(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type tcase struct {
		name          string
		state         func() llb.State
		callbacks     []policysession.PolicyCallback
		expectedError string
	}

	tcases := []tcase{
		{
			name:  "convert and allow",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					src := req.Source.Source
					src.Identifier = "docker-image://docker.io/library/busybox:latest"
					if src.Attrs == nil {
						src.Attrs = map[string]string{}
					}
					src.Attrs["foo"] = "bar"
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Update: src,
					}, nil, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/busybox:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					require.Equal(t, "bar", req.Source.Source.Attrs["foo"])
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "convert and deny",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					src := req.Source.Source
					if src.Attrs == nil {
						src.Attrs = map[string]string{}
					}
					src.Attrs["foo"] = "bar"
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Update: src,
					}, nil, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					require.Equal(t, "bar", req.Source.Source.Attrs["foo"])
					src := req.Source.Source
					src.Attrs["foo"] = "baz"
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Update: src,
					}, nil, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					require.Equal(t, "baz", req.Source.Source.Attrs["foo"])
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_DENY,
					}, nil, nil
				},
			},
			expectedError: "not allowed by policy",
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.state()
			def, err := st.Marshal(ctx)
			require.NoError(t, err)

			callCounter := 0

			p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
				if callCounter >= len(tc.callbacks) {
					return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
				}
				cb := tc.callbacks[callCounter]
				callCounter++
				return cb(ctx, req)
			})

			_, err = c.Solve(ctx, def, SolveOpt{
				SourcePolicyProvider: p,
			}, nil)
			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}
			require.NoError(t, err)

			require.Equal(t, len(tc.callbacks), callCounter, "not all policy callbacks were called")
		})
	}

	// policy loop test
	t.Run("convert loop", func(t *testing.T) {
		def, err := llb.Image("alpine").Marshal(ctx)
		require.NoError(t, err)

		calls := 0

		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
			require.Nil(t, req.Source.Image)
			calls++
			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_CONVERT,
				Update: req.Source.Source,
			}, nil, nil
		})
		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.ErrorContains(t, err, "too many policy requests")
		require.Equal(t, 10, calls) // this is not strict value but just to make sure calls happened. future version may optimize this with less calls.
	})
}

func testSourcePolicySessionHTTPChecksumAssist(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	signFixturesPath, ok := os.LookupEnv("BUILDKIT_TEST_SIGN_FIXTURES")
	if !ok {
		t.Skip("missing BUILDKIT_TEST_SIGN_FIXTURES")
	}

	payload, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.http.artifact"))
	require.NoError(t, err)
	sigData, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.http.artifact.asc"))
	require.NoError(t, err)
	pubKeyData, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.gpg.pub"))
	require.NoError(t, err)
	sig, _, err := pgpsign.ParseArmoredDetachedSignature(sigData)
	require.NoError(t, err)
	keyring, err := pgpsign.ReadAllArmoredKeyRings(pubKeyData)
	require.NoError(t, err)

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artifact.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer httpSrv.Close()

	def, err := llb.Scratch().File(llb.Copy(llb.HTTP(httpSrv.URL+"/artifact.txt"), "artifact.txt", "/artifact.txt")).Marshal(ctx)
	require.NoError(t, err)

	t.Run("valid checksum request", func(t *testing.T) {
		algo, err := toPBChecksumAlgo(sig.Hash)
		require.NoError(t, err)
		expectedDigest, err := payloadWithSuffixDigest(sig.Hash, payload, sig.HashSuffix)
		require.NoError(t, err)

		callCounter := 0
		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			switch callCounter {
			case 0:
				callCounter++
				return nil, &pb.ResolveSourceMetaRequest{
					Source:   req.Source.Source,
					Platform: req.Platform,
					HTTP: &pb.ResolveSourceHTTPRequest{
						ChecksumRequest: &pb.ChecksumRequest{
							Algo:   algo,
							Suffix: slices.Clone(sig.HashSuffix),
						},
					},
				}, nil
			case 1:
				callCounter++
				require.NotNil(t, req.Source.HTTP)
				require.NotNil(t, req.Source.HTTP.ChecksumResponse)
				require.Equal(t, expectedDigest, req.Source.HTTP.ChecksumResponse.Digest)
				require.Equal(t, sig.HashSuffix, req.Source.HTTP.ChecksumResponse.Suffix)
				responseDigest, err := digest.Parse(req.Source.HTTP.ChecksumResponse.Digest)
				require.NoError(t, err)
				require.NoError(t, pgpsign.VerifySignatureWithDigest(sig, keyring, responseDigest))
				// Negative check: tampered digest must fail signature verification.
				badDigest := tamperDigestHex(responseDigest)
				err = pgpsign.VerifySignatureWithDigest(sig, keyring, badDigest)
				require.Error(t, err)
				require.ErrorContains(t, err, "failed to verify signature with checksum digest")
				return &policysession.DecisionResponse{
					Action: sourcepolicypb.PolicyAction_ALLOW,
				}, nil, nil
			default:
				return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
			}
		})

		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.NoError(t, err)
		require.Equal(t, 2, callCounter)
	})

	t.Run("oversized suffix denied", func(t *testing.T) {
		callCounter := 0
		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			callCounter++
			return nil, &pb.ResolveSourceMetaRequest{
				Source:   req.Source.Source,
				Platform: req.Platform,
				HTTP: &pb.ResolveSourceHTTPRequest{
					ChecksumRequest: &pb.ChecksumRequest{
						Algo:   pb.ChecksumRequest_CHECKSUM_ALGO_SHA256,
						Suffix: make([]byte, 4097),
					},
				},
			}, nil
		})

		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "suffix exceeds max size")
		require.Equal(t, 1, callCounter)
	})

	t.Run("unsupported algo denied", func(t *testing.T) {
		callCounter := 0
		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			callCounter++
			return nil, &pb.ResolveSourceMetaRequest{
				Source:   req.Source.Source,
				Platform: req.Platform,
				HTTP: &pb.ResolveSourceHTTPRequest{
					ChecksumRequest: &pb.ChecksumRequest{
						Algo:   pb.ChecksumRequest_ChecksumAlgo(99),
						Suffix: []byte{1, 2, 3},
					},
				},
			}, nil
		})

		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "unsupported checksum algorithm")
		require.Equal(t, 1, callCounter)
	})
}

func toPBChecksumAlgo(in crypto.Hash) (pb.ChecksumRequest_ChecksumAlgo, error) {
	switch in {
	case crypto.SHA256:
		return pb.ChecksumRequest_CHECKSUM_ALGO_SHA256, nil
	case crypto.SHA384:
		return pb.ChecksumRequest_CHECKSUM_ALGO_SHA384, nil
	case crypto.SHA512:
		return pb.ChecksumRequest_CHECKSUM_ALGO_SHA512, nil
	default:
		return 0, errors.Errorf("unsupported signature hash algorithm %v", in)
	}
}

func payloadWithSuffixDigest(algo crypto.Hash, payload, suffix []byte) (string, error) {
	var (
		h        hash.Hash
		algoName string
	)
	switch algo {
	case crypto.SHA256:
		h = sha256.New()
		algoName = "sha256"
	case crypto.SHA384:
		h = sha512.New384()
		algoName = "sha384"
	case crypto.SHA512:
		h = sha512.New()
		algoName = "sha512"
	default:
		return "", errors.Errorf("unsupported signature hash algorithm %v", algo)
	}
	if _, err := h.Write(payload); err != nil {
		return "", err
	}
	if _, err := h.Write(suffix); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%x", algoName, h.Sum(nil)), nil
}

func tamperDigestHex(dgst digest.Digest) digest.Digest {
	hexPart := []byte(dgst.Encoded())
	if len(hexPart) == 0 {
		return dgst
	}
	if hexPart[len(hexPart)-1] == '0' {
		hexPart[len(hexPart)-1] = '1'
	} else {
		hexPart[len(hexPart)-1] = '0'
	}
	return digest.NewDigestFromEncoded(dgst.Algorithm(), string(hexPart))
}

func testSourcePolicy(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Image("busybox:1.34.1-uclibc").File(
			llb.Copy(llb.HTTP("https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md"),
				"README.md", "README.md"))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	type testCase struct {
		srcPol      *sourcepolicypb.Policy
		expectedErr string
	}
	testCases := []testCase{
		{
			// Valid
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: "docker-image://docker.io/library/busybox:1.34.1-uclibc",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: "docker-image://docker.io/library/busybox:1.34.1-uclibc@sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
						},
					},
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
							Attrs:      map[string]string{"http.checksum": "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53"},
						},
					},
				},
			},
			expectedErr: "",
		},
		{
			// Invalid docker-image source
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: "docker-image://docker.io/library/busybox:1.34.1-uclibc",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: "docker-image://docker.io/library/busybox:1.34.1-uclibc@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // invalid
						},
					},
				},
			},
			expectedErr: "docker.io/library/busybox:1.34.1-uclibc@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa: not found",
		},
		{
			// Invalid http source
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
						},
						Updates: &sourcepolicypb.Update{
							Attrs: map[string]string{opspb.AttrHTTPChecksum: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, // invalid
						},
					},
				},
			},
			expectedErr: "digest mismatch sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	for i, tc := range testCases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, err = c.Build(sb.Context(), SolveOpt{SourcePolicy: tc.srcPol}, "", frontend, nil)
			if tc.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
			}
		})
	}

	t.Run("Frontend policies", func(t *testing.T) {
		t.Run("deny http", func(t *testing.T) {
			denied := "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md"
			frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
				st := llb.Image("busybox:1.34.1-uclibc").File(
					llb.Copy(llb.HTTP(denied),
						"README.md", "README.md"))
				def, err := st.Marshal(sb.Context())
				if err != nil {
					return nil, err
				}
				return c.Solve(ctx, gateway.SolveRequest{
					Definition: def.ToPB(),
					SourcePolicies: []*sourcepolicypb.Policy{{
						Rules: []*sourcepolicypb.Rule{
							{
								Action: sourcepolicypb.PolicyAction_DENY,
								Selector: &sourcepolicypb.Selector{
									Identifier: denied,
								},
							},
						},
					}},
				})
			}

			_, err = c.Build(sb.Context(), SolveOpt{}, "", frontend, nil)
			require.ErrorContains(t, err, sourcepolicy.ErrSourceDenied.Error())
		})
		t.Run("resolve image config", func(t *testing.T) {
			frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
				const (
					origRef    = "docker.io/library/busybox:1.34.1-uclibc"
					updatedRef = "docker.io/library/busybox:latest"
				)
				pol := []*sourcepolicypb.Policy{
					{
						Rules: []*sourcepolicypb.Rule{
							{
								Action: sourcepolicypb.PolicyAction_DENY,
								Selector: &sourcepolicypb.Selector{
									Identifier: "*",
								},
							},
							{
								Action: sourcepolicypb.PolicyAction_ALLOW,
								Selector: &sourcepolicypb.Selector{
									Identifier: "docker-image://" + updatedRef + "*",
								},
							},
							{
								Action: sourcepolicypb.PolicyAction_CONVERT,
								Selector: &sourcepolicypb.Selector{
									Identifier: "docker-image://" + origRef,
								},
								Updates: &sourcepolicypb.Update{
									Identifier: "docker-image://" + updatedRef,
								},
							},
						},
					},
				}

				ref, dgst, _, err := c.ResolveImageConfig(ctx, origRef, sourceresolver.Opt{
					SourcePolicies: pol,
				})
				if err != nil {
					return nil, err
				}
				require.Equal(t, updatedRef, ref)
				st := llb.Image(ref + "@" + dgst.String())
				def, err := st.Marshal(sb.Context())
				if err != nil {
					return nil, err
				}
				return c.Solve(ctx, gateway.SolveRequest{
					Definition:     def.ToPB(),
					SourcePolicies: pol,
				})
			}
			_, err = c.Build(sb.Context(), SolveOpt{}, "", frontend, nil)
			require.NoError(t, err)
		})
	})
}
