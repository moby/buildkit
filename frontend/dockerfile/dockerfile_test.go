package dockerfile

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func init() {
	if workers.IsTestDockerd() {
		workers.InitDockerdWorker()
	} else {
		workers.InitOCIWorker()
		workers.InitContainerdWorker()
	}
}

var allTests = integration.TestFuncs(
	// dockerfile_add_test.go
	testDockerfileADDFromURL,
	testDockerfileAddArchive,
	testDockerfileAddChownArchive,
	testDockerfileAddArchiveWildcard,
	testDockerfileAddChownExpand,
	testAddURLChmod,
	testAddInvalidChmod,
	testAddChecksum,

	// dockerfile_args_test.go
	testGlobalArg,
	testGlobalArgErrors,
	testArgDefaultExpansion,
	testMultiArgs,
	testQuotedMetaArgs,
	testTargetStageNameArg,
	testBuiltinArgs,
	testDefaultEnvWithArgs,
	testEmptyStringArgInEnv,
	testEnvEmptyFormatting,

	// dockerfile_cache_test.go
	testCacheReleased,
	testExportCacheLoop,
	testCacheMultiPlatformImportExport,
	testImageManifestCacheImportExport,
	testCacheImportExport,
	testReproducibleIDs,
	testImportExportReproducibleIDs,
	testNoCache,
	testCacheMountModeNoCache,
	testWildcardRenameCache,

	// dockerfile_client_test.go
	testGatewayBuiltinSyntaxSourceDockerfileV0,
	testFrontendUseForwardedSolveResults,
	testFrontendEvaluate,
	testFrontendInputs,
	testFrontendSubrequests,
	testNilContextInSolveGateway,
	testMultiNilRefsInSolveGateway,

	// dockerfile_cmd_test.go
	testCmdShell,
	testDefaultShellAndPath,
	testDefaultPathEnvOnWindows,
	testIgnoreEntrypoint,
	testInvalidJSONCommands,
	testDockerfileInvalidCommand,
	testDockerfileInvalidInstruction,

	// dockerfile_copy_test.go
	testDockerfileCopyFromArgs,
	testChmodNonOctal,
	testEmptyDestDir,
	testPreserveDestDirSlash,
	testCopyLinkDotDestDir,
	testCopyLinkEmptyDestDir,
	testCopyChownExistingDir,
	testCopyWildcardCache,
	testEmptyWildcard,
	testCopyChownCreateDest,
	testCopyThroughSymlinkContext,
	testCopyThroughSymlinkMultiStage,
	testCopySocket,
	testCopyFollowAllSymlinks,
	testCopySymlinks,
	testCopyChown,
	testCopyChmod,
	testCopyInvalidChmod,
	testCopyOverrideFiles,
	testCopyVarSubstitution,
	testCopyWildcards,
	testCopyRelative,
	testCopyUnicodePath,
	testLocalCustomSessionID,

	// dockerfile_core_test.go
	testDockerfileDirs,
	testSymlinkedDockerfile,
	testSymlinkDestination,
	testContextChangeDirToFile,
	testNoSnapshotLeak,
	testExposeExpansion,
	testDockerfileLowercase,
	testLabels,
	testUser,
	testUserAdditionalGids,
	testDockerfileCheckHostname,
	testStepNames,
	testTargetMistype,

	// dockerfile_dockerignore_test.go
	testDockerignore,
	testDockerignoreInvalid,
	testDockerignoreOverride,

	// dockerfile_export_test.go
	testTarExporterBasic,
	testTarExporterMulti,
	testExportMultiPlatform,
	testTarContext,
	testTarContextExternalDockerfile,

	// dockerfile_git_http_test.go
	testDockerfileFromGitSHA1,
	testDockerfileFromGitSHA256,
	testDockerfileFromHTTP,
	testHTTPDockerfile,

	// dockerfile_history_test.go
	testExportedHistory,
	testExportedHistoryFlattenArgs,
	testHistoryError,
	testHistoryFinalizeTrace,

	// dockerfile_image_test.go
	testPullScratch,
	testDockerfileScratchConfig,
	testOCILayoutMultiname,
	testMultiNilRefsOCIExporter,

	// dockerfile_multistage_test.go
	testMultiStageImplicitFrom,
	testMultiStageCaseInsensitive,
	testOutOfOrderStage,
	testEmptyStages,

	// dockerfile_namedcontext_test.go
	testNamedImageContext,
	testNamedImageContextPlatform,
	testNamedImageContextTimestamps,
	testNamedImageContextScratch,
	testNamedLocalContext,
	testNamedOCILayoutContext,
	testNamedOCILayoutContextExport,
	testNamedInputContext,
	testNamedMultiplatformInputContext,
	testNamedFilteredContext,
	testSourcePolicyWithNamedContext,
	testEagerNamedContextLookup,

	// dockerfile_onbuild_test.go
	testOnBuildCleared,
	testOnBuildWithChildStage,
	testOnBuildNamedContext,
	testOnBuildInheritedStageRun,
	testOnBuildInheritedStageWithFrom,
	testOnBuildNewDeps,
	testOnBuildWithCacheMount,

	// dockerfile_platform_test.go
	testPlatformArgsImplicit,
	testPlatformArgsExplicit,
	testPlatformWithOSVersion,
	testMaintainBaseOSVersion,

	// dockerfile_provenance_test.go
	testGitProvenanceAttestationSHA1,
	testGitProvenanceAttestationSHA256,
	testMultiPlatformProvenance,
	testClientFrontendProvenance,
	testGatewayBuiltinSyntaxSourceProvenance,
	testClientLLBProvenance,
	testGatewayProvenanceRootRequest,
	testGatewayProvenanceSameCallbackInputProducer,
	testGatewayProvenanceDifferentCallbackInputProducer,
	testGatewayProvenancePlainLLBInputFallback,
	testGatewayProvenanceNestedInputProducer,
	testDockerfileProvenanceInputProducer,
	testSecretSSHProvenance,
	testOCILayoutProvenance,
	testNilProvenance,
	testDuplicatePlatformProvenance,
	testDockerIgnoreMissingProvenance,
	testCommandSourceMapping,
	testFrontendDeduplicateSources,
	testDuplicateLayersProvenance,
	testProvenanceExportLocal,
	testProvenanceExportLocalForceSplit,
	testProvenanceExportLocalMultiPlatform,
	testProvenanceExportLocalMultiPlatformNoSplit,

	// dockerfile_resources_test.go
	testShmSize,
	testUlimit,
	testCgroupParent,
	testLinuxResources,

	// dockerfile_sbom_test.go
	testSBOMScannerImage,
	testSBOMScannerArgs,

	// dockerfile_workdir_test.go
	testWorkdirCreatesDir,
	testWorkdirUser,
	testWorkdirCopyIgnoreRelative,
	testWorkdirExists,

	// errors_test.go
	testErrorsSourceMap,
)

// Tests that depend on reproducible env
var reproTests = integration.TestFuncs(
	// dockerfile_source_date_epoch_test.go
	testReproSourceDateEpoch,
	testSourceDateEpochWithoutExporter,
	testSourceDateEpochDockerfileDefault,
	testSourceDateEpochDockerfileDefaultOverride,
	testSourceDateEpochDockerfileDefaultReset,
	testSourceDateEpochDockerfileDefaultInvalid,
	testSourceDateEpochContextGit,
	testSourceDateEpochContextHTTPLastModified,
	testSourceDateEpochContextHTTPArchive,
	testSourceDateEpochContextLocalUnset,
	testSourceDateEpochStageHTTPArchive,
	testSourceDateEpochStageOverride,
	testSourceDateEpochStageInvalid,
	testSourceDateEpochNamedContextHTTPLastModified,
	testSourceDateEpochNamedContextHTTPArchive,

	// dockerfile_workdir_test.go
	testWorkdirSourceDateEpochReproducible,
)

var (
	opts         []integration.TestOpt
	securityOpts []integration.TestOpt
)

type frontend interface {
	Solve(context.Context, *client.Client, client.SolveOpt, chan *client.SolveStatus) (*client.SolveResponse, error)
	SolveGateway(context.Context, gateway.Client, gateway.SolveRequest) (*gateway.Result, error)
	DFCmdArgs(string, string) (string, string)
	RequiresBuildctl(t *testing.T)
}

func init() {
	frontends := map[string]any{}

	images := integration.UnixOrWindows(
		[]string{"busybox:latest", "alpine:latest", "busybox:stable-musl"},
		[]string{"nanoserver:latest", "nanoserver:plus", "nanoserver:plus-busybox"})
	opts = []integration.TestOpt{
		integration.WithMirroredImages(integration.OfficialImages(images...)),
		integration.WithMatrix("frontend", frontends),
	}

	if os.Getenv("FRONTEND_BUILTIN_ONLY") == "1" {
		frontends["builtin"] = &builtinFrontend{}
	} else if os.Getenv("FRONTEND_CLIENT_ONLY") == "1" {
		frontends["client"] = &clientFrontend{}
	} else if gw := os.Getenv("FRONTEND_GATEWAY_ONLY"); gw != "" {
		name := "buildkit_test/" + identity.NewID() + ":latest"
		opts = append(opts, integration.WithMirroredImages(map[string]string{
			name: gw,
		}))
		frontends["gateway"] = &gatewayFrontend{gw: name}
	} else {
		frontends["builtin"] = &builtinFrontend{}
		frontends["client"] = &clientFrontend{}
	}
}

func TestIntegration(t *testing.T) {
	integration.Run(t, allTests, opts...)

	integration.Run(t, lintTests, opts...)
	integration.Run(t, heredocTests, opts...)
	integration.Run(t, outlineTests, opts...)
	integration.Run(t, targetsTests, opts...)

	// the rest of the tests are meant for non-Windows, skipping on Windows.
	integration.SkipOnPlatform(t, "windows")

	integration.Run(t, reproTests, append(opts,
		// Only use the amd64 digest,  regardless to the host platform
		integration.WithMirroredImages(map[string]string{
			"amd64/debian:bullseye-20230109-slim": "docker.io/amd64/debian:bullseye-20230109-slim@sha256:1acb06a0c31fb467eb8327ad361f1091ab265e0bf26d452dea45dcb0c0ea5e75",
		}),
	)...)

	integration.Run(t, securityTests, append(append(opts, securityOpts...),
		integration.WithMatrix("security.insecure", map[string]any{
			"granted": securityInsecureGranted,
			"denied":  securityInsecureDenied,
		}))...)

	integration.Run(t, networkTests, append(opts,
		integration.WithMatrix("network.host", map[string]any{
			"granted": networkHostGranted,
			"denied":  networkHostDenied,
		}))...)

	integration.Run(t, integration.TestFuncs(testProvenanceAttestation), append(opts,
		integration.WithMatrix("env", map[string]any{
			"simple": provenanceEnvSimpleConfig,
		}))...)
}

func runShell(dir string, cmds ...string) error {
	for _, args := range cmds {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(context.TODO(), "powershell", "-command", args)
		} else {
			cmd = exec.CommandContext(context.TODO(), "sh", "-c", args)
		}
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "error running %v", args)
		}
	}
	return nil
}

// ensurePruneAll tries to ensure Prune completes with retries.
// Current cache implementation defers release-related logic using goroutine so
// there can be situation where a build has finished but the following prune doesn't
// cleanup cache because some records still haven't been released.
// This function tries to ensure prune by retrying it.
func ensurePruneAll(t *testing.T, c *client.Client, sb integration.Sandbox) {
	for i := range 2 {
		require.NoError(t, c.Prune(sb.Context(), nil, client.PruneAll))
		for range 20 {
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

func newContainerd(cdAddress string) (*ctd.Client, error) {
	return ctd.New(cdAddress, ctd.WithTimeout(60*time.Second))
}

func dfCmdArgs(ctx, dockerfile, args string) (string, string) {
	traceFile := filepath.Join(os.TempDir(), "trace"+identity.NewID())
	return fmt.Sprintf("build --progress=plain %s --local context=%s --local dockerfile=%s --trace=%s", args, ctx, dockerfile, traceFile), traceFile
}

type builtinFrontend struct{}

var _ frontend = &builtinFrontend{}

func (f *builtinFrontend) Solve(ctx context.Context, c *client.Client, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error) {
	opt.Frontend = "dockerfile.v0"
	return c.Solve(ctx, nil, opt, statusChan)
}

func (f *builtinFrontend) SolveGateway(ctx context.Context, c gateway.Client, req gateway.SolveRequest) (*gateway.Result, error) {
	req.Frontend = "dockerfile.v0"
	return c.Solve(ctx, req)
}

func (f *builtinFrontend) DFCmdArgs(ctx, dockerfile string) (string, string) {
	return dfCmdArgs(ctx, dockerfile, "--frontend dockerfile.v0")
}

func (f *builtinFrontend) RequiresBuildctl(t *testing.T) {}

type clientFrontend struct{}

var _ frontend = &clientFrontend{}

func (f *clientFrontend) Solve(ctx context.Context, c *client.Client, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error) {
	return c.Build(ctx, opt, "", builder.Build, statusChan)
}

func (f *clientFrontend) SolveGateway(ctx context.Context, c gateway.Client, req gateway.SolveRequest) (*gateway.Result, error) {
	if req.Frontend == "" && req.Definition == nil {
		req.Frontend = "dockerfile.v0"
	}
	return c.Solve(ctx, req)
}

func (f *clientFrontend) DFCmdArgs(ctx, dockerfile string) (string, string) {
	return "", ""
}

func (f *clientFrontend) RequiresBuildctl(t *testing.T) {
	t.Skip()
}

type gatewayFrontend struct {
	gw string
}

var _ frontend = &gatewayFrontend{}

func (f *gatewayFrontend) Solve(ctx context.Context, c *client.Client, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error) {
	opt.Frontend = "gateway.v0"
	if opt.FrontendAttrs == nil {
		opt.FrontendAttrs = make(map[string]string)
	}
	opt.FrontendAttrs["source"] = f.gw
	return c.Solve(ctx, nil, opt, statusChan)
}

func (f *gatewayFrontend) SolveGateway(ctx context.Context, c gateway.Client, req gateway.SolveRequest) (*gateway.Result, error) {
	req.Frontend = "gateway.v0"
	if req.FrontendOpt == nil {
		req.FrontendOpt = make(map[string]string)
	}
	req.FrontendOpt["source"] = f.gw
	return c.Solve(ctx, req)
}

func (f *gatewayFrontend) DFCmdArgs(ctx, dockerfile string) (string, string) {
	return dfCmdArgs(ctx, dockerfile, "--frontend gateway.v0 --opt=source="+f.gw)
}

func (f *gatewayFrontend) RequiresBuildctl(t *testing.T) {}

func getFrontend(t *testing.T, sb integration.Sandbox) frontend {
	v := sb.Value("frontend")
	require.NotNil(t, v)
	fn, ok := v.(frontend)
	require.True(t, ok)
	return fn
}

type secModeSandbox struct{}

func (*secModeSandbox) UpdateConfigFile(in string) (string, func() error) {
	return in, nil
}

type secModeInsecure struct{}

func (*secModeInsecure) UpdateConfigFile(in string) (string, func() error) {
	return in + "\n\ninsecure-entitlements = [\"security.insecure\"]\n", nil
}

var (
	securityInsecureGranted integration.ConfigUpdater = &secModeInsecure{}
	securityInsecureDenied  integration.ConfigUpdater = &secModeSandbox{}
)

type networkModeHost struct{}

func (*networkModeHost) UpdateConfigFile(in string) (string, func() error) {
	return in + "\n\ninsecure-entitlements = [\"network.host\"]\n", nil
}

type networkModeSandbox struct{}

func (*networkModeSandbox) UpdateConfigFile(in string) (string, func() error) {
	return in, nil
}

var (
	networkHostGranted integration.ConfigUpdater = &networkModeHost{}
	networkHostDenied  integration.ConfigUpdater = &networkModeSandbox{}
)

func fixedWriteCloser(wc io.WriteCloser) filesync.FileOutputFunc {
	return func(map[string]string) (io.WriteCloser, error) {
		return wc, nil
	}
}
