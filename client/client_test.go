package client

import (
	"testing"

	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
)

func init() {
	if workers.IsTestDockerd() {
		workers.InitDockerdWorker()
	} else {
		workers.InitOCIWorker()
		workers.InitContainerdWorker()
	}
}

var allTests = []func(t *testing.T, sb integration.Sandbox){
	// client_cache_test.go
	testBasicAzblobCacheImportExport,
	testBasicInlineCacheImportExport,
	testBasicLocalCacheImportExport,
	testBasicRegistryCacheImportExport,
	testBasicS3CacheImportExport,
	testCacheExportCacheDeletedContent,
	testCacheExportCacheKeyLoop,
	testCacheExportIgnoreError,
	testImageManifestRegistryCacheImportExport,
	testLocalCacheExportReset,
	testMultipleCacheExports,
	testMultipleRecordsWithSameLayersCacheImportExport,
	testMultipleRegistryCacheImportExport,
	testRegistryEmptyCacheExport,
	testSnapshotWithMultipleBlobs,
	testUncompressedLocalCacheImportExport,
	testUncompressedRegistryCacheImportExport,
	testZstdLocalCacheExport,
	testZstdLocalCacheImportExport,
	testZstdRegistryCacheImportExport,
	testStargzLazyInlineCacheImportExport,
	testStargzLazyRegistryCacheImportExport,

	// client_control_test.go
	testCallInfo,
	testClientCustomGRPCOpts,
	testListenBuildHistoryExcludesSoftDeletedRecords,

	// client_diskusage_test.go
	testCallDiskUsage,

	// client_exec_test.go
	testCgroupParent,
	testLinuxResources,
	testPassthroughOp,
	testRelativeMountpoint,
	testRelativeWorkDir,
	testRunValidExitCodes,
	testShmSize,
	testStdinClosed,
	testUlimit,
	testUser,

	// client_export_image_test.go
	testBuildExportScratch,
	testBuildExportWithForeignLayer,
	testBuildExportWithUncompressed,
	testBuildExportZstd,
	testBuildPushAndValidate,
	testExportBusyboxLocal,
	testExportedImageLabels,
	testImageResolveConfigDefaultLocalFallback,
	testInvalidExporter,
	testLazyImagePush,
	testOCIExporter,
	testOCIExporterContentStore,
	testPullWithDigestCheck,
	testPullZstdImage,
	testPushByDigest,
	testPushProgressSameVertex,
	testStargzLazyPull,

	// client_export_local_test.go
	testExportLocalForcePlatformSplit,
	testExportLocalModeCopyKeepsStaleDestinationFiles,
	testExportLocalModeCopyMultiPlatformKeepsAllPlatforms,
	testExportLocalModeDeleteRemovesStaleDestinationFiles,
	testExportLocalModeDeleteMultiPlatformKeepsAllPlatforms,
	testExportLocalModeInvalid,
	testExportLocalNoPlatformSplit,
	testExportLocalNoPlatformSplitOverwrite,
	testExporterTargetExists,
	testMultipleExporters,
	testSessionExporter,
	testTarExporterSymlink,
	testTarExporterWithSocket,
	testTarExporterWithSocketCopy,

	// client_export_metadata_test.go
	testAttestationBundle,
	testAttestationDefaultSubject,
	testExportAnnotations,
	testExportAnnotationsMediaTypes,
	testExportAttestationsDefaultOCIArtifact,
	testExportAttestationsImageManifest,
	testExportAttestationsOCIArtifact,
	testImageResolveAttestationChainLocal,
	testImageResolveAttestationChainRequiresNetwork,
	testImageResolveProvenanceAttestation,
	testSBOMScan,
	testSBOMScanSingleRef,
	testSBOMSupplements,
	testSourceDateEpochClamp,
	testSourceDateEpochImageExporter,
	testSourceDateEpochLayerTimestamps,
	testSourceDateEpochLocalExporter,
	testSourceDateEpochReset,
	testSourceDateEpochTarExporter,

	// client_fileop_test.go
	testCopyFromEmptyImage,
	testDuplicateWhiteouts,
	testFileOpCopyAlwaysReplaceExistingDestPaths,
	testFileOpCopyChmodText,
	testFileOpCopyIncludeExclude,
	testFileOpCopyRm,
	testFileOpCopyUIDCache,
	testFileOpInputSwap,
	testFileOpMkdirMkfile,
	testFileOpRmWildcard,
	testFileOpSymlink,
	testMoveParentDir,
	testRmSymlink,
	testWhiteoutParentDir,

	// client_frontend_test.go
	testFrontendImageNaming,
	testFrontendLintSkipVerifyPlatforms,
	testFrontendMetadataReturn,
	testFrontendUseSolveResults,
	testFrontendVerifyPlatforms,

	// client_git_source_test.go
	testGitBundleRoundTrip,
	testGitBundleRoundTripRegistry,
	testGitResolveMutatedSource,
	testGitResolveSourceMetadata,

	// client_http_source_test.go
	testBuildHTTPSource,
	testBuildHTTPSourceAuthHeaderSecret,
	testBuildHTTPSourceEtagScope,
	testBuildHTTPSourceHeader,
	testBuildHTTPSourceHostTokenSecret,
	testBuildHTTPSourcePGPSignatureVerify,
	testBuildHTTPSourceUnauthorizedChecksumRace,
	testHTTPPruneAfterCacheKey,
	testHTTPPruneAfterResolveMeta,
	testHTTPResolveMetaReuse,
	testHTTPResolveMultiBuild,
	testHTTPResolveSourceMetadata,

	// client_image_source_test.go
	testClientGatewayCanceledCredentialsCallbackReturns,
	testPullWithLayerLimit,
	testValidateDigestOrigin,

	// client_local_source_test.go
	testLocalSourceDiffer,
	testLocalSourceWithHardlinksFilter,
	testLocalSymlinkEscape,
	testMetadataOnlyLocal,
	testParallelLocalBuilds,

	// client_mergeop_test.go
	testMergeOp,
	testMergeOpCacheInline,
	testMergeOpCacheMax,
	testMergeOpCacheMin,

	// client_mount_test.go
	testBuildMultiMount,
	testCachedMounts,
	testCacheMountNoCache,
	testDuplicateCacheMount,
	testLayerLimitOnMounts,
	testLLBMountPerformance,
	testLockedCacheMounts,
	testMountStubsDirectory,
	testMountStubsTimestamp,
	testMountWithNoSource,
	testRawSocketMount,
	testReadonlyRootFS,
	testRunCacheWithMounts,
	testSecretEnv,
	testSecretMounts,
	testSharedCacheMounts,
	testSharedCacheMountsNoScratch,
	testSSHMount,
	testTmpfsMounts,

	// client_network_test.go
	testBridgeNetworking,
	testExtraHosts,
	testHostnameLookup,
	testHostnameSpecifying,
	testNetworkMode,
	testProxyEnv,
	testResolveAndHosts,

	// client_oci_source_test.go
	testImageBlobSource,
	testNoTarOCIIndexMediaType,
	testOCIIndexMediatype,
	testOCILayoutBlobSource,
	testOCILayoutPlatformSource,
	testOCILayoutSource,

	// client_session_test.go
	testSessionHealthMonitorFailsBlockedSecretResponse,

	// client_source_map_test.go
	testSourceMap,
	testSourceMapFromRef,

	// client_warnings_test.go
	testWarnings,

	// policy_test.go
	testSourcePolicySession,
	testSourcePolicySessionDenyMessages,
	testSourceMetaPolicySession,
	testSourceMetaPolicySessionResolveAttestations,
	testSourcePolicyParallelSession,
	testSourcePolicySignedCommit,
	testSourcePolicySessionConvert,
	testSourcePolicySessionHTTPChecksumAssist,
	testSourcePolicy,
}

func TestIntegration(t *testing.T) {
	testIntegration(t, append(allTests, validationTests...)...)
}

func TestClientGatewayIntegration(t *testing.T) {
	integration.Run(t, integration.TestFuncs(
		// gateway_container_exec_test.go
		testClientGatewayContainerCancelExecTty,
		testClientGatewayContainerCancelOnRelease,
		testClientGatewayContainerCancelPID1Tty,
		testClientGatewayContainerExecPipe,
		testClientGatewayContainerExecPipeRelease,
		testClientGatewayContainerExecPipeSignalKill,
		testClientGatewayContainerExecTty,
		testClientGatewayContainerPID1Exit,
		testClientGatewayContainerPID1Fail,
		testClientGatewayContainerPID1Tty,
		testClientGatewayContainerSignal,
		testClientGatewayExecError,
		testClientGatewayExecFileActionError,
		testClientGatewaySlowCacheExecError,

		// gateway_container_mount_test.go
		testClientGatewayContainerMounts,
		testClientGatewayContainerPlatformPATH,
		testClientGatewayContainerSecretEnv,

		// gateway_container_network_test.go
		testClientGatewayContainerExtraHosts,

		// gateway_container_security_test.go
		testClientGatewayContainerInvalidSecurityMode,

		// gateway_solve_test.go
		testClientGatewayEmptyImageExec,
		testClientGatewayEmptySolve,
		testClientGatewayFailedSolve,
		testClientGatewayNilResult,
		testClientGatewaySolve,
		testClientSlowCacheRootfsRef,
		testNoBuildID,
		testUnknownBuildID,
	), integration.WithMirroredImages(integration.OfficialImages("busybox:latest")))

	integration.Run(t, integration.TestFuncs(
		// gateway_container_security_test.go
		testClientGatewayContainerSecurityModeCaps,
		testClientGatewayContainerSecurityModeValidation,
	), integration.WithMirroredImages(integration.OfficialImages("busybox:latest")),
		integration.WithMatrix("secmode", map[string]any{
			"sandbox":  securitySandbox,
			"insecure": securityInsecure,
		}),
	)

	integration.Run(t, integration.TestFuncs(
		// gateway_container_network_test.go
		testClientGatewayContainerHostNetworkingAccess,
		testClientGatewayContainerHostNetworkingValidation,
	),
		integration.WithMirroredImages(integration.OfficialImages("busybox:latest")),
		integration.WithMatrix("netmode", map[string]any{
			"default": defaultNetwork,
			"host":    hostNetwork,
		}),
	)
}

func testIntegration(t *testing.T, funcs ...func(t *testing.T, sb integration.Sandbox)) {
	mirroredImagesUnix := integration.OfficialImages("busybox:latest", "alpine:latest")
	mirroredImagesUnix["tonistiigi/test:nolayers"] = "docker.io/tonistiigi/test:nolayers"
	mirroredImagesUnix["cpuguy83/buildkit-foreign:latest"] = "docker.io/cpuguy83/buildkit-foreign:latest"
	mirroredImagesWin := integration.OfficialImages("nanoserver:latest", "nanoserver:plus")

	mirroredImages := integration.UnixOrWindows(mirroredImagesUnix, mirroredImagesWin)
	mirrors := integration.WithMirroredImages(mirroredImages)

	tests := integration.TestFuncs(funcs...)
	tests = append(tests, diffOpTestCases()...)
	integration.Run(t, tests, mirrors)

	// the rest of the tests are meant for non-Windows, skipping on Windows.
	integration.SkipOnPlatform(t, "windows")

	integration.Run(t, integration.TestFuncs(
		// client_exec_test.go
		testSecurityMode,
		testSecurityModeSysfs,
		testSecurityModeErrors,
	),
		mirrors,
		integration.WithMatrix("secmode", map[string]any{
			"sandbox":  securitySandbox,
			"insecure": securityInsecure,
		}),
	)

	integration.Run(t, integration.TestFuncs(
		// client_network_test.go
		testHostNetworking,
	),
		mirrors,
		integration.WithMatrix("netmode", map[string]any{
			"default": defaultNetwork,
			"host":    hostNetwork,
		}),
	)

	integration.Run(t, integration.TestFuncs(
		// policy_test.go
		testProxyNetworkNoRootless,
		testProxyNetworkModesNoRootless,
		testProxyNetworkDefaultEgressNoRootless,
	),
		mirrors,
		integration.WithMatrix("netmode", map[string]any{
			"default":        proxyDefaultNetwork,
			"host":           proxyHostNetwork,
			"bridge":         proxyBridgeNetwork,
			"default-no-cni": proxyDefaultNetworkNoCNI,
		}),
	)

	integration.Run(
		t,
		integration.TestFuncs(
			// client_network_test.go
			testBridgeNetworkingDNSNoRootless,
		),
		mirrors,
		integration.WithMatrix("netmode", map[string]any{
			"dns": bridgeDNSNetwork,
		}),
	)

	// client_cdi_test.go
	integration.Run(t, integration.TestFuncs(cdiTests...), mirrors)
}
