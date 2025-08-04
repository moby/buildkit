package dockerfile

import (
	"encoding/json"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testSBOMScannerImage,
		testSBOMScannerArgs,
	)...)
}

func testSBOMScannerImage(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureSBOM)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox:latest
COPY <<-"EOF" /scan.sh
	set -e
	cat <<BUNDLE > $BUILDKIT_SCAN_DESTINATION/spdx.json
	{
	  "_type": "https://in-toto.io/Statement/v0.1",
	  "predicateType": "https://spdx.dev/Document",
	  "predicate": {"name": "sbom-scan"}
	}
	BUNDLE
EOF
CMD sh /scan.sh
`)
	scannerDir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	scannerTarget := registry + "/buildkit/testsbomscanner:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: scannerDir,
			dockerui.DefaultLocalNameContext:    scannerDir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": scannerTarget,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = []byte(`
FROM scratch
COPY <<EOF /foo
data
EOF
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target := registry + "/buildkit/testsbomscannertarget:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:sbom": "generator=" + scannerTarget,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	img := imgs.Find(platforms.Format(platforms.Normalize(platforms.DefaultSpec())))
	require.NotNil(t, img)
	require.Equal(t, []byte("data\n"), img.Layers[0]["foo"].Data)

	att := imgs.Find("unknown/unknown")
	require.Equal(t, 1, len(att.LayersRaw))
	var attest intoto.Statement
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{"name": "sbom-scan"})
}

func testSBOMScannerArgs(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureSBOM)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox:latest
COPY <<-"EOF" /scan.sh
	set -e
	cat <<BUNDLE > $BUILDKIT_SCAN_DESTINATION/spdx.json
	{
	  "_type": "https://in-toto.io/Statement/v0.1",
	  "predicateType": "https://spdx.dev/Document",
	  "predicate": {"name": "core"}
	}
	BUNDLE
	if [ "${BUILDKIT_SCAN_SOURCE_EXTRAS}" ]; then
		for src in "${BUILDKIT_SCAN_SOURCE_EXTRAS}"/*; do
			cat <<BUNDLE > $BUILDKIT_SCAN_DESTINATION/$(basename $src).spdx.json
			{
			  "_type": "https://in-toto.io/Statement/v0.1",
			  "predicateType": "https://spdx.dev/Document",
			  "predicate": {"name": "extra"}
			}
			BUNDLE
		done
	fi
EOF
CMD sh /scan.sh
`)

	scannerDir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	scannerTarget := registry + "/buildkit/testsbomscannerargs:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: scannerDir,
			dockerui.DefaultLocalNameContext:    scannerDir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": scannerTarget,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// scan an image with no additional sboms
	dockerfile = []byte(`
FROM scratch as base
COPY <<EOF /foo
data
EOF
FROM base
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target := registry + "/buildkit/testsbomscannerargstarget1:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:sbom":                          "generator=" + scannerTarget,
			"build-arg:BUILDKIT_SBOM_SCAN_CONTEXT": "true",
			"build-arg:BUILDKIT_SBOM_SCAN_STAGE":   "true",
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	img := imgs.Find(platforms.Format(platforms.Normalize(platforms.DefaultSpec())))
	require.NotNil(t, img)

	att := imgs.Find("unknown/unknown")
	require.Equal(t, 1, len(att.LayersRaw))
	var attest intoto.Statement
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Subset(t, attest.Predicate, map[string]any{"name": "core"})

	dockerfile = []byte(`
ARG BUILDKIT_SBOM_SCAN_CONTEXT=true

FROM scratch as file
ARG BUILDKIT_SBOM_SCAN_STAGE=true
COPY <<EOF /file
data
EOF

FROM scratch as base
ARG BUILDKIT_SBOM_SCAN_STAGE=true
COPY --from=file /file /foo

FROM scratch as base2
ARG BUILDKIT_SBOM_SCAN_STAGE=true
COPY --from=file /file /bar
RUN non-existent-command-would-fail

FROM base
ARG BUILDKIT_SBOM_SCAN_STAGE=true
`)
	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	// scan an image with additional sboms
	target = registry + "/buildkit/testsbomscannertarget2:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:sbom": "generator=" + scannerTarget,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	img = imgs.Find(platforms.Format(platforms.Normalize(platforms.DefaultSpec())))
	require.NotNil(t, img)

	att = imgs.Find("unknown/unknown")
	require.Equal(t, 4, len(att.LayersRaw))
	extraCount := 0
	for _, l := range att.LayersRaw {
		var attest intoto.Statement
		require.NoError(t, json.Unmarshal(l, &attest))
		att := attest.Predicate.(map[string]any)
		switch att["name"] {
		case "core":
		case "extra":
			extraCount++
		default:
			require.Fail(t, "unexpected attestation", "%v", att)
		}
	}
	require.Equal(t, extraCount, len(att.LayersRaw)-1)

	// scan an image with additional sboms, but disable them
	target = registry + "/buildkit/testsbomscannertarget3:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:sbom":                          "generator=" + scannerTarget,
			"build-arg:BUILDKIT_SBOM_SCAN_STAGE":   "false",
			"build-arg:BUILDKIT_SBOM_SCAN_CONTEXT": "false",
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	img = imgs.Find(platforms.Format(platforms.Normalize(platforms.DefaultSpec())))
	require.NotNil(t, img)

	att = imgs.Find("unknown/unknown")
	require.Equal(t, 1, len(att.LayersRaw))
}
