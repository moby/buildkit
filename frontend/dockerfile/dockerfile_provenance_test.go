package dockerfile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/platforms"
	"github.com/containerd/continuity/fs/fstest"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testProvenanceAttestation(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureDirectPush, integration.FeatureProvenance)
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
RUN echo "ok" > /foo
`)
	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	for _, mode := range []string{"", "min", "max"} {
		t.Run(mode, func(t *testing.T) {
			var target string
			if target == "" {
				target = registry + "/buildkit/testwithprovenance:none"
			} else {
				target = registry + "/buildkit/testwithprovenance:" + mode
			}

			provReq := ""
			if mode != "" {
				provReq = "mode=" + mode
			}
			_, err = f.Solve(sb.Context(), c, client.SolveOpt{
				LocalDirs: map[string]string{
					dockerui.DefaultLocalNameDockerfile: dir,
					dockerui.DefaultLocalNameContext:    dir,
				},
				FrontendAttrs: map[string]string{
					"attest:provenance": provReq,
					"build-arg:FOO":     "bar",
					"label:lbl":         "abc",
					"vcs:source":        "https://user:pass@example.invalid/repo.git",
					"vcs:revision":      "123456",
					"filename":          "Dockerfile",
					dockerui.DefaultLocalNameContext + ":foo": "https://foo:bar@example.invalid/foo.html",
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
			require.Equal(t, []byte("ok\n"), img.Layers[1]["foo"].Data)

			att := imgs.Find("unknown/unknown")
			require.NotNil(t, att)
			require.Equal(t, att.Desc.Annotations["vnd.docker.reference.digest"], string(img.Desc.Digest))
			require.Equal(t, att.Desc.Annotations["vnd.docker.reference.type"], "attestation-manifest")
			var attest intoto.Statement
			require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
			require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
			require.Equal(t, "https://slsa.dev/provenance/v0.2", attest.PredicateType) // intentionally not const

			type stmtT struct {
				Predicate provenance.ProvenancePredicate `json:"predicate"`
			}
			var stmt stmtT
			require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
			pred := stmt.Predicate

			require.Equal(t, "https://mobyproject.org/buildkit@v1", pred.BuildType)
			require.Equal(t, "", pred.Builder.ID)

			require.Equal(t, "", pred.Invocation.ConfigSource.URI)

			_, isClient := f.(*clientFrontend)
			_, isGateway := f.(*gatewayFrontend)

			args := pred.Invocation.Parameters.Args
			if isClient {
				require.Equal(t, "", pred.Invocation.Parameters.Frontend)
				require.Equal(t, 0, len(args), "%v", args)
				require.False(t, pred.Metadata.Completeness.Parameters)
				require.Equal(t, "", pred.Invocation.ConfigSource.EntryPoint)
			} else if isGateway {
				require.Equal(t, "gateway.v0", pred.Invocation.Parameters.Frontend)

				if mode == "max" || mode == "" {
					require.Equal(t, 4, len(args), "%v", args)
					require.True(t, pred.Metadata.Completeness.Parameters)

					require.Equal(t, "bar", args["build-arg:FOO"])
					require.Equal(t, "abc", args["label:lbl"])
					require.Contains(t, args["source"], "buildkit_test/")
				} else {
					require.False(t, pred.Metadata.Completeness.Parameters)
					require.Equal(t, 2, len(args), "%v", args)
					require.Contains(t, args["source"], "buildkit_test/")
				}
				require.Equal(t, "https://xxxxx:xxxxx@example.invalid/foo.html", args["context:foo"])
			} else {
				require.Equal(t, "dockerfile.v0", pred.Invocation.Parameters.Frontend)

				if mode == "max" || mode == "" {
					require.Equal(t, 3, len(args))
					require.True(t, pred.Metadata.Completeness.Parameters)

					require.Equal(t, "bar", args["build-arg:FOO"])
					require.Equal(t, "abc", args["label:lbl"])
				} else {
					require.False(t, pred.Metadata.Completeness.Parameters)
					require.Equal(t, 1, len(args), "%v", args)
				}
				require.Equal(t, "https://xxxxx:xxxxx@example.invalid/foo.html", args["context:foo"])
			}

			expectedBase := "pkg:docker/busybox@latest?platform=" + url.PathEscape(platforms.Format(platforms.Normalize(platforms.DefaultSpec())))
			if isGateway {
				require.Equal(t, 2, len(pred.Materials), "%+v", pred.Materials)
				require.Contains(t, pred.Materials[0].URI, "docker/buildkit_test")
				require.Equal(t, expectedBase, pred.Materials[1].URI)
				require.NotEmpty(t, pred.Materials[1].Digest["sha256"])
			} else {
				require.Equal(t, 1, len(pred.Materials), "%+v", pred.Materials)
				require.Equal(t, expectedBase, pred.Materials[0].URI)
				require.NotEmpty(t, pred.Materials[0].Digest["sha256"])
			}

			if !isClient {
				require.Equal(t, "Dockerfile", pred.Invocation.ConfigSource.EntryPoint)
				require.Equal(t, "https://xxxxx:xxxxx@example.invalid/repo.git", pred.Metadata.BuildKitMetadata.VCS["source"])
				require.Equal(t, "123456", pred.Metadata.BuildKitMetadata.VCS["revision"])
			}

			require.NotEmpty(t, pred.Metadata.BuildInvocationID)

			require.Equal(t, 2, len(pred.Invocation.Parameters.Locals), "%+v", pred.Invocation.Parameters.Locals)
			require.Equal(t, "context", pred.Invocation.Parameters.Locals[0].Name)
			require.Equal(t, "dockerfile", pred.Invocation.Parameters.Locals[1].Name)

			require.NotNil(t, pred.Metadata.BuildFinishedOn)
			require.True(t, time.Since(*pred.Metadata.BuildFinishedOn) < 5*time.Minute)
			require.NotNil(t, pred.Metadata.BuildStartedOn)
			require.True(t, time.Since(*pred.Metadata.BuildStartedOn) < 5*time.Minute)
			require.True(t, pred.Metadata.BuildStartedOn.Before(*pred.Metadata.BuildFinishedOn))

			require.True(t, pred.Metadata.Completeness.Environment)
			require.Equal(t, platforms.Format(platforms.Normalize(platforms.DefaultSpec())), pred.Invocation.Environment.Platform)

			require.False(t, pred.Metadata.Completeness.Materials)
			require.False(t, pred.Metadata.Reproducible)
			require.False(t, pred.Metadata.Hermetic)

			if mode == "max" || mode == "" {
				require.Equal(t, 2, len(pred.Metadata.BuildKitMetadata.Layers))
				require.NotNil(t, pred.Metadata.BuildKitMetadata.Source)
				require.Equal(t, "Dockerfile", pred.Metadata.BuildKitMetadata.Source.Infos[0].Filename)
				require.Equal(t, dockerfile, pred.Metadata.BuildKitMetadata.Source.Infos[0].Data)
				require.NotNil(t, pred.BuildConfig)

				require.Equal(t, 3, len(pred.BuildConfig.Definition))
			} else {
				require.Equal(t, 0, len(pred.Metadata.BuildKitMetadata.Layers))
				require.Nil(t, pred.Metadata.BuildKitMetadata.Source)
				require.Nil(t, pred.BuildConfig)
			}
		})
	}
}

func testGitProvenanceAttestation(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureDirectPush, integration.FeatureProvenance)
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
RUN --network=none echo "git" > /foo
COPY myapp.Dockerfile /
`)
	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("myapp.Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	err = runShell(dir,
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"git add myapp.Dockerfile",
		"git commit -m initial",
		"git branch v1",
		"git update-server-info",
	)
	require.NoError(t, err)

	cmd := exec.Command("git", "rev-parse", "v1")
	cmd.Dir = dir
	expectedGitSHA, err := cmd.Output()
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(dir))))
	defer server.Close()

	target := registry + "/buildkit/testwithprovenance:git"

	// inject dummy credentials to test that they are masked
	expectedURL := strings.Replace(server.URL, "http://", "http://xxxxx:xxxxx@", 1)
	require.NotEqual(t, expectedURL, server.URL)
	server.URL = strings.Replace(server.URL, "http://", "http://user:pass@", 1)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":           server.URL + "/.git#v1",
			"attest:provenance": "",
			"filename":          "myapp.Dockerfile",
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
	require.Equal(t, []byte("git\n"), img.Layers[1]["foo"].Data)

	att := imgs.Find("unknown/unknown")
	require.NotNil(t, att)
	require.Equal(t, att.Desc.Annotations["vnd.docker.reference.digest"], string(img.Desc.Digest))
	require.Equal(t, att.Desc.Annotations["vnd.docker.reference.type"], "attestation-manifest")
	var attest intoto.Statement
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
	require.Equal(t, "https://slsa.dev/provenance/v0.2", attest.PredicateType) // intentionally not const

	type stmtT struct {
		Predicate provenance.ProvenancePredicate `json:"predicate"`
	}
	var stmt stmtT
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
	pred := stmt.Predicate

	_, isClient := f.(*clientFrontend)
	_, isGateway := f.(*gatewayFrontend)

	if isClient {
		require.Empty(t, pred.Invocation.Parameters.Frontend)
		require.Equal(t, "", pred.Invocation.ConfigSource.URI)
		require.Equal(t, "", pred.Invocation.ConfigSource.EntryPoint)
	} else {
		require.NotEmpty(t, pred.Invocation.Parameters.Frontend)
		require.Equal(t, expectedURL+"/.git#v1", pred.Invocation.ConfigSource.URI)
		require.Equal(t, "myapp.Dockerfile", pred.Invocation.ConfigSource.EntryPoint)
	}

	expBase := "pkg:docker/busybox@latest?platform=" + url.PathEscape(platforms.Format(platforms.Normalize(platforms.DefaultSpec())))
	if isGateway {
		require.Equal(t, 3, len(pred.Materials), "%+v", pred.Materials)

		require.Contains(t, pred.Materials[0].URI, "pkg:docker/buildkit_test/")
		require.NotEmpty(t, pred.Materials[0].Digest)

		require.Equal(t, expBase, pred.Materials[1].URI)
		require.NotEmpty(t, pred.Materials[1].Digest["sha256"])

		require.Equal(t, expectedURL+"/.git#v1", pred.Materials[2].URI)
		require.Equal(t, strings.TrimSpace(string(expectedGitSHA)), pred.Materials[2].Digest["sha1"])
	} else {
		require.Equal(t, 2, len(pred.Materials), "%+v", pred.Materials)

		require.Equal(t, expBase, pred.Materials[0].URI)
		require.NotEmpty(t, pred.Materials[0].Digest["sha256"])

		require.Equal(t, expectedURL+"/.git#v1", pred.Materials[1].URI)
		require.Equal(t, strings.TrimSpace(string(expectedGitSHA)), pred.Materials[1].Digest["sha1"])
	}

	require.Equal(t, 0, len(pred.Invocation.Parameters.Locals))

	require.True(t, pred.Metadata.Completeness.Materials)
	require.True(t, pred.Metadata.Completeness.Environment)
	require.True(t, pred.Metadata.Hermetic)

	if isClient {
		require.False(t, pred.Metadata.Completeness.Parameters)
	} else {
		require.True(t, pred.Metadata.Completeness.Parameters)
	}
	require.False(t, pred.Metadata.Reproducible)

	require.Equal(t, 0, len(pred.Metadata.BuildKitMetadata.VCS), "%+v", pred.Metadata.BuildKitMetadata.VCS)
}

func testMultiPlatformProvenance(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureDirectPush, integration.FeatureMultiPlatform, integration.FeatureProvenance)
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
ARG TARGETARCH
RUN echo "ok-$TARGETARCH" > /foo
`)
	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	target := registry + "/buildkit/testmultiprovenance:latest"

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max",
			"build-arg:FOO":     "bar",
			"label:lbl":         "abc",
			"platform":          "linux/amd64,linux/arm64",
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
	require.Equal(t, 4, len(imgs.Images))

	_, isClient := f.(*clientFrontend)
	_, isGateway := f.(*gatewayFrontend)

	for _, p := range []string{"linux/amd64", "linux/arm64"} {
		img := imgs.Find(p)
		require.NotNil(t, img)
		if p == "linux/amd64" {
			require.Equal(t, []byte("ok-amd64\n"), img.Layers[1]["foo"].Data)
		} else {
			require.Equal(t, []byte("ok-arm64\n"), img.Layers[1]["foo"].Data)
		}

		att := imgs.FindAttestation(p)
		require.NotNil(t, att)
		require.Equal(t, att.Desc.Annotations["vnd.docker.reference.type"], "attestation-manifest")
		var attest intoto.Statement
		require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
		require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
		require.Equal(t, "https://slsa.dev/provenance/v0.2", attest.PredicateType) // intentionally not const

		type stmtT struct {
			Predicate provenance.ProvenancePredicate `json:"predicate"`
		}
		var stmt stmtT
		require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
		pred := stmt.Predicate

		require.Equal(t, "https://mobyproject.org/buildkit@v1", pred.BuildType)
		require.Equal(t, "", pred.Builder.ID)
		require.Equal(t, "", pred.Invocation.ConfigSource.URI)

		if isGateway {
			require.Equal(t, 2, len(pred.Materials), "%+v", pred.Materials)
			require.Contains(t, pred.Materials[0].URI, "buildkit_test")
			require.Contains(t, pred.Materials[1].URI, "pkg:docker/busybox@latest")
			require.Contains(t, pred.Materials[1].URI, url.PathEscape(p))
		} else {
			require.Equal(t, 1, len(pred.Materials), "%+v", pred.Materials)
			require.Contains(t, pred.Materials[0].URI, "pkg:docker/busybox@latest")
			require.Contains(t, pred.Materials[0].URI, url.PathEscape(p))
		}

		args := pred.Invocation.Parameters.Args
		if isClient {
			require.Equal(t, 0, len(args), "%+v", args)
		} else if isGateway {
			require.Equal(t, 3, len(args), "%+v", args)
			require.Equal(t, "bar", args["build-arg:FOO"])
			require.Equal(t, "abc", args["label:lbl"])
			require.Contains(t, args["source"], "buildkit_test/")
		} else {
			require.Equal(t, 2, len(args), "%+v", args)
			require.Equal(t, "bar", args["build-arg:FOO"])
			require.Equal(t, "abc", args["label:lbl"])
		}
	}
}

func testClientFrontendProvenance(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureDirectPush, integration.FeatureProvenance)
	// Building with client frontend does not capture frontend provenance
	// because frontend runs in client, not in BuildKit.
	// This test builds Dockerfile inside a client frontend ensuring that
	// in that case frontend provenance is captured.
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/clientprovenance:latest"

	f := getFrontend(t, sb)

	_, isClient := f.(*clientFrontend)
	if !isClient {
		t.Skip("not a client frontend")
	}

	dockerfile := []byte(`
	FROM alpine as x86target
	RUN echo "alpine" > /foo

	FROM busybox:latest AS armtarget
	RUN --network=none echo "bbox" > /foo
	`)
	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.HTTP("https://raw.githubusercontent.com/moby/moby/v20.10.21/README.md")
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		// This does not show up in provenance
		res0, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		dt, err := res0.Ref.ReadFile(ctx, gateway.ReadRequest{
			Filename: "README.md",
		})
		if err != nil {
			return nil, err
		}

		res1, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendOpt: map[string]string{
				"build-arg:FOO": string(dt[:3]),
				"target":        "armtarget",
			},
		})
		if err != nil {
			return nil, err
		}

		res2, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendOpt: map[string]string{
				"build-arg:FOO": string(dt[4:8]),
				"target":        "x86target",
			},
		})
		if err != nil {
			return nil, err
		}

		res := gateway.NewResult()
		res.AddRef("linux/arm64", res1.Ref)
		res.AddRef("linux/amd64", res2.Ref)

		pl, err := json.Marshal(exptypes.Platforms{
			Platforms: []exptypes.Platform{
				{
					ID:       "linux/arm64",
					Platform: ocispecs.Platform{OS: "linux", Architecture: "arm64"},
				},
				{
					ID:       "linux/amd64",
					Platform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, pl)

		return res, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=full",
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
		LocalDirs: map[string]string{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 4, len(imgs.Images))

	img := imgs.Find("linux/arm64")
	require.NotNil(t, img)
	require.Equal(t, []byte("bbox\n"), img.Layers[1]["foo"].Data)

	att := imgs.FindAttestation("linux/arm64")
	require.NotNil(t, att)
	require.Equal(t, att.Desc.Annotations["vnd.docker.reference.type"], "attestation-manifest")
	var attest intoto.Statement
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
	require.Equal(t, "https://slsa.dev/provenance/v0.2", attest.PredicateType) // intentionally not const

	type stmtT struct {
		Predicate provenance.ProvenancePredicate `json:"predicate"`
	}
	var stmt stmtT
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
	pred := stmt.Predicate

	require.Equal(t, "https://mobyproject.org/buildkit@v1", pred.BuildType)
	require.Equal(t, "", pred.Builder.ID)
	require.Equal(t, "", pred.Invocation.ConfigSource.URI)

	args := pred.Invocation.Parameters.Args
	require.Equal(t, 2, len(args), "%+v", args)
	require.Equal(t, "The", args["build-arg:FOO"])
	require.Equal(t, "armtarget", args["target"])

	require.Equal(t, 2, len(pred.Invocation.Parameters.Locals))
	require.Equal(t, 1, len(pred.Materials))
	require.Contains(t, pred.Materials[0].URI, "docker/busybox")

	// amd64
	img = imgs.Find("linux/amd64")
	require.NotNil(t, img)
	require.Equal(t, []byte("alpine\n"), img.Layers[1]["foo"].Data)

	att = imgs.FindAttestation("linux/amd64")
	require.NotNil(t, att)
	require.Equal(t, att.Desc.Annotations["vnd.docker.reference.type"], "attestation-manifest")
	attest = intoto.Statement{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
	require.Equal(t, "https://slsa.dev/provenance/v0.2", attest.PredicateType) // intentionally not const

	stmt = stmtT{}
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
	pred = stmt.Predicate

	require.Equal(t, "https://mobyproject.org/buildkit@v1", pred.BuildType)
	require.Equal(t, "", pred.Builder.ID)
	require.Equal(t, "", pred.Invocation.ConfigSource.URI)

	args = pred.Invocation.Parameters.Args
	require.Equal(t, 2, len(args), "%+v", args)
	require.Equal(t, "Moby", args["build-arg:FOO"])
	require.Equal(t, "x86target", args["target"])

	require.Equal(t, 2, len(pred.Invocation.Parameters.Locals))
	require.Equal(t, 1, len(pred.Materials))
	require.Contains(t, pred.Materials[0].URI, "docker/alpine")
}

func testClientLLBProvenance(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureDirectPush, integration.FeatureProvenance)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/clientprovenance:llb"

	f := getFrontend(t, sb)

	_, isClient := f.(*clientFrontend)
	if !isClient {
		t.Skip("not a client frontend")
	}

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.HTTP("https://raw.githubusercontent.com/moby/moby/v20.10.21/README.md")
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		// this also shows up in the provenance
		res0, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		dt, err := res0.Ref.ReadFile(ctx, gateway.ReadRequest{
			Filename: "README.md",
		})
		if err != nil {
			return nil, err
		}

		st = llb.Image("alpine").File(llb.Mkfile("/foo", 0600, dt))
		def, err = st.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		res1, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		return res1, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=full",
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
		LocalDirs: map[string]string{},
	}, "", frontend, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.Equal(t, 2, len(imgs.Images))

	nativePlatform := platforms.Format(platforms.Normalize(platforms.DefaultSpec()))

	img := imgs.Find(nativePlatform)
	require.NotNil(t, img)
	require.Contains(t, string(img.Layers[1]["foo"].Data), "The Moby Project")

	att := imgs.FindAttestation(nativePlatform)
	require.NotNil(t, att)
	require.Equal(t, att.Desc.Annotations["vnd.docker.reference.type"], "attestation-manifest")
	var attest intoto.Statement
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &attest))
	require.Equal(t, "https://in-toto.io/Statement/v0.1", attest.Type)
	require.Equal(t, "https://slsa.dev/provenance/v0.2", attest.PredicateType) // intentionally not const

	type stmtT struct {
		Predicate provenance.ProvenancePredicate `json:"predicate"`
	}
	var stmt stmtT
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
	pred := stmt.Predicate

	require.Equal(t, "https://mobyproject.org/buildkit@v1", pred.BuildType)
	require.Equal(t, "", pred.Builder.ID)
	require.Equal(t, "", pred.Invocation.ConfigSource.URI)

	args := pred.Invocation.Parameters.Args
	require.Equal(t, 0, len(args), "%+v", args)
	require.Equal(t, 0, len(pred.Invocation.Parameters.Locals))

	require.Equal(t, 2, len(pred.Materials), "%+v", pred.Materials)
	require.Contains(t, pred.Materials[0].URI, "docker/alpine")
	require.Contains(t, pred.Materials[1].URI, "README.md")
}

func testSecretSSHProvenance(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureDirectPush, integration.FeatureProvenance)
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
RUN --mount=type=secret,id=mysecret --mount=type=secret,id=othersecret --mount=type=ssh echo "ok" > /foo
`)
	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	target := registry + "/buildkit/testsecretprovenance:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max",
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

	expPlatform := platforms.Format(platforms.Normalize(platforms.DefaultSpec()))

	img := imgs.Find(expPlatform)
	require.NotNil(t, img)
	require.Equal(t, []byte("ok\n"), img.Layers[1]["foo"].Data)

	att := imgs.FindAttestation(expPlatform)
	type stmtT struct {
		Predicate provenance.ProvenancePredicate `json:"predicate"`
	}
	var stmt stmtT
	require.NoError(t, json.Unmarshal(att.LayersRaw[0], &stmt))
	pred := stmt.Predicate

	require.Equal(t, 2, len(pred.Invocation.Parameters.Secrets), "%+v", pred.Invocation.Parameters.Secrets)
	require.Equal(t, "mysecret", pred.Invocation.Parameters.Secrets[0].ID)
	require.True(t, pred.Invocation.Parameters.Secrets[0].Optional)
	require.Equal(t, "othersecret", pred.Invocation.Parameters.Secrets[1].ID)
	require.True(t, pred.Invocation.Parameters.Secrets[1].Optional)

	require.Equal(t, 1, len(pred.Invocation.Parameters.SSH), "%+v", pred.Invocation.Parameters.SSH)
	require.Equal(t, "default", pred.Invocation.Parameters.SSH[0].ID)
	require.True(t, pred.Invocation.Parameters.SSH[0].Optional)
}

func testNilProvenance(t *testing.T, sb integration.Sandbox) {
	integration.CheckFeatureCompat(t, sb, integration.FeatureProvenance)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
ENV FOO=bar
`)
	dir, err := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max",
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
			},
		},
	}, nil)
	require.NoError(t, err)
}
