package client

import (
	"context"
	"embed"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	solvererrdefs "github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/llbsolver/compat"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	policyimage "github.com/moby/policy-helpers/image"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/stretchr/testify/require"
)

const compatibilityUpdateEnv = "BUILDKIT_UPDATE_COMPAT_GOLDENS"
const compatibilityExpectedVersionEnv = "BUILDKIT_TEST_EXPECTED_COMPATIBILITY_VERSION"
const compatibilityEpoch = "1445412480"
const compatibilityPlatformString = "linux/arm64"
const compatibilityBusyboxMirrorRef = "busybox_arm64:latest"
const compatibilityBusyboxImageRef = "docker.io/library/" + compatibilityBusyboxMirrorRef

//go:embed testdata/compatibility
var compatibilityGoldens embed.FS

type compatibilityLayerExpectation struct {
	MediaType string
	Digest    string
}

type compatibilityCase struct {
	Name             string
	Attrs            map[string]string
	TouchExecOutputs bool
}

type compatibilityActual struct {
	ManifestDigest string
	ConfigDigest   string
	Layers         []compatibilityLayerExpectation
	ManifestBytes  []byte
	ConfigBytes    []byte
	ManifestJSON   string
	ConfigJSON     string
}

var compatibilityCases = []compatibilityCase{
	{
		Name: "default-gzip",
		Attrs: map[string]string{
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
	{
		Name: "gzip-level-0",
		Attrs: map[string]string{
			"compression":       "gzip",
			"compression-level": "0",
			"force-compression": "true",
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
	{
		Name: "gzip-level-9",
		Attrs: map[string]string{
			"compression":       "gzip",
			"compression-level": "9",
			"force-compression": "true",
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
	{
		Name: "uncompressed",
		Attrs: map[string]string{
			"compression":       "uncompressed",
			"force-compression": "true",
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
	{
		Name: "zstd-oci-types",
		Attrs: map[string]string{
			"compression":       "zstd",
			"compression-level": "12",
			"force-compression": "true",
			"oci-mediatypes":    "true",
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
	{
		Name: "manifest-annotation",
		Attrs: map[string]string{
			"annotation-manifest.org.opencontainers.image.title": "compatibility-test",
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
	{
		Name: "oci-mediatypes",
		Attrs: map[string]string{
			"oci-mediatypes":    "true",
			"force-compression": "true",
			"source-date-epoch": compatibilityEpoch,
			"rewrite-timestamp": "true",
		},
	},
}

func TestCompatibilityIntegration(t *testing.T) {
	integration.SkipOnPlatform(t, "windows")
	mirrors := integration.WithMirroredImages(map[string]string{
		"library/" + compatibilityBusyboxMirrorRef: "docker.io/arm64v8/busybox:latest@sha256:1fa89c01cd0473cedbd1a470abb8c139eeb80920edf1bc55de87851bfb63ea11",
	})
	integration.Run(t, integration.TestFuncs(
		testImageExporterCompatibilityVersion,
		testOCIExporterCompatibilityVersion,
		testImageExporterCompatibilityVersionProvenance,
	), mirrors)
}

func testImageExporterCompatibilityVersion(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureProvenance, workers.FeatureSourceDateEpoch)
	requiresLinux(t)

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	platform := compatibilityPlatform(t)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	baseRef, baseConfig := createCompatibilityBaseImage(ctx, t, c, registry, platform)

	expectedSet := compatibilityExpectedVersionIsSet()
	for _, version := range compatibilityVersionsUnderTest(t) {
		ensurePruneAll(t, c, sb)
		for _, tc := range compatibilityCases {
			t.Run(fmt.Sprintf("image/%s/%s", tc.Name, compatibilityRunName(version, expectedSet)), func(t *testing.T) {
				def := createCompatibilityDefinition(t, sb, baseRef, platform, tc)
				actual, err := exportCompatibilityImageCase(ctx, t, c, def, registry, tc, version, expectedSet, baseConfig)
				assertCompatibilityCaseResult(t, ExporterImage, tc, version, expectedSet, actual, err)
			})
		}
	}
}

func testOCIExporterCompatibilityVersion(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureProvenance, workers.FeatureSourceDateEpoch)
	requiresLinux(t)

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	platform := compatibilityPlatform(t)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	baseRef, baseConfig := createCompatibilityBaseImage(ctx, t, c, registry, platform)

	expectedSet := compatibilityExpectedVersionIsSet()
	for _, version := range compatibilityVersionsUnderTest(t) {
		ensurePruneAll(t, c, sb)
		for _, tc := range compatibilityCases {
			t.Run(fmt.Sprintf("oci/%s/%s", tc.Name, compatibilityRunName(version, expectedSet)), func(t *testing.T) {
				def := createCompatibilityDefinition(t, sb, baseRef, platform, tc)
				actual, err := exportCompatibilityOCICase(ctx, t, c, def, tc, version, expectedSet, baseConfig)
				assertCompatibilityCaseResult(t, ExporterOCI, tc, version, expectedSet, actual, err)
			})
		}
	}
}

func testImageExporterCompatibilityVersionProvenance(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureProvenance, workers.FeatureSourceDateEpoch)
	requiresLinux(t)

	if compatibilityExpectedVersionIsSet() {
		t.Skip("provenance compatibility assertion requires explicit compatibility-version request")
	}

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	platform := compatibilityPlatform(t)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	baseRef, baseConfig := createCompatibilityBaseImage(ctx, t, c, registry, platform)
	ensurePruneAll(t, c, sb)

	var tc compatibilityCase
	for _, candidate := range compatibilityCases {
		if candidate.Name == "default-gzip" {
			tc = candidate
			break
		}
	}
	require.NotEmpty(t, tc.Name)

	def := createCompatibilityDefinition(t, sb, baseRef, platform, tc)
	target := fmt.Sprintf("%s/buildkit/compatibility-provenance-v10:latest", registry)
	attrs := cloneStringMap(tc.Attrs)
	attrs["name"] = target
	attrs["push"] = "true"
	attrs[exptypes.ExporterImageConfigKey] = string(baseConfig)

	_, err = c.Solve(ctx, def, SolveOpt{
		CompatibilityVersion: compat.CompatibilityVersion013,
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1",
		},
		Exports: []ExportEntry{{
			Type:  ExporterImage,
			Attrs: attrs,
		}},
	}, nil)
	require.NoError(t, err)

	pr, err := readImageCompatibilityProvenance(ctx, target)
	require.NoError(t, err)
	require.Equal(t, compat.CompatibilityVersion013, pr.BuildDefinition.ExternalParameters.Request.CompatibilityVersion)
}

func compatibilityVersionsUnderTest(t *testing.T) []int {
	t.Helper()

	versionStr := os.Getenv(compatibilityExpectedVersionEnv)
	if versionStr == "" {
		return compat.SupportedCompatibilityVersions()
	}

	version, err := strconv.Atoi(versionStr)
	require.NoError(t, err)
	require.NoError(t, compat.ValidateCompatibilityVersion(version))
	return []int{version}
}

func compatibilityExpectedVersionIsSet() bool {
	return os.Getenv(compatibilityExpectedVersionEnv) != ""
}

func compatibilityRunName(version int, expectedSet bool) string {
	if expectedSet {
		return "default"
	}
	return "v" + strconv.Itoa(version)
}

func createCompatibilityBaseImage(ctx context.Context, t *testing.T, c *Client, registry string, platform ocispecs.Platform) (string, []byte) {
	t.Helper()

	baseRef := fmt.Sprintf("%s/buildkit/compatibility-base:latest", registry)
	_, err := c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name":  baseRef,
				"push":  "true",
				"store": "false",
			},
		}},
	}, "", func(ctx context.Context, gw gateway.Client) (*gateway.Result, error) {
		baseConfig, err := resolveCompatibilityImageConfig(ctx, gw, platform)
		if err != nil {
			return nil, err
		}
		base, err := llb.Image(compatibilityBusyboxImageRef, llb.Platform(platform)).WithImageConfig(baseConfig)
		if err != nil {
			return nil, err
		}
		base = base.File(
			llb.Mkdir("/base", 0o755, llb.WithParents(true)).
				Mkfile("/base/base.txt", 0o644, []byte("compatibility base\n")),
		)

		def, err := base.Marshal(ctx)
		if err != nil {
			return nil, err
		}

		r, err := gw.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		ref, err := r.SingleRef()
		if err != nil {
			return nil, err
		}

		res := gateway.NewResult()
		res.SetRef(ref)
		res.AddMeta(exptypes.ExporterImageConfigKey, baseConfig)
		return res, nil
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(baseRef)
	require.NoError(t, err)
	actual := readCompatibilityActualFromProvider(ctx, t, provider, desc)

	return baseRef, actual.ConfigBytes
}

func resolveCompatibilityImageConfig(ctx context.Context, gw gateway.Client, platform ocispecs.Platform) ([]byte, error) {
	_, _, baseConfig, err := gw.ResolveImageConfig(ctx, compatibilityBusyboxImageRef, sourceresolver.Opt{
		ImageOpt: &sourceresolver.ResolveImageOpt{
			Platform: &platform,
		},
	})
	return baseConfig, err
}

func createCompatibilityDefinition(t *testing.T, sb integration.Sandbox, baseRef string, platform ocispecs.Platform, tc compatibilityCase) *llb.Definition {
	t.Helper()

	httpModTime := time.Date(2021, time.January, 2, 3, 4, 5, 0, time.UTC)
	httpSrv := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/artifact.txt": {
			Content:      []byte("http fixture\n"),
			LastModified: &httpModTime,
		},
	})
	t.Cleanup(httpSrv.Close)

	gitDir := t.TempDir()
	err := runInDirEnv(gitDir, []string{
		"GIT_AUTHOR_DATE=2020-01-02T03:04:05Z",
		"GIT_COMMITTER_DATE=2020-01-02T03:04:05Z",
	}, []string{
		"git init",
		"git config --local user.email test@example.com",
		"git config --local user.name test",
		"git checkout -b main",
		"mkdir -p sub",
		"printf 'git fixture\\n' > git.txt",
		"printf 'nested fixture\\n' > sub/nested.txt",
		"git add .",
		"git commit -m fixture",
		"git update-server-info",
	}...)
	require.NoError(t, err)

	gitSrv := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	t.Cleanup(gitSrv.Close)

	httpState := llb.HTTP(httpSrv.URL+"/artifact.txt", llb.Filename("artifact.txt"))
	gitState := llb.Git(gitSrv.URL+"/.git", "", llb.GitRef("main"))

	st := llb.Image(baseRef, llb.Platform(platform)).File(
		llb.Mkdir("/inputs/git", 0o755, llb.WithParents(true)).
			Mkfile("/inputs/llb.txt", 0o644, []byte("llb fixture\n")).
			Copy(httpState, "/artifact.txt", "/inputs/http.txt").
			Copy(gitState, "/git.txt", "/inputs/git/git.txt").
			Copy(gitState, "/sub/nested.txt", "/inputs/git/nested.txt"),
	)

	execScript := `sh -eux -c '
mkdir -p /foo/bar/generated/deeper
echo exec-fixture > /foo/bar/generated/exec.txt
echo nested-exec > /foo/bar/generated/deeper/nested.txt
`
	if tc.TouchExecOutputs {
		execScript += `touch -t 201903040506.07 /foo/bar/generated /foo/bar/generated/deeper
touch -t 201903040506.07 /foo/bar/generated/exec.txt /foo/bar/generated/deeper/nested.txt
`
	}
	execScript += `'`

	run := st.Run(
		llb.Shlex(execScript),
		llb.AddMount("/foo/bar", llb.Scratch()),
	)

	def, err := run.Root().Marshal(sb.Context())
	require.NoError(t, err)
	return def
}

func exportCompatibilityImageCase(ctx context.Context, t *testing.T, c *Client, def *llb.Definition, registry string, tc compatibilityCase, version int, expectedSet bool, baseConfig []byte) (compatibilityActual, error) {
	t.Helper()

	target := fmt.Sprintf("%s/buildkit/compatibility-%s:%s", registry, sanitizeCompatibilityName(tc.Name), compatibilityRunName(version, expectedSet))
	attrs := cloneStringMap(tc.Attrs)
	attrs["name"] = target
	attrs["push"] = "true"
	attrs[exptypes.ExporterImageConfigKey] = string(baseConfig)

	opt := SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1",
		},
		Exports: []ExportEntry{{
			Type:  ExporterImage,
			Attrs: attrs,
		}},
	}
	if !expectedSet {
		opt.CompatibilityVersion = version
	}
	_, err := c.Solve(ctx, def, opt, nil)
	if err != nil {
		return compatibilityActual{}, err
	}

	desc, provider, err := contentutil.ProviderFromRef(target)
	if err != nil {
		return compatibilityActual{}, err
	}
	return readCompatibilityActualFromProvider(ctx, t, provider, desc), nil
}

func exportCompatibilityOCICase(ctx context.Context, t *testing.T, c *Client, def *llb.Definition, tc compatibilityCase, version int, expectedSet bool, baseConfig []byte) (compatibilityActual, error) {
	t.Helper()

	dir := t.TempDir()
	attrs := cloneStringMap(tc.Attrs)
	attrs["tar"] = "false"
	attrs[exptypes.ExporterImageConfigKey] = string(baseConfig)

	opt := SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1",
		},
		Exports: []ExportEntry{{
			Type:      ExporterOCI,
			OutputDir: dir,
			Attrs:     attrs,
		}},
	}
	if !expectedSet {
		opt.CompatibilityVersion = version
	}
	_, err := c.Solve(ctx, def, opt, nil)
	if err != nil {
		return compatibilityActual{}, err
	}

	indexDT, err := os.ReadFile(filepath.Join(dir, ocispecs.ImageIndexFile))
	require.NoError(t, err)

	var index ocispecs.Index
	require.NoError(t, json.Unmarshal(indexDT, &index))

	for _, desc := range index.Manifests {
		if desc.Digest == "" {
			continue
		}
		if desc.Platform != nil && desc.Platform.OS == "unknown" && desc.Platform.Architecture == "unknown" {
			continue
		}
		manifestDT, err := os.ReadFile(filepath.Join(dir, ocispecs.ImageBlobsDir, desc.Digest.Algorithm().String(), desc.Digest.Encoded()))
		require.NoError(t, err)

		var manifest ocispecs.Manifest
		require.NoError(t, json.Unmarshal(manifestDT, &manifest))
		if manifest.Config.Digest == "" {
			continue
		}
		if isCompatibilityAttestationManifest(manifest) {
			continue
		}

		configDT, err := os.ReadFile(filepath.Join(dir, ocispecs.ImageBlobsDir, manifest.Config.Digest.Algorithm().String(), manifest.Config.Digest.Encoded()))
		require.NoError(t, err)

		return compatibilityActualFromBytes(manifestDT, configDT, manifest.Layers), nil
	}

	blobPaths, err := filepath.Glob(filepath.Join(dir, ocispecs.ImageBlobsDir, "*", "*"))
	require.NoError(t, err)
	sort.Strings(blobPaths)
	for _, blobPath := range blobPaths {
		manifestDT, err := os.ReadFile(blobPath)
		require.NoError(t, err)

		var manifest ocispecs.Manifest
		if err := json.Unmarshal(manifestDT, &manifest); err != nil {
			continue
		}
		if manifest.Config.Digest == "" {
			continue
		}
		if isCompatibilityAttestationManifest(manifest) {
			continue
		}

		configDT, err := os.ReadFile(filepath.Join(dir, ocispecs.ImageBlobsDir, manifest.Config.Digest.Algorithm().String(), manifest.Config.Digest.Encoded()))
		require.NoError(t, err)

		return compatibilityActualFromBytes(manifestDT, configDT, manifest.Layers), nil
	}

	t.Fatalf("missing platform manifest\nindex json:\n%s", normalizeJSON(indexDT))
	return compatibilityActual{}, nil
}

func compatibilityActualFromBytes(manifestDT, configDT []byte, layers []ocispecs.Descriptor) compatibilityActual {
	out := compatibilityActual{
		ManifestDigest: digest.FromBytes(manifestDT).String(),
		ConfigDigest:   digest.FromBytes(configDT).String(),
		ManifestBytes:  append([]byte(nil), manifestDT...),
		ConfigBytes:    append([]byte(nil), configDT...),
		ManifestJSON:   normalizeJSON(manifestDT),
		ConfigJSON:     normalizeJSON(configDT),
		Layers:         make([]compatibilityLayerExpectation, len(layers)),
	}
	for i, layer := range layers {
		out.Layers[i] = compatibilityLayerExpectation{
			MediaType: layer.MediaType,
			Digest:    layer.Digest.String(),
		}
	}
	return out
}

func isCompatibilityAttestationManifest(manifest ocispecs.Manifest) bool {
	if len(manifest.Layers) == 0 {
		return false
	}
	for _, layer := range manifest.Layers {
		if layer.MediaType != "application/vnd.in-toto+json" {
			return false
		}
	}
	return true
}

func readCompatibilityActualFromProvider(ctx context.Context, t *testing.T, provider content.Provider, desc ocispecs.Descriptor) compatibilityActual {
	t.Helper()

	if images.IsIndexType(desc.MediaType) {
		indexDT, err := content.ReadBlob(ctx, provider, desc)
		require.NoError(t, err)

		var index ocispecs.Index
		require.NoError(t, json.Unmarshal(indexDT, &index))

		platform := compatibilityPlatform(t)
		matcher := platforms.Only(platform)
		for _, manifestDesc := range index.Manifests {
			if manifestDesc.Platform == nil || !matcher.Match(*manifestDesc.Platform) {
				continue
			}
			return readCompatibilityActualFromProvider(ctx, t, provider, manifestDesc)
		}

		require.FailNow(t, "missing platform manifest in image index")
	}

	manifestDT, err := content.ReadBlob(ctx, provider, desc)
	require.NoError(t, err)

	var manifest ocispecs.Manifest
	require.NoError(t, json.Unmarshal(manifestDT, &manifest))
	require.True(t, images.IsManifestType(manifest.MediaType), "unexpected manifest media type %s", manifest.MediaType)

	configDT, err := content.ReadBlob(ctx, provider, manifest.Config)
	require.NoError(t, err)

	return compatibilityActualFromBytes(manifestDT, configDT, manifest.Layers)
}

func readImageCompatibilityProvenance(ctx context.Context, ref string) (*provenancetypes.ProvenancePredicateSLSA1, error) {
	desc, provider, err := contentutil.ProviderFromRef(ref)
	if err != nil {
		return nil, err
	}

	indexDT, err := content.ReadBlob(ctx, provider, desc)
	if err != nil {
		return nil, err
	}

	var index ocispecs.Index
	if err := json.Unmarshal(indexDT, &index); err != nil {
		return nil, err
	}

	for _, manifestDesc := range index.Manifests {
		manifestDT, err := content.ReadBlob(ctx, provider, manifestDesc)
		if err != nil {
			return nil, err
		}

		var manifest ocispecs.Manifest
		if err := json.Unmarshal(manifestDT, &manifest); err != nil {
			return nil, err
		}
		if !isCompatibilityAttestationManifest(manifest) {
			continue
		}

		for _, layer := range manifest.Layers {
			if layer.Annotations["in-toto.io/predicate-type"] != policyimage.SLSAProvenancePredicateType1 {
				continue
			}

			stmtDT, err := content.ReadBlob(ctx, provider, layer)
			if err != nil {
				return nil, err
			}

			var stmt struct {
				intoto.StatementHeader
				Predicate json.RawMessage `json:"predicate"`
			}
			if err := json.Unmarshal(stmtDT, &stmt); err != nil {
				return nil, err
			}

			var pr provenancetypes.ProvenancePredicateSLSA1
			if err := json.Unmarshal(stmt.Predicate, &pr); err != nil {
				return nil, err
			}
			return &pr, nil
		}
	}

	return nil, errors.New("missing SLSA provenance attestation")
}

func assertCompatibilityCase(t *testing.T, exporterType string, tc compatibilityCase, version int, actual compatibilityActual) {
	t.Helper()

	if os.Getenv(compatibilityUpdateEnv) != "" {
		writeCompatibilityGoldens(t, tc.Name, version, actual)
		t.Logf("compatibility expectation %s/%s/v%d manifest=%q config=%q layers=%s",
			exporterType, tc.Name, version, actual.ManifestDigest, actual.ConfigDigest, formatLayerExpectations(actual.Layers))
		return
	}

	expectedManifestJSON, err := compatibilityGoldens.ReadFile(goldenPath(tc.Name, version, "manifest.json"))
	require.NoError(t, err)
	expectedConfigJSON, err := compatibilityGoldens.ReadFile(goldenPath(tc.Name, version, "config.json"))
	require.NoError(t, err)

	exp, err := compatibilityExpectationFromGoldens(expectedManifestJSON, expectedConfigJSON)
	require.NoError(t, err)

	manifestDiff := ""
	if normalizeJSON(expectedManifestJSON) != actual.ManifestJSON {
		manifestDiff = unifiedDiff("golden-manifest", normalizeJSON(expectedManifestJSON), actual.ManifestJSON)
	}
	configDiff := ""
	if normalizeJSON(expectedConfigJSON) != actual.ConfigJSON {
		configDiff = unifiedDiff("golden-config", normalizeJSON(expectedConfigJSON), actual.ConfigJSON)
	}

	if exp.ManifestDigest != actual.ManifestDigest ||
		exp.ConfigDigest != actual.ConfigDigest ||
		!equalLayerExpectations(exp.Layers, actual.Layers) ||
		manifestDiff != "" ||
		configDiff != "" {
		t.Fatalf("%s", formatCompatibilityDebug(exporterType, tc.Name, version, tc.Attrs, exp, actual, manifestDiff, configDiff))
	}
}

func assertCompatibilityCaseResult(t *testing.T, exporterType string, tc compatibilityCase, version int, expectedSet bool, actual compatibilityActual, err error) {
	t.Helper()

	if compatibilityCaseExpectedUnsupported(tc, version, expectedSet) {
		require.Error(t, err)
		err = grpcerrors.FromGRPC(err)
		var unsupported *solvererrdefs.UnsupportedCompatibilityFeatureError
		expectedFeature := fmt.Sprintf("%s exporter compression=zstd", exporterType)
		if stderrors.As(err, &unsupported) {
			require.Equal(t, int64(version), unsupported.Version)
			require.Equal(t, expectedFeature, unsupported.Feature)
			return
		}
		require.ErrorContains(t, err, fmt.Sprintf("unsupported compatibility-version %d feature %s", version, expectedFeature))
		return
	}

	require.NoError(t, err)
	assertCompatibilityCase(t, exporterType, tc, version, actual)
}

func compatibilityCaseExpectedUnsupported(tc compatibilityCase, version int, expectedSet bool) bool {
	return !expectedSet && version == compat.CompatibilityVersion013 && tc.Attrs["compression"] == "zstd"
}

func compatibilityExpectationFromGoldens(manifestDT, configDT []byte) (compatibilityActual, error) {
	var manifest ocispecs.Manifest
	if err := json.Unmarshal(manifestDT, &manifest); err != nil {
		return compatibilityActual{}, err
	}
	return compatibilityActualFromBytes(manifestDT, configDT, manifest.Layers), nil
}

func writeCompatibilityGoldens(t *testing.T, caseName string, version int, actual compatibilityActual) {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	baseDir := filepath.Dir(currentFile)
	manifestPath := filepath.Join(baseDir, "testdata", "compatibility", caseName, fmt.Sprintf("v%d", version), "manifest.json")
	configPath := filepath.Join(baseDir, "testdata", "compatibility", caseName, fmt.Sprintf("v%d", version), "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(manifestPath), 0o755))
	require.NoError(t, os.WriteFile(manifestPath, actual.ManifestBytes, 0o644))
	require.NoError(t, os.WriteFile(configPath, actual.ConfigBytes, 0o644))
}

func goldenPath(caseName string, version int, file string) string {
	return filepath.ToSlash(filepath.Join("testdata", "compatibility", caseName, fmt.Sprintf("v%d", version), file))
}

func formatCompatibilityDebug(exporterType, caseName string, version int, attrs map[string]string, exp compatibilityActual, actual compatibilityActual, manifestDiff, configDiff string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "compatibility regression mismatch\n")
	fmt.Fprintf(&b, "case: %s\n", caseName)
	fmt.Fprintf(&b, "exporter: %s\n", exporterType)
	fmt.Fprintf(&b, "compatibility-version: %d\n", version)
	fmt.Fprintf(&b, "attrs:\n")

	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s=%s\n", k, attrs[k])
	}

	fmt.Fprintf(&b, "\nexpected:\n")
	fmt.Fprintf(&b, "  manifest=%s\n", exp.ManifestDigest)
	fmt.Fprintf(&b, "  config=%s\n", exp.ConfigDigest)
	for i, layer := range exp.Layers {
		fmt.Fprintf(&b, "  layer[%d]=%s %s\n", i, layer.MediaType, layer.Digest)
	}

	fmt.Fprintf(&b, "\nactual:\n")
	fmt.Fprintf(&b, "  manifest=%s\n", actual.ManifestDigest)
	fmt.Fprintf(&b, "  config=%s\n", actual.ConfigDigest)
	for i, layer := range actual.Layers {
		fmt.Fprintf(&b, "  layer[%d]=%s %s\n", i, layer.MediaType, layer.Digest)
	}

	if manifestDiff != "" {
		fmt.Fprintf(&b, "\nmanifest diff:\n%s\n", manifestDiff)
	}
	if configDiff != "" {
		fmt.Fprintf(&b, "\nconfig diff:\n%s\n", configDiff)
	}

	fmt.Fprintf(&b, "\nmanifest json:\n%s\n", actual.ManifestJSON)
	fmt.Fprintf(&b, "\nconfig json:\n%s\n", actual.ConfigJSON)
	return b.String()
}

func unifiedDiff(name, expected, actual string) string {
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(expected),
		B:        difflib.SplitLines(actual),
		FromFile: name + ".golden",
		ToFile:   name + ".actual",
		Context:  3,
	})
	if err != nil {
		return fmt.Sprintf("failed to render diff: %v", err)
	}
	return diff
}

func normalizeJSON(dt []byte) string {
	var v any
	if err := json.Unmarshal(dt, &v); err != nil {
		return string(dt)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(dt)
	}
	return string(out)
}

func equalLayerExpectations(a, b []compatibilityLayerExpectation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatLayerExpectations(layers []compatibilityLayerExpectation) string {
	parts := make([]string, len(layers))
	for i, layer := range layers {
		parts[i] = fmt.Sprintf("{MediaType:%q,Digest:%q}", layer.MediaType, layer.Digest)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func sanitizeCompatibilityName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func compatibilityPlatform(t *testing.T) ocispecs.Platform {
	t.Helper()
	return platforms.Normalize(platforms.MustParse(compatibilityPlatformString))
}
