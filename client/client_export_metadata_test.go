package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/attestation"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	containerdutil "github.com/moby/buildkit/util/testutil/containerd"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	policyimage "github.com/moby/policy-helpers/image"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spdx/tools-golang/spdx"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"
)

func testAttestationBundle(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	ps := []ocispecs.Platform{
		platforms.MustParse("linux/amd64"),
	}

	scratch := func() llb.State {
		return integration.UnixOrWindows(llb.Scratch(), llb.Image("nanoserver"))
	}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{}

		for _, p := range ps {
			pk := platforms.Format(p)
			expPlatforms.Platforms = append(expPlatforms.Platforms, exptypes.Platform{ID: pk, Platform: p})

			// build image
			st := scratch().File(
				llb.Mkfile("/greeting", 0600, fmt.Appendf(nil, "hello %s!", pk)),
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
			res.AddRef(pk, ref)

			stmt := intoto.Statement{
				StatementHeader: intoto.StatementHeader{
					Type:          intoto.StatementInTotoV1,
					PredicateType: "https://example.com/attestations/v1.0",
				},
				Predicate: map[string]any{
					"foo": "1",
				},
			}
			buff := bytes.NewBuffer(nil)
			enc := json.NewEncoder(buff)
			require.NoError(t, enc.Encode(stmt))

			// build attestations
			st = scratch().File(
				llb.Mkdir("/bundle", 0700),
			)
			st = st.File(
				llb.Mkfile("/bundle/attestation.json", 0600, buff.Bytes()),
			)
			def, err = st.Marshal(ctx)
			if err != nil {
				return nil, err
			}
			r, err = c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}
			refAttest, err := r.SingleRef()
			if err != nil {
				return nil, err
			}
			_, err = ref.ToState()
			if err != nil {
				return nil, err
			}
			res.AddAttestation(pk, gateway.Attestation{
				Kind: gatewaypb.AttestationKind_Bundle,
				Ref:  refAttest,
				Path: "/bundle",
			})
		}

		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	target := registry + "/buildkit/testattestationsbundle:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, len(ps)*2, len(imgs.Images))

	var bases []*testutil.ImageInfo
	for _, p := range ps {
		pk := platforms.Format(p)
		bases = append(bases, imgs.Find(pk))
	}

	atts := imgs.Filter("unknown/unknown")
	require.Equal(t, len(ps)*1, len(atts.Images))
	for i, att := range atts.Images {
		require.Equal(t, 1, len(att.LayersRaw))
		var attest intoto.Statement
		require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))

		require.Equal(t, "https://example.com/attestations/v1.0", attest.PredicateType)
		require.Equal(t, map[string]any{"foo": "1"}, attest.Predicate)
		name := fmt.Sprintf("pkg:docker/%s/buildkit/testattestationsbundle@latest?platform=%s", url.QueryEscape(registry), url.QueryEscape(platforms.Format(ps[i])))
		subjects := []intoto.Subject{{
			Name: name,
			Digest: map[string]string{
				"sha256": bases[i].Desc.Digest.Encoded(),
			},
		}}
		require.Equal(t, subjects, attest.Subject)
	}
}

func testAttestationDefaultSubject(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	ps := []ocispecs.Platform{
		platforms.MustParse("linux/amd64"),
	}

	success := []byte(`{"success": true}`)
	scratch := func() llb.State {
		return integration.UnixOrWindows(llb.Scratch(), llb.Image("nanoserver"))
	}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{}

		for _, p := range ps {
			pk := platforms.Format(p)
			expPlatforms.Platforms = append(expPlatforms.Platforms, exptypes.Platform{ID: pk, Platform: p})

			// build image
			st := scratch().File(
				llb.Mkfile("/greeting", 0600, fmt.Appendf(nil, "hello %s!", pk)),
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
			res.AddRef(pk, ref)

			// build attestations
			st = scratch().File(llb.Mkfile("/attestation.json", 0600, success))
			def, err = st.Marshal(ctx)
			if err != nil {
				return nil, err
			}
			r, err = c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}
			refAttest, err := r.SingleRef()
			if err != nil {
				return nil, err
			}
			_, err = ref.ToState()
			if err != nil {
				return nil, err
			}
			res.AddAttestation(pk, gateway.Attestation{
				Kind: gatewaypb.AttestationKind_InToto,
				Ref:  refAttest,
				Path: "/attestation.json",
				InToto: result.InTotoAttestation{
					PredicateType: "https://example.com/attestations/v1.0",
				},
			})
		}

		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	target := registry + "/buildkit/testattestationsemptysubject:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, len(ps)*2, len(imgs.Images))

	var bases []*testutil.ImageInfo
	for _, p := range ps {
		pk := platforms.Format(p)
		bases = append(bases, imgs.Find(pk))
	}

	atts := imgs.Filter("unknown/unknown")
	require.Equal(t, len(ps), len(atts.Images))
	for i, att := range atts.Images {
		var attest intoto.Statement
		require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))

		require.Equal(t, intoto.StatementInTotoV1, attest.Type)
		require.Equal(t, "https://example.com/attestations/v1.0", attest.PredicateType)
		require.Equal(t, map[string]any{"success": true}, attest.Predicate)

		name := fmt.Sprintf("pkg:docker/%s/buildkit/testattestationsemptysubject@latest?platform=%s", url.QueryEscape(registry), url.QueryEscape(platforms.Format(ps[i])))
		subjects := []intoto.Subject{{
			Name: name,
			Digest: map[string]string{
				"sha256": bases[i].Desc.Digest.Encoded(),
			},
		}}
		require.Equal(t, subjects, attest.Subject)
	}
}

func testExportAnnotations(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	scratch := integration.UnixOrWindows(llb.Scratch(), llb.Image("nanoserver"))

	amd64 := platforms.MustParse("linux/amd64")
	arm64 := platforms.MustParse("linux/arm64")
	ps := []ocispecs.Platform{amd64, arm64}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(ps)),
		}
		for i, p := range ps {
			st := scratch.File(
				llb.Mkfile("platform", 0600, []byte(platforms.Format(p))),
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

			k := platforms.Format(p)
			res.AddRef(k, ref)

			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       k,
				Platform: p,
			}
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		res.AddMeta(exptypes.AnnotationIndexKey("gi"), []byte("generic index"))
		res.AddMeta(exptypes.AnnotationIndexDescriptorKey("gid"), []byte("generic index descriptor"))
		res.AddMeta(exptypes.AnnotationManifestKey(nil, "gm"), []byte("generic manifest"))
		res.AddMeta(exptypes.AnnotationManifestDescriptorKey(nil, "gmd"), []byte("generic manifest descriptor"))
		res.AddMeta(exptypes.AnnotationManifestKey(&amd64, "m"), []byte("amd64 manifest"))
		res.AddMeta(exptypes.AnnotationManifestKey(&arm64, "m"), []byte("arm64 manifest"))
		res.AddMeta(exptypes.AnnotationManifestDescriptorKey(&amd64, "md"), []byte("amd64 manifest descriptor"))
		res.AddMeta(exptypes.AnnotationManifestDescriptorKey(&arm64, "md"), []byte("arm64 manifest descriptor"))
		res.AddMeta(exptypes.AnnotationKey{Key: "gd"}.String(), []byte("generic default"))

		return res, nil
	}

	// testing for image exporter

	target := registry + "/buildkit/testannotations:latest"

	const created = "2022-01-23T12:34:56Z"

	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                 target,
					"push":                 "true",
					"annotation-index.gio": "generic index opt",
					"annotation-index." + ocispecs.AnnotationCreated:  created,
					"annotation-manifest.gmo":                         "generic manifest opt",
					"annotation-manifest-descriptor.gmdo":             "generic manifest descriptor opt",
					"annotation-manifest[linux/amd64].mo":             "amd64 manifest opt",
					"annotation-manifest-descriptor[linux/amd64].mdo": "amd64 manifest descriptor opt",
					"annotation-manifest[linux/arm64].mo":             "arm64 manifest opt",
					"annotation-manifest-descriptor[linux/arm64].mdo": "arm64 manifest descriptor opt",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	require.Equal(t, "generic index", imgs.Index.Annotations["gi"])
	require.Equal(t, "generic index opt", imgs.Index.Annotations["gio"])
	require.Equal(t, created, imgs.Index.Annotations[ocispecs.AnnotationCreated])
	for _, desc := range imgs.Index.Manifests {
		require.Equal(t, "generic manifest descriptor", desc.Annotations["gmd"])
		require.Equal(t, "generic manifest descriptor opt", desc.Annotations["gmdo"])
		switch {
		case platforms.Only(amd64).Match(*desc.Platform):
			require.Equal(t, "amd64 manifest descriptor", desc.Annotations["md"])
			require.Equal(t, "amd64 manifest descriptor opt", desc.Annotations["mdo"])
		case platforms.Only(arm64).Match(*desc.Platform):
			require.Equal(t, "arm64 manifest descriptor", desc.Annotations["md"])
			require.Equal(t, "arm64 manifest descriptor opt", desc.Annotations["mdo"])
		default:
			require.Fail(t, "unrecognized platform")
		}
	}

	amdImage := imgs.Find(platforms.Format(amd64))
	require.Equal(t, "generic default", amdImage.Manifest.Annotations["gd"])
	require.Equal(t, "generic manifest", amdImage.Manifest.Annotations["gm"])
	require.Equal(t, "generic manifest opt", amdImage.Manifest.Annotations["gmo"])
	require.Equal(t, "amd64 manifest", amdImage.Manifest.Annotations["m"])
	require.Equal(t, "amd64 manifest opt", amdImage.Manifest.Annotations["mo"])

	armImage := imgs.Find(platforms.Format(arm64))
	require.Equal(t, "generic default", armImage.Manifest.Annotations["gd"])
	require.Equal(t, "generic manifest", armImage.Manifest.Annotations["gm"])
	require.Equal(t, "generic manifest opt", armImage.Manifest.Annotations["gmo"])
	require.Equal(t, "arm64 manifest", armImage.Manifest.Annotations["m"])
	require.Equal(t, "arm64 manifest opt", armImage.Manifest.Annotations["mo"])

	// testing for oci exporter

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
				Attrs: map[string]string{
					"annotation-index.gio":                                      "generic index opt",
					"annotation-index-descriptor.gido":                          "generic index descriptor opt",
					"annotation-index-descriptor." + ocispecs.AnnotationCreated: created,
					"annotation-manifest.gmo":                                   "generic manifest opt",
					"annotation-manifest-descriptor.gmdo":                       "generic manifest descriptor opt",
					"annotation-manifest[linux/amd64].mo":                       "amd64 manifest opt",
					"annotation-manifest-descriptor[linux/amd64].mdo":           "amd64 manifest descriptor opt",
					"annotation-manifest[linux/arm64].mo":                       "arm64 manifest opt",
					"annotation-manifest-descriptor[linux/arm64].mdo":           "arm64 manifest descriptor opt",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var layout ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &layout)
	require.Equal(t, "generic index descriptor", layout.Manifests[0].Annotations["gid"])
	require.Equal(t, "generic index descriptor opt", layout.Manifests[0].Annotations["gido"])
	require.Equal(t, created, layout.Manifests[0].Annotations[ocispecs.AnnotationCreated])
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+layout.Manifests[0].Digest.Hex()].Data, &index)
	require.Equal(t, "generic index", index.Annotations["gi"])
	require.Equal(t, "generic index opt", index.Annotations["gio"])
	require.NoError(t, err)

	for _, desc := range index.Manifests {
		var mfst ocispecs.Manifest
		err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+desc.Digest.Hex()].Data, &mfst)
		require.NoError(t, err)

		require.Equal(t, "generic default", mfst.Annotations["gd"])
		require.Equal(t, "generic manifest", mfst.Annotations["gm"])
		require.Equal(t, "generic manifest descriptor", desc.Annotations["gmd"])
		require.Equal(t, "generic manifest opt", mfst.Annotations["gmo"])
		require.Equal(t, "generic manifest descriptor opt", desc.Annotations["gmdo"])

		switch {
		case platforms.Only(amd64).Match(*desc.Platform):
			require.Equal(t, "amd64 manifest", mfst.Annotations["m"])
			require.Equal(t, "amd64 manifest descriptor", desc.Annotations["md"])
			require.Equal(t, "amd64 manifest opt", mfst.Annotations["mo"])
			require.Equal(t, "amd64 manifest descriptor opt", desc.Annotations["mdo"])
		case platforms.Only(arm64).Match(*desc.Platform):
			require.Equal(t, "arm64 manifest", mfst.Annotations["m"])
			require.Equal(t, "arm64 manifest descriptor", desc.Annotations["md"])
			require.Equal(t, "arm64 manifest opt", mfst.Annotations["mo"])
			require.Equal(t, "arm64 manifest descriptor opt", desc.Annotations["mdo"])
		default:
			require.Fail(t, "unrecognized platform")
		}
	}
}

func testExportAnnotationsMediaTypes(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	p := platforms.DefaultSpec()
	ps := []ocispecs.Platform{p}

	scratch := integration.UnixOrWindows(llb.Scratch(), llb.Image("nanoserver"))

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(ps)),
		}
		for i, p := range ps {
			st := scratch.File(
				llb.Mkfile("platform", 0600, []byte(platforms.Format(p))),
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

			k := platforms.Format(p)
			res.AddRef(k, ref)

			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       k,
				Platform: p,
			}
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	target := registry + "/buildkit/testannotationsmedia:1"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                  target,
					"push":                  "true",
					"annotation-manifest.a": "b",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 1, len(imgs.Images))

	target2 := registry + "/buildkit/testannotationsmedia:2"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":               target2,
					"push":               "true",
					"annotation-index.c": "d",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target2)
	require.NoError(t, err)
	imgs2, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 1, len(imgs2.Images))

	require.Equal(t, "b", imgs.Images[0].Manifest.Annotations["a"])
	require.Equal(t, "d", imgs2.Index.Annotations["c"])

	require.Equal(t, ocispecs.MediaTypeImageIndex, imgs.Index.MediaType)
	require.Equal(t, ocispecs.MediaTypeImageIndex, imgs2.Index.MediaType)
}

func testExportAttestations(t *testing.T, sb integration.Sandbox, ociArtifact bool, setOCIArtifact bool) {
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

	ps := []ocispecs.Platform{
		platforms.MustParse("linux/amd64"),
		platforms.MustParse("linux/arm64"),
	}

	success := []byte(`{"success": true}`)
	successDigest := digest.SHA256.FromBytes(success)

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()
		expPlatforms := &exptypes.Platforms{}

		for _, p := range ps {
			pk := platforms.Format(p)
			expPlatforms.Platforms = append(expPlatforms.Platforms, exptypes.Platform{ID: pk, Platform: p})

			// build image
			st := llb.Scratch().File(
				llb.Mkfile("/greeting", 0600, fmt.Appendf(nil, "hello %s!", pk)),
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
			res.AddRef(pk, ref)

			// build attestations
			st = llb.Scratch().
				File(llb.Mkfile("/attestation.json", 0600, success)).
				File(llb.Mkfile("/attestation2.json", 0600, []byte{}))
			def, err = st.Marshal(ctx)
			if err != nil {
				return nil, err
			}
			r, err = c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}
			refAttest, err := r.SingleRef()
			if err != nil {
				return nil, err
			}
			_, err = ref.ToState()
			if err != nil {
				return nil, err
			}
			res.AddAttestation(pk, gateway.Attestation{
				Kind: gatewaypb.AttestationKind_InToto,
				Ref:  refAttest,
				Path: "/attestation.json",
				InToto: result.InTotoAttestation{
					PredicateType: "https://example.com/attestations/v1.0",
					Subjects: []result.InTotoSubject{{
						Kind: gatewaypb.InTotoSubjectKind_Self,
					}},
				},
			})
			res.AddAttestation(pk, gateway.Attestation{
				Kind: gatewaypb.AttestationKind_InToto,
				Ref:  refAttest,
				Path: "/attestation2.json",
				InToto: result.InTotoAttestation{
					PredicateType: "https://example.com/attestations2/v1.0",
					Subjects: []result.InTotoSubject{{
						Kind:   gatewaypb.InTotoSubjectKind_Raw,
						Name:   "/attestation.json",
						Digest: []digest.Digest{successDigest},
					}},
				},
			})
		}

		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	t.Run("image", func(t *testing.T) {
		expectedOCIArtifact := !setOCIArtifact || ociArtifact
		targets := []string{
			registry + "/buildkit/testattestationsfoo:latest",
			registry + "/buildkit/testattestationsbar:latest",
		}
		attrs := map[string]string{
			"name": strings.Join(targets, ","),
			"push": "true",
		}
		if setOCIArtifact {
			attrs["oci-artifact"] = strconv.FormatBool(ociArtifact)
		}
		_, err = c.Build(sb.Context(), SolveOpt{
			Exports: []ExportEntry{
				{
					Type:  ExporterImage,
					Attrs: attrs,
				},
			},
		}, "", frontend, nil)
		require.NoError(t, err)

		desc, provider, err := contentutil.ProviderFromRef(targets[0])
		require.NoError(t, err)

		imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
		require.NoError(t, err)
		require.Equal(t, len(ps)*2, len(imgs.Images))

		var bases []*testutil.ImageInfo
		for _, p := range ps {
			pk := platforms.Format(p)
			img := imgs.Find(pk)
			require.NotNil(t, img)
			require.Equal(t, pk, platforms.Format(*img.Desc.Platform))
			require.Equal(t, 1, len(img.Layers))
			require.Equal(t, fmt.Appendf(nil, "hello %s!", pk), img.Layers[0]["greeting"].Data)
			bases = append(bases, img)
		}

		atts := imgs.Filter("unknown/unknown")
		require.Equal(t, len(ps), len(atts.Images))
		for i, att := range atts.Images {
			require.Equal(t, ocispecs.MediaTypeImageManifest, att.Desc.MediaType)
			require.Equal(t, "unknown/unknown", platforms.Format(*att.Desc.Platform))
			require.Equal(t, attestation.DockerAnnotationReferenceTypeDefault, att.Desc.Annotations[attestation.DockerAnnotationReferenceType])
			require.Equal(t, bases[i].Desc.Digest.String(), att.Desc.Annotations[attestation.DockerAnnotationReferenceDigest])
			require.Equal(t, 2, len(att.Layers))

			if expectedOCIArtifact {
				subject := att.Manifest.Subject
				require.NotNil(t, subject)
				require.Equal(t, bases[i].Desc.MediaType, subject.MediaType)
				require.Equal(t, bases[i].Desc.Digest, subject.Digest)
				require.Equal(t, bases[i].Desc.Size, subject.Size)
				require.Empty(t, subject.Annotations)
				require.Nil(t, subject.Platform)
				require.Equal(t, "application/vnd.docker.attestation.manifest.v1+json", att.Manifest.ArtifactType)
				require.Equal(t, ocispecs.DescriptorEmptyJSON, att.Manifest.Config)
			} else {
				require.Nil(t, att.Manifest.Subject)
				require.Empty(t, att.Manifest.ArtifactType)

				// image config is not included in the OCI artifact
				require.Equal(t, "unknown/unknown", att.Img.OS+"/"+att.Img.Architecture)
				require.Equal(t, len(att.Layers), len(att.Img.RootFS.DiffIDs))
				require.Equal(t, 0, len(att.Img.History))
			}

			var attest intoto.Statement
			require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))

			purls := map[string]string{}
			for _, k := range targets {
				named, err := reference.ParseNormalizedNamed(k)
				require.NoError(t, err)
				name := reference.FamiliarName(named)
				version := ""
				if tagged, ok := named.(reference.Tagged); ok {
					version = tagged.Tag()
				}
				p := fmt.Sprintf("pkg:docker/%s%s@%s?platform=%s", url.QueryEscape(registry), strings.TrimPrefix(name, registry), version, url.PathEscape(platforms.Format(ps[i])))
				purls[k] = p
			}

			require.Equal(t, intoto.StatementInTotoV1, attest.Type)
			require.Equal(t, "https://example.com/attestations/v1.0", attest.PredicateType)
			require.Equal(t, map[string]any{"success": true}, attest.Predicate)
			subjects := []intoto.Subject{
				{
					Name: purls[targets[0]],
					Digest: map[string]string{
						"sha256": bases[i].Desc.Digest.Encoded(),
					},
				},
				{
					Name: purls[targets[1]],
					Digest: map[string]string{
						"sha256": bases[i].Desc.Digest.Encoded(),
					},
				},
			}
			require.Equal(t, subjects, attest.Subject)

			var attest2 intoto.Statement
			require.NoError(t, json.Unmarshal(att.LayersRaw[1], &attest2))

			require.Equal(t, intoto.StatementInTotoV1, attest2.Type)
			require.Equal(t, "https://example.com/attestations2/v1.0", attest2.PredicateType)
			require.Nil(t, attest2.Predicate)
			subjects = []intoto.Subject{{
				Name: "/attestation.json",
				Digest: map[string]string{
					"sha256": successDigest.Encoded(),
				},
			}}
			require.Equal(t, subjects, attest2.Subject)
		}

		cdAddress := sb.ContainerdAddress()
		if cdAddress == "" {
			return
		}
		client, err := ctd.New(cdAddress)
		require.NoError(t, err)
		defer client.Close()
		ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

		for _, target := range targets {
			err = client.ImageService().Delete(ctx, target, images.SynchronousDelete())
			require.NoError(t, err)
		}
		checkAllReleasable(t, c, sb, true)
	})

	t.Run("local", func(t *testing.T) {
		dir := t.TempDir()
		_, err = c.Build(sb.Context(), SolveOpt{
			Exports: []ExportEntry{
				{
					Type:      ExporterLocal,
					OutputDir: dir,
					Attrs: map[string]string{
						"attestation-prefix": "test.",
					},
				},
			},
		}, "", frontend, nil)
		require.NoError(t, err)

		for _, p := range ps {
			var attest intoto.Statement
			dt, err := os.ReadFile(path.Join(dir, strings.ReplaceAll(platforms.Format(p), "/", "_"), "test.attestation.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(dt, &attest))

			require.Equal(t, intoto.StatementInTotoV1, attest.Type)
			require.Equal(t, "https://example.com/attestations/v1.0", attest.PredicateType)
			require.Equal(t, map[string]any{"success": true}, attest.Predicate)

			require.Equal(t, []intoto.Subject{{
				Name:   "greeting",
				Digest: result.ToDigestMap(digest.Canonical.FromString("hello " + platforms.Format(p) + "!")),
			}}, attest.Subject)

			var attest2 intoto.Statement
			dt, err = os.ReadFile(path.Join(dir, strings.ReplaceAll(platforms.Format(p), "/", "_"), "test.attestation2.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(dt, &attest2))

			require.Equal(t, intoto.StatementInTotoV1, attest2.Type)
			require.Equal(t, "https://example.com/attestations2/v1.0", attest2.PredicateType)
			require.Nil(t, attest2.Predicate)
			subjects := []intoto.Subject{{
				Name: "/attestation.json",
				Digest: map[string]string{
					"sha256": successDigest.Encoded(),
				},
			}}
			require.Equal(t, subjects, attest2.Subject)
		}
	})

	t.Run("tar", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "out.tar")
		outW, err := os.Create(out)
		require.NoError(t, err)

		_, err = c.Build(sb.Context(), SolveOpt{
			Exports: []ExportEntry{
				{
					Type:   ExporterTar,
					Output: fixedWriteCloser(outW),
					Attrs: map[string]string{
						"attestation-prefix": "test.",
					},
				},
			},
		}, "", frontend, nil)
		require.NoError(t, err)

		dt, err := os.ReadFile(out)
		require.NoError(t, err)

		m, err := testutil.ReadTarToMap(dt, false)
		require.NoError(t, err)

		for _, p := range ps {
			var attest intoto.Statement
			item := m[path.Join(strings.ReplaceAll(platforms.Format(p), "/", "_"), "test.attestation.json")]
			require.NotNil(t, item)
			require.NoError(t, json.Unmarshal(item.Data, &attest))

			require.Equal(t, intoto.StatementInTotoV1, attest.Type)
			require.Equal(t, "https://example.com/attestations/v1.0", attest.PredicateType)
			require.Equal(t, map[string]any{"success": true}, attest.Predicate)

			require.Equal(t, []intoto.Subject{{
				Name:   "greeting",
				Digest: result.ToDigestMap(digest.Canonical.FromString("hello " + platforms.Format(p) + "!")),
			}}, attest.Subject)

			var attest2 intoto.Statement
			item = m[path.Join(strings.ReplaceAll(platforms.Format(p), "/", "_"), "test.attestation2.json")]
			require.NotNil(t, item)
			require.NoError(t, json.Unmarshal(item.Data, &attest2))

			require.Equal(t, intoto.StatementInTotoV1, attest2.Type)
			require.Equal(t, "https://example.com/attestations2/v1.0", attest2.PredicateType)
			require.Nil(t, attest2.Predicate)
			subjects := []intoto.Subject{{
				Name: "/attestation.json",
				Digest: map[string]string{
					"sha256": successDigest.Encoded(),
				},
			}}
			require.Equal(t, subjects, attest2.Subject)
		}
	})
}

func testExportAttestationsDefaultOCIArtifact(t *testing.T, sb integration.Sandbox) {
	testExportAttestations(t, sb, false, false)
}

func testExportAttestationsImageManifest(t *testing.T, sb integration.Sandbox) {
	testExportAttestations(t, sb, false, true)
}

func testExportAttestationsOCIArtifact(t *testing.T, sb integration.Sandbox) {
	testExportAttestations(t, sb, true, true)
}

func testImageResolveAttestationChainLocal(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureProvenance)
	requiresLinux(t)

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target, platform := buildProvenanceImage(ctx, t, c, sb)

	_, err = c.Build(ctx, SolveOpt{}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		md, err := c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: "docker-image://" + target,
		}, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				NoConfig:         true,
				AttestationChain: true,
				Platform:         &platform,
				ResolveMode:      pb.AttrImageResolveModeForcePull,
			},
		})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Image)
		require.NotNil(t, md.Image.AttestationChain)
		ac := md.Image.AttestationChain
		require.NotEmpty(t, ac.AttestationManifest)
		att := ac.Blobs[ac.AttestationManifest]
		require.NotEmpty(t, att.Data)

		var manifest ocispecs.Manifest
		require.NoError(t, json.Unmarshal(att.Data, &manifest))
		require.NotEmpty(t, manifest.Layers)
		found := false
		for _, layer := range manifest.Layers {
			if isSLSAPredicateType(layer.Annotations["in-toto.io/predicate-type"]) {
				found = true
				break
			}
		}
		require.True(t, found)
		return nil, nil
	}, nil)
	require.NoError(t, err)
}

func testImageResolveAttestationChainRequiresNetwork(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiPlatform, workers.FeatureProvenance)
	// this test temporarily requires direct registry access as the integration test
	// mirroring system does not support mirroring attestation chains yet.
	// Support is coming in future buildx release.
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	amd64, err := platforms.Parse("linux/amd64")
	require.NoError(t, err)

	_, err = c.Build(ctx, SolveOpt{}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		const rootDigest = "sha256:4e91099af134f4b1f509fecbd1a55981dfff18d8029d6782e28413210ef468b7"
		const imageDigest = "sha256:d2f0b39234a66c3d58f24200d3fda9e4e3d2263c09f9b7286a826ab639713047"
		const attestationDigest = "sha256:fb4c46b14f52d1bf790f593921c52dddc698fc6780792fb8469fc60efc0e609b"
		const sigDigest = "sha256:bd0a6b088440ba9838e8eec79e736128fe52afce934578eae30ca8675f6d3142"

		id := "registry-1-stage.docker.io/docker/github-builder-test@" + rootDigest
		md, err := c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: "docker-image://" + id,
		}, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				NoConfig:         true,
				AttestationChain: true,
				Platform:         &amd64,
			},
		})
		if err != nil {
			return nil, err
		}
		require.Equal(t, rootDigest, md.Image.Digest.String())
		require.Nil(t, md.Image.Config)
		require.NotNil(t, md.Image)
		require.NotNil(t, md.Image.AttestationChain)
		ac := md.Image.AttestationChain
		require.Equal(t, rootDigest, ac.Root.String())
		require.Equal(t, imageDigest, ac.ImageManifest.String())
		require.Equal(t, attestationDigest, ac.AttestationManifest.String())
		require.Len(t, ac.SignatureManifests, 1)
		require.Equal(t, sigDigest, ac.SignatureManifests[0].String())

		desc := ac.Blobs[ac.Root]
		require.Equal(t, rootDigest, desc.Descriptor.Digest.String())
		require.Len(t, desc.Data, int(desc.Descriptor.Size))
		require.Equal(t, ocispecs.MediaTypeImageIndex, desc.Descriptor.MediaType)
		chk := digest.FromBytes(desc.Data)
		require.Equal(t, rootDigest, chk.String())

		desc = ac.Blobs[ac.ImageManifest]
		// image manifest is expected to be missing as content is not needed to verify attestation chain
		_, ok := ac.Blobs[digest.Digest(imageDigest)]
		require.False(t, ok)

		desc = ac.Blobs[ac.AttestationManifest]
		require.Equal(t, attestationDigest, desc.Descriptor.Digest.String())
		require.Equal(t, ocispecs.MediaTypeImageManifest, desc.Descriptor.MediaType)
		require.Len(t, desc.Data, int(desc.Descriptor.Size))
		chk = digest.FromBytes(desc.Data)
		require.Equal(t, attestationDigest, chk.String())

		desc = ac.Blobs[ac.SignatureManifests[0]]
		require.Equal(t, sigDigest, desc.Descriptor.Digest.String())
		require.Equal(t, ocispecs.MediaTypeImageManifest, desc.Descriptor.MediaType)
		require.Len(t, desc.Data, int(desc.Descriptor.Size))
		chk = digest.FromBytes(desc.Data)
		require.Equal(t, sigDigest, chk.String())

		var sigMfst ocispecs.Manifest
		err = json.Unmarshal(ac.Blobs[ac.SignatureManifests[0]].Data, &sigMfst)
		require.NoError(t, err)
		require.Equal(t, 1, len(sigMfst.Layers))
		sigLayer := sigMfst.Layers[0]
		sigDesc := ac.Blobs[digest.Digest(sigLayer.Digest)]
		require.Equal(t, sigLayer.Digest, sigDesc.Descriptor.Digest)
		require.Len(t, sigDesc.Data, int(sigDesc.Descriptor.Size))
		require.Equal(t, "application/vnd.dev.sigstore.bundle.v0.3+json", sigDesc.Descriptor.MediaType)
		chk = digest.FromBytes(sigDesc.Data)
		require.Equal(t, sigLayer.Digest, chk)

		require.Len(t, md.Image.AttestationChain.Blobs, 4)
		return nil, nil
	}, nil)
	require.NoError(t, err)
}

func testImageResolveProvenanceAttestation(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureProvenance)
	requiresLinux(t)

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target, platform := buildProvenanceImage(ctx, t, c, sb)

	_, err = c.Build(ctx, SolveOpt{}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		md, err := c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: "docker-image://" + target,
		}, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				NoConfig: true,
				ResolveAttestations: []string{
					policyimage.SLSAProvenancePredicateType02,
					policyimage.SLSAProvenancePredicateType1,
				},
				Platform:    &platform,
				ResolveMode: pb.AttrImageResolveModeForcePull,
			},
		})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Image)
		require.NotNil(t, md.Image.AttestationChain)
		ac := md.Image.AttestationChain
		require.NotEmpty(t, ac.AttestationManifest)
		att := ac.Blobs[ac.AttestationManifest]
		require.NotEmpty(t, att.Data)

		var manifest ocispecs.Manifest
		require.NoError(t, json.Unmarshal(att.Data, &manifest))
		require.NotEmpty(t, manifest.Layers)
		var (
			stmtBytes  []byte
			foundLayer ocispecs.Descriptor
		)
		for _, layer := range manifest.Layers {
			if !isSLSAPredicateType(layer.Annotations["in-toto.io/predicate-type"]) {
				continue
			}
			blob, ok := ac.Blobs[layer.Digest]
			if !ok {
				continue
			}
			stmtBytes = blob.Data
			foundLayer = layer
			break
		}
		require.NotEmpty(t, stmtBytes)
		require.Contains(t, []string{
			policyimage.SLSAProvenancePredicateType02,
			policyimage.SLSAProvenancePredicateType1,
		}, foundLayer.Annotations["in-toto.io/predicate-type"])

		var stmt intoto.Statement
		require.NoError(t, json.Unmarshal(stmtBytes, &stmt))
		require.Equal(t, intoto.StatementInTotoV1, stmt.Type)
		require.Contains(t, []string{
			policyimage.SLSAProvenancePredicateType02,
			policyimage.SLSAProvenancePredicateType1,
		}, stmt.PredicateType)
		require.Equal(t, stmt.Subject[0].Digest["sha256"], ac.ImageManifest.Hex())
		return nil, nil
	}, nil)
	require.NoError(t, err)
}

func testSBOMScan(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureSBOM)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	p := platforms.MustParse("linux/amd64")
	pk := platforms.Format(p)

	scannerFrontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()

		st := llb.Image("busybox", llb.Platform(p))
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

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
		res.SetRef(ref)

		var img ocispecs.Image
		cmd := `
cat <<EOF > $BUILDKIT_SCAN_DESTINATION/spdx.json
{
  "_type": "https://in-toto.io/Statement/v1",
  "predicateType": "https://spdx.dev/Document",
  "predicate": {
	"name": "fallback",
	"extraParams": {
	  "ARG1": "$BUILDKIT_SCAN_ARG1",
	  "ARG2": "$BUILDKIT_SCAN_ARG2"
	}
  }
}
EOF
`
		img.Config.Cmd = []string{"/bin/sh", "-c", cmd}
		img.Platform = p
		config, err := json.Marshal(img)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal image config")
		}
		res.AddMeta(exptypes.ExporterImageConfigKey, config)

		return res, nil
	}

	scannerTarget := registry + "/buildkit/testsbomscanner:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":           scannerTarget,
					"push":           "true",
					"oci-mediatypes": "false",
				},
			},
		},
	}, "", scannerFrontend, nil)
	require.NoError(t, err)

	makeTargetFrontend := func(attest bool) func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		return func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			res := gateway.NewResult()

			// build image
			st := llb.Scratch().File(
				llb.Mkfile("/greeting", 0600, []byte("hello world!")),
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
			res.AddRef(pk, ref)

			expPlatforms := &exptypes.Platforms{
				Platforms: []exptypes.Platform{{ID: pk, Platform: p}},
			}
			dt, err := json.Marshal(expPlatforms)
			if err != nil {
				return nil, err
			}
			res.AddMeta(exptypes.ExporterPlatformsKey, dt)

			// build attestations
			if attest {
				st = llb.Scratch().
					File(llb.Mkfile("/result.spdx", 0600, []byte(`{"name": "frontend"}`)))
				def, err = st.Marshal(ctx)
				if err != nil {
					return nil, err
				}
				r, err = c.Solve(ctx, gateway.SolveRequest{
					Definition: def.ToPB(),
				})
				if err != nil {
					return nil, err
				}
				refAttest, err := r.SingleRef()
				if err != nil {
					return nil, err
				}
				_, err = ref.ToState()
				if err != nil {
					return nil, err
				}

				res.AddAttestation(pk, gateway.Attestation{
					Kind: gatewaypb.AttestationKind_InToto,
					Ref:  refAttest,
					Path: "/result.spdx",
					InToto: result.InTotoAttestation{
						PredicateType: intoto.PredicateSPDX,
					},
				})
			}

			return res, nil
		}
	}

	// test the default fallback scanner
	target := registry + "/buildkit/testsbom:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "",
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", makeTargetFrontend(false), nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	// test the frontend builtin scanner
	target = registry + "/buildkit/testsbom2:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "",
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", makeTargetFrontend(true), nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	att := imgs.Find("unknown/unknown")
	attest := intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{"name": "frontend"})

	// test the specified fallback scanner
	target = registry + "/buildkit/testsbom3:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "generator=" + scannerTarget,
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", makeTargetFrontend(false), nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	att = imgs.Find("unknown/unknown")
	attest = intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{"name": "fallback"})

	// test the builtin frontend scanner and the specified fallback scanner together
	target = registry + "/buildkit/testsbom3:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "generator=" + scannerTarget,
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", makeTargetFrontend(true), nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	att = imgs.Find("unknown/unknown")
	attest = intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{"name": "frontend"})

	// test configuring the scanner (simple)
	target = registry + "/buildkit/testsbom4:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "generator=" + scannerTarget + ",ARG1=foo,ARG2=bar",
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", makeTargetFrontend(false), nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	att = imgs.Find("unknown/unknown")
	attest = intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{
		"extraParams": map[string]any{"ARG1": "foo", "ARG2": "bar"},
	})

	// test configuring the scanner (complex)
	target = registry + "/buildkit/testsbom4:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "\"generator=" + scannerTarget + "\",\"ARG1=foo\",\"ARG2=hello,world\"",
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", makeTargetFrontend(false), nil)
	require.NoError(t, err)

	desc, provider, err = contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err = testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	att = imgs.Find("unknown/unknown")
	attest = intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{
		"extraParams": map[string]any{"ARG1": "foo", "ARG2": "hello,world"},
	})
}

func testSBOMScanSingleRef(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureSBOM)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	p := platforms.DefaultSpec()
	pk := platforms.Format(p)

	scannerFrontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()

		st := llb.Image("busybox", llb.Platform(p))
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

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
		res.SetRef(ref)

		var img ocispecs.Image
		cmd := `
cat <<EOF > $BUILDKIT_SCAN_DESTINATION/spdx.json
{
  "_type": "https://in-toto.io/Statement/v1",
  "predicateType": "https://spdx.dev/Document",
  "predicate": {"name": "fallback"}
}
EOF
`
		img.Config.Cmd = []string{"/bin/sh", "-c", cmd}
		img.Platform = p
		config, err := json.Marshal(img)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal image config")
		}
		res.AddMeta(exptypes.ExporterImageConfigKey, config)

		return res, nil
	}

	scannerTarget := registry + "/buildkit/testsbomscanner:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":           scannerTarget,
					"push":           "true",
					"oci-mediatypes": "false",
				},
			},
		},
	}, "", scannerFrontend, nil)
	require.NoError(t, err)

	targetFrontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()

		// build image
		st := llb.Scratch().File(
			llb.Mkfile("/greeting", 0600, []byte("hello world!")),
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
		res.SetRef(ref)

		var img ocispecs.Image
		img.Config.Cmd = []string{"/bin/sh", "-c", "cat /greeting"}
		img.Platform = p
		config, err := json.Marshal(img)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal image config")
		}
		res.AddMeta(exptypes.ExporterImageConfigKey, config)

		expPlatforms := &exptypes.Platforms{
			Platforms: []exptypes.Platform{{ID: pk, Platform: p}},
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	target := registry + "/buildkit/testsbomsingle:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "generator=" + scannerTarget,
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", targetFrontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	var img *testutil.ImageInfo
	for _, candidate := range imgs.Images {
		if candidate.Desc.Platform != nil && platforms.Format(*candidate.Desc.Platform) == "unknown/unknown" {
			continue
		}
		img = candidate
		break
	}
	require.NotNil(t, img)
	require.Equal(t, []string{"/bin/sh", "-c", "cat /greeting"}, img.Img.Config.Cmd)

	att := imgs.Find("unknown/unknown")
	require.NotNil(t, att)
	attest := intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)
	require.Subset(t, attest.Predicate, map[string]any{"name": "fallback"})
}

func testSBOMSupplements(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureSBOM)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	p := platforms.MustParse("linux/amd64")
	pk := platforms.Format(p)

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()

		// build image
		st := llb.Scratch().File(
			llb.Mkfile("/foo", 0600, []byte{}),
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
		res.AddRef(pk, ref)

		expPlatforms := &exptypes.Platforms{
			Platforms: []exptypes.Platform{{ID: pk, Platform: p}},
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		// build attestations
		doc := spdx.Document{
			SPDXVersion:    "SPDX-2.2",
			SPDXIdentifier: "DOCUMENT",
			Files: []*spdx.File{
				{
					// foo exists...
					FileSPDXIdentifier: "SPDXRef-File-foo",
					FileName:           "/foo",
				},
				{
					// ...but bar doesn't
					FileSPDXIdentifier: "SPDXRef-File-bar",
					FileName:           "/bar",
				},
			},
		}
		docBytes, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		st = llb.Scratch().
			File(llb.Mkfile("/result.spdx", 0600, docBytes))
		def, err = st.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		r, err = c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		refAttest, err := r.SingleRef()
		if err != nil {
			return nil, err
		}
		_, err = ref.ToState()
		if err != nil {
			return nil, err
		}

		res.AddAttestation(pk, gateway.Attestation{
			Kind: gatewaypb.AttestationKind_InToto,
			Ref:  refAttest,
			Path: "/result.spdx",
			InToto: result.InTotoAttestation{
				PredicateType: intoto.PredicateSPDX,
			},
			Metadata: map[string][]byte{
				result.AttestationSBOMCore: []byte("result"),
			},
		})

		return res, nil
	}

	// test the default fallback scanner
	target := registry + "/buildkit/testsbom:latest"
	_, err = c.Build(sb.Context(), SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:sbom": "",
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	att := imgs.Find("unknown/unknown")
	attest := struct {
		intoto.StatementHeader
		Predicate spdx.Document
	}{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, intoto.StatementInTotoV1, attest.Type)
	require.Equal(t, intoto.PredicateSPDX, attest.PredicateType)

	require.Equal(t, "DOCUMENT", string(attest.Predicate.SPDXIdentifier))
	require.Len(t, attest.Predicate.Files, 2)
	require.Equal(t, "/foo", attest.Predicate.Files[0].FileName)
	require.Regexp(t, "^layerID: sha256:", attest.Predicate.Files[0].FileComment)
	require.Equal(t, "/bar", attest.Predicate.Files[1].FileName)
	require.Empty(t, attest.Predicate.Files[1].FileComment)

	checkAllReleasable(t, c, sb, true)
}

func testSourceDateEpochClamp(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	var bboxConfig []byte
	_, err = c.Build(sb.Context(), SolveOpt{}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		_, _, bboxConfig, err = c.ResolveImageConfig(ctx, "docker.io/library/busybox:latest", sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	require.NoError(t, err)

	m := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(bboxConfig, &m))
	delete(m, "created")
	bboxConfig, err = json.Marshal(m)
	require.NoError(t, err)

	busybox, err := llb.Image("busybox:latest").WithImageConfig(bboxConfig)
	require.NoError(t, err)

	def, err := busybox.Marshal(sb.Context())
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
				Type: ExporterOCI,
				Attrs: map[string]string{
					exptypes.ExporterImageConfigKey: string(bboxConfig),
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	busyboxTmsX, err := readImageTimestamps(dt)
	require.NoError(t, err)
	busyboxTms := busyboxTmsX.FromImage

	require.Greater(t, len(busyboxTms), 1)
	bboxLayerLen := len(busyboxTms) - 1

	tm, err := time.Parse(time.RFC3339Nano, busyboxTms[1])
	require.NoError(t, err)

	next := tm.Add(time.Hour).Truncate(time.Second)

	st := busybox.Run(llb.Shlex("touch /foo"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	out = filepath.Join(destDir, "out.tar")
	outW, err = os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", next.Unix()),
		},
		Exports: []ExportEntry{
			{
				Type: ExporterOCI,
				Attrs: map[string]string{
					exptypes.ExporterImageConfigKey: string(bboxConfig),
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(out)
	require.NoError(t, err)

	tmsX, err := readImageTimestamps(dt)
	require.NoError(t, err)
	tms := tmsX.FromImage

	require.Equal(t, len(tms), bboxLayerLen+2)

	expected := next.UTC().Format(time.RFC3339Nano)
	require.Equal(t, expected, tms[0])
	require.Equal(t, busyboxTms[1], tms[1])
	require.Equal(t, expected, tms[bboxLayerLen+1])
	require.Equal(t, expected, tmsX.FromAnnotation)

	checkAllReleasable(t, c, sb, true)
}

func testSourceDateEpochImageExporter(t *testing.T, sb integration.Sandbox) {
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.SkipNow()
	}

	// https://github.com/containerd/containerd/commit/133ddce7cf18a1db175150e7a69470dea1bb3132
	minVer := "v1.7.0-beta.1"
	cdVersion := containerdutil.GetVersion(t, cdAddress)
	// Normalize containerd version to semver format (add v prefix if missing)
	normalizedCdVersion := cdVersion
	if !strings.HasPrefix(cdVersion, "v") {
		normalizedCdVersion = "v" + cdVersion
	}
	if semver.Compare(normalizedCdVersion, minVer) < 0 {
		t.Skipf("containerd version %q does not satisfy minimal version %q", cdVersion, minVer)
	}

	workers.CheckFeatureCompat(t, sb, workers.FeatureSourceDateEpoch)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)

	type testVars struct {
		baseImgName string
		st          llb.State
		cmdPrefix   string
	}

	baseImg := llb.Image(integration.UnixOrWindows("busybox:latest", "nanoserver:latest"))
	v := integration.UnixOrWindows(
		testVars{
			baseImgName: "busybox:latest",
			st:          llb.Scratch(),
			cmdPrefix:   `sh -c "echo -n`,
		},
		testVars{
			baseImgName: "nanoserver:latest",
			st:          baseImg,
			cmdPrefix:   `cmd /C "echo`,
		},
	)

	st := v.st
	run := func(cmd string) {
		st = baseImg.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(fmt.Sprintf(`%s first> foo"`, v.cmdPrefix))
	run(fmt.Sprintf(`%s second> bar"`, v.cmdPrefix))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	name := strings.ToLower(path.Base(t.Name()))
	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": name,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	img, err := client.GetImage(ctx, name)
	require.NoError(t, err)
	require.Equal(t, tm, img.Metadata().CreatedAt)

	err = client.ImageService().Delete(ctx, name, images.SynchronousDelete())
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testSourceDateEpochLayerTimestamps(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
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

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
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

	tmsX, err := readImageTimestamps(dt)
	require.NoError(t, err)
	tms := tmsX.FromImage

	require.Equal(t, 3, len(tms))

	expected := tm.UTC().Format(time.RFC3339Nano)
	require.Equal(t, expected, tms[0])
	require.Equal(t, expected, tms[1])
	require.Equal(t, expected, tms[2])

	checkAllReleasable(t, c, sb, true)
}

func testSourceDateEpochLocalExporter(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureSourceDateEpoch)
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

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
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
	require.Equal(t, fi.ModTime().Format(time.RFC3339), tm.UTC().Format(time.RFC3339))

	fi, err = os.Stat(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, fi.ModTime().Format(time.RFC3339), tm.UTC().Format(time.RFC3339))

	checkAllReleasable(t, c, sb, true)
}

// testSourceDateEpochReset tests that the SOURCE_DATE_EPOCH is reset if exporter option is set
func testSourceDateEpochReset(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
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

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Attrs:  map[string]string{"source-date-epoch": ""},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	tmsX, err := readImageTimestamps(dt)
	require.NoError(t, err)
	tms := tmsX.FromImage

	require.Equal(t, 3, len(tms))

	expected := tm.UTC().Format(time.RFC3339Nano)
	require.NotEqual(t, expected, tms[0])
	require.NotEqual(t, expected, tms[1])
	require.NotEqual(t, expected, tms[2])

	require.Equal(t, tms[0], tms[2])
	require.NotEqual(t, tms[2], tms[1])

	checkAllReleasable(t, c, sb, true)
}

func testSourceDateEpochTarExporter(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureSourceDateEpoch)
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

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	require.Equal(t, 2, len(m))

	require.Equal(t, tm.Format(time.RFC3339), m["foo"].Header.ModTime.Format(time.RFC3339))
	require.Equal(t, tm.Format(time.RFC3339), m["bar"].Header.ModTime.Format(time.RFC3339))

	checkAllReleasable(t, c, sb, true)
}

func buildProvenanceImage(ctx context.Context, t *testing.T, c *Client, sb integration.Sandbox) (string, ocispecs.Platform) {
	t.Helper()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	platform := platforms.Normalize(platforms.DefaultSpec())
	platformKey := platforms.Format(platform)
	target := registry + "/buildkit/testprovenance:latest"

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res := gateway.NewResult()

		st := llb.Scratch().File(
			llb.Mkfile("/greeting", 0600, []byte("hello provenance")),
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
		res.AddRef(platformKey, ref)

		img := ocispecs.Image{
			Platform: platform,
		}
		config, err := json.Marshal(img)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal image config")
		}
		res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterImageConfigKey, platformKey), config)

		expPlatforms := &exptypes.Platforms{
			Platforms: []exptypes.Platform{{ID: platformKey, Platform: platform}},
		}
		dt, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}

	_, err = c.Build(ctx, SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max,version=v1",
		},
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	return target, platform
}

// isSLSAPredicateType reports whether the predicate type represents SLSA provenance.
func isSLSAPredicateType(v string) bool {
	switch v {
	case policyimage.SLSAProvenancePredicateType02, policyimage.SLSAProvenancePredicateType1:
		return true
	default:
		return false
	}
}
