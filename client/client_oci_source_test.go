package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/platforms"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/purl"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	packageurl "github.com/package-url/packageurl-go"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testImageBlobSource(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	st := llb.Image("alpine")

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	name := registry + "/foo/blobtest:img"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name": name,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(name)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.Equal(t, 1, len(imgs.Images))
	mfst := imgs.Images[0].Manifest
	require.GreaterOrEqual(t, len(mfst.Layers), 1)

	l := mfst.Layers[0]

	blob := llb.ImageBlob(registry+"/foo/blobtest@"+l.Digest.String(), llb.Filename("layer.tar.gz"), llb.Chown(123, 456))
	st = llb.Image("alpine").Run(llb.Shlex(`sh -c 'sha256sum /layers/layer.tar.gz | cut -d" " -f0 > /out/checksum && stat -c "%u-%g-%s" /layers/layer.tar.gz > /out/stat'`), llb.AddMount("/layers", blob, llb.Readonly)).AddMount("/out", llb.Scratch())

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "",
		},
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "stat"))
	require.NoError(t, err)

	require.Equal(t, "123-456-"+strconv.FormatInt(l.Size, 10), strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "checksum"))
	require.NoError(t, err)

	require.Equal(t, l.Digest.Hex(), strings.TrimSpace(string(dt)))

	provDt, err := os.ReadFile(filepath.Join(destDir, "provenance.json"))
	require.NoError(t, err)

	var stmt struct {
		intoto.StatementHeader
		Predicate provenancetypes.ProvenancePredicateSLSA1 `json:"predicate"`
	}
	require.NoError(t, json.Unmarshal(provDt, &stmt))

	expectedName, err := purl.RefToPURL(packageurl.TypeDocker, registry+"/foo/blobtest@"+l.Digest.String(), nil)
	require.NoError(t, err)
	purlObj, err := packageurl.FromString(expectedName)
	require.NoError(t, err)
	purlObj.Qualifiers = append(purlObj.Qualifiers, packageurl.Qualifier{Key: "ref_type", Value: "blob"})
	expectedName = purlObj.ToString()

	found := false
	for _, m := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		if m.URI == expectedName {
			found = true
			require.Equal(t, l.Digest.Hex(), m.Digest["sha256"])
			break
		}
	}
	require.True(t, found, "expected to find %q in %+v", expectedName, stmt.Predicate.BuildDefinition.ResolvedDependencies)
}

func testNoTarOCIIndexMediaType(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	cmdStr := integration.UnixOrWindows(
		`sh -c "echo -n hello > hello"`,
		`cmd /C "echo hello> hello"`,
	)
	st := llb.Image(imgName).Run(llb.Shlex(cmdStr))
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	outDir := filepath.Join(destDir, "out.d")
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterOCI,
				Attrs: map[string]string{
					"tar": "false",
				},
				OutputDir: outDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(dt, &index)
	require.NoError(t, err)

	require.Equal(t, "application/vnd.oci.image.index.v1+json", index.MediaType)

	checkAllReleasable(t, c, sb, true)
}

func testOCIIndexMediatype(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	cmdStr := integration.UnixOrWindows(
		`sh -c "echo -n hello > hello"`,
		`cmd /C "echo hello> hello"`,
	)
	st := llb.Image(imgName).Run(llb.Shlex(cmdStr))
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

	indexDt, ok := m[ocispecs.ImageIndexFile]
	require.True(t, ok)

	var index ocispecs.Index
	err = json.Unmarshal(indexDt.Data, &index)
	require.NoError(t, err)

	require.Equal(t, "application/vnd.oci.image.index.v1+json", index.MediaType)

	checkAllReleasable(t, c, sb, true)
}

func testOCILayoutBlobSource(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("alpine")
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	ociDir := t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterOCI,
				Attrs: map[string]string{
					"tar": "false",
				},
				OutputDir: ociDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	indexDt, err := os.ReadFile(filepath.Join(ociDir, ocispecs.ImageIndexFile))
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(indexDt, &index)
	require.NoError(t, err)
	require.Equal(t, 1, len(index.Manifests))

	var mfst ocispecs.Manifest
	mfstDt, err := os.ReadFile(filepath.Join(ociDir, "blobs/sha256", index.Manifests[0].Digest.Hex()))
	require.NoError(t, err)
	err = json.Unmarshal(mfstDt, &mfst)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mfst.Layers), 1)
	layer := mfst.Layers[0]

	store, err := local.NewStore(ociDir)
	require.NoError(t, err)
	csID := "my-blob-content-store"

	blob := llb.OCILayoutBlob("not/real@"+layer.Digest.String(), llb.ImageBlobOCIStore("", csID), llb.Filename("layer.tar.gz"), llb.Chown(123, 456))
	st = llb.Image("alpine").Run(llb.Shlex(`sh -c 'sha256sum /layers/layer.tar.gz | cut -d" " -f0 > /out/checksum && stat -c "%u-%g-%s" /layers/layer.tar.gz > /out/stat'`), llb.AddMount("/layers", blob, llb.Readonly)).AddMount("/out", llb.Scratch())

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "",
		},
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		OCIStores: map[string]content.Store{
			csID: store,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "stat"))
	require.NoError(t, err)
	require.Equal(t, "123-456-"+strconv.FormatInt(layer.Size, 10), strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "checksum"))
	require.NoError(t, err)
	require.Equal(t, layer.Digest.Hex(), strings.TrimSpace(string(dt)))

	provDt, err := os.ReadFile(filepath.Join(destDir, "provenance.json"))
	require.NoError(t, err)

	var stmt struct {
		intoto.StatementHeader
		Predicate provenancetypes.ProvenancePredicateSLSA1 `json:"predicate"`
	}
	require.NoError(t, json.Unmarshal(provDt, &stmt))

	expectedName, err := purl.RefToPURL(packageurl.TypeOCI, "not/real@"+layer.Digest.String(), nil)
	require.NoError(t, err)
	purlObj, err := packageurl.FromString(expectedName)
	require.NoError(t, err)
	purlObj.Qualifiers = append(purlObj.Qualifiers, packageurl.Qualifier{Key: "ref_type", Value: "blob"})
	expectedName = purlObj.ToString()

	found := false
	for _, m := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		if m.URI == expectedName {
			found = true
			require.Equal(t, layer.Digest.Hex(), m.Digest["sha256"])
			break
		}
	}
	require.True(t, found, "expected to find %q in %+v", expectedName, stmt.Predicate.BuildDefinition.ResolvedDependencies)
}

func testOCILayoutPlatformSource(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	requiresLinux(t)
	c, err := New(context.TODO(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// create a tempdir where we will store the OCI layout
	dir := t.TempDir()

	platformsToTest := []string{"linux/amd64", "linux/arm64"}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			st := llb.Scratch().File(
				llb.Mkfile("platform", 0600, []byte(platform)),
			)

			def, err := st.Marshal(ctx)
			if err != nil {
				return nil, err
			}

			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}

			ref, err := r.SingleRef()
			if err != nil {
				return nil, err
			}

			_, err = ref.ToState()
			if err != nil {
				return nil, err
			}
			res.AddRef(platform, ref)

			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       platform,
				Platform: platforms.MustParse(platform),
			}
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}
	attrs := map[string]string{}
	outW := bytes.NewBuffer(nil)
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Attrs:  attrs,
				Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: outW}),
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	// extract the tar stream to the directory as OCI layout
	m, err := testutil.ReadTarToMap(outW.Bytes(), false)
	require.NoError(t, err)

	for filename, tarItem := range m {
		fullFilename := path.Join(dir, filename)
		err = os.MkdirAll(path.Dir(fullFilename), 0755)
		require.NoError(t, err)
		if tarItem.Header.FileInfo().IsDir() {
			err = os.MkdirAll(fullFilename, 0755)
			require.NoError(t, err)
		} else {
			err = os.WriteFile(fullFilename, tarItem.Data, 0644)
			require.NoError(t, err)
		}
	}

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)
	require.Equal(t, 1, len(index.Manifests))
	digest := index.Manifests[0].Digest

	store, err := local.NewStore(dir)
	require.NoError(t, err)
	csID := "my-content-store"

	destDir := t.TempDir()

	frontendOCILayout := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(platformsToTest)),
		}
		for i, platform := range platformsToTest {
			st := llb.OCILayout(fmt.Sprintf("not/real@%s", digest), llb.OCIStore("", csID))

			def, err := st.Marshal(ctx, llb.Platform(platforms.MustParse(platform)))
			if err != nil {
				return nil, err
			}

			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}

			ref, err := r.SingleRef()
			if err != nil {
				return nil, err
			}

			_, err = ref.ToState()
			if err != nil {
				return nil, err
			}
			res.AddRef(platform, ref)

			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       platform,
				Platform: platforms.MustParse(platform),
			}
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		OCIStores: map[string]content.Store{
			csID: store,
		},
	}, "", frontendOCILayout, nil)
	require.NoError(t, err)

	for _, platform := range platformsToTest {
		dt, err := os.ReadFile(filepath.Join(destDir, strings.ReplaceAll(platform, "/", "_"), "platform"))
		require.NoError(t, err)
		require.Equal(t, []byte(platform), dt)
	}
}

func testOCILayoutSource(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureOCILayout)
	c, err := New(context.TODO(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// create a tempdir where we will store the OCI layout
	dir := t.TempDir()

	// make an image that is exported there
	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(
		llb.Scratch(),
		llb.Image(imgName),
	)

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	cmdPrefix := integration.UnixOrWindows(
		`sh -c "echo -n`,
		`cmd /C "echo`,
	)
	run(fmt.Sprintf(`%s first > foo"`, cmdPrefix))
	run(fmt.Sprintf(`%s second > bar"`, cmdPrefix))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	outW := bytes.NewBuffer(nil)
	attrs := map[string]string{}
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Attrs:  attrs,
				Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: outW}),
			},
		},
	}, nil)
	require.NoError(t, err)

	// extract the tar stream to the directory as OCI layout
	m, err := testutil.ReadTarToMap(outW.Bytes(), false)
	require.NoError(t, err)

	for filename, content := range m {
		fullFilename := path.Join(dir, filename)
		err = os.MkdirAll(path.Dir(fullFilename), 0755)
		require.NoError(t, err)
		if content.Header.FileInfo().IsDir() {
			err = os.MkdirAll(fullFilename, 0755)
			require.NoError(t, err)
		} else {
			err = os.WriteFile(fullFilename, content.Data, 0644)
			require.NoError(t, err)
		}
	}

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)
	require.Equal(t, 1, len(index.Manifests))
	digest := index.Manifests[0].Digest

	store, err := local.NewStore(dir)
	require.NoError(t, err)

	// reference the OCI Layout in a build
	// note that the key does not need to be the directory name, just something
	// unique. since we are doing just one build with one remote here, we can
	// give it any ID
	csID := "my-content-store"
	st = llb.OCILayout(fmt.Sprintf("not/real@%s", digest), llb.OCIStore("", csID))

	def, err = st.Marshal(context.TODO())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		OCIStores: map[string]content.Store{
			csID: store,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	newLine := integration.UnixOrWindows("", " \r\n")
	require.Equal(t, []byte("first"+newLine), dt)

	dt, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("second"+newLine), dt)
}
