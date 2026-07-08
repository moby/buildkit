package sbom

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestScannerTmpMount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg, err := json.Marshal(ocispecs.Image{
		Config: ocispecs.ImageConfig{
			Cmd: []string{"scan"},
		},
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name          string
		buildPlatform ocispecs.Platform
		mountType     pb.MountType
	}{
		{
			name:          "linux",
			buildPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			mountType:     pb.MountType_TMPFS,
		},
		{
			name:          "windows",
			buildPlatform: ocispecs.Platform{OS: "windows", Architecture: "amd64"},
			mountType:     pb.MountType_BIND,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scanner, err := CreateSBOMScanner(ctx, testMetaResolver{config: cfg}, "example.com/scanner:latest", tc.buildPlatform, sourceresolver.Opt{}, nil)
			require.NoError(t, err)
			require.NotNil(t, scanner)

			att, err := scanner(ctx, tc.name, llb.Scratch(), nil)
			require.NoError(t, err)
			op := scannerExecOp(t, att.Ref)
			require.Equal(t, tc.buildPlatform.OS, op.Platform.OS)

			sourceOp := scannerImageSourceOp(t, att.Ref)
			require.Equal(t, tc.buildPlatform.OS, sourceOp.Platform.OS)

			tmpMount := findMount(t, op.GetExec(), "/tmp")
			require.Equal(t, tc.mountType, tmpMount.MountType)
			require.Equal(t, int64(pb.SkipOutput), tmpMount.Output)

			if tc.mountType == pb.MountType_TMPFS {
				require.NotNil(t, tmpMount.TmpfsOpt)
				require.Nil(t, tmpMount.CacheOpt)
			} else {
				require.Nil(t, tmpMount.TmpfsOpt)
				require.Nil(t, tmpMount.CacheOpt)
				require.Equal(t, "/tmp", tmpMount.Selector)
			}
		})
	}
}

type testMetaResolver struct {
	config []byte
}

func (r testMetaResolver) ResolveSourceMetadata(ctx context.Context, op *pb.SourceOp, opt sourceresolver.Opt) (*sourceresolver.MetaResponse, error) {
	return &sourceresolver.MetaResponse{
		Op: op,
		Image: &sourceresolver.ResolveImageResponse{
			Digest: digest.FromString("scanner"),
			Config: r.config,
		},
	}, nil
}

func scannerExecOp(t *testing.T, st *llb.State) *pb.Op {
	t.Helper()

	def, err := st.Marshal(t.Context())
	require.NoError(t, err)

	for _, dt := range def.Def {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(dt))
		if op.GetExec() != nil {
			return &op
		}
	}
	require.FailNow(t, "scanner exec op not found")
	return nil
}

func scannerImageSourceOp(t *testing.T, st *llb.State) *pb.Op {
	t.Helper()

	def, err := st.Marshal(t.Context())
	require.NoError(t, err)

	for _, dt := range def.Def {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(dt))
		if src := op.GetSource(); src != nil && strings.HasPrefix(src.Identifier, "docker-image://") {
			return &op
		}
	}
	require.FailNow(t, "scanner image source op not found")
	return nil
}

func findMount(t *testing.T, exec *pb.ExecOp, dest string) *pb.Mount {
	t.Helper()

	for _, mount := range exec.Mounts {
		if mount.Dest == dest {
			return mount
		}
	}
	require.FailNow(t, "mount not found", "dest=%s", dest)
	return nil
}
