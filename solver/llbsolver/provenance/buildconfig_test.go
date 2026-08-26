package provenance

import (
	"testing"

	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/moby/buildkit/solver"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestProxyNetworkResourceUsageKeepsBuildStepAssociation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy bool
	}{
		{name: "disabled", proxy: false},
		{name: "enabled", proxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def, definitionDigest, runtimeDigest := proxyNetworkBuildConfigDefinition(t, tc.proxy)
			want := &resourcestypes.Samples{Samples: []*resourcestypes.Sample{{}}}
			capture := &Capture{Samples: map[digest.Digest]*resourcestypes.Samples{runtimeDigest: want}}
			if tc.proxy {
				capture.DigestMapping = map[digest.Digest]digest.Digest{definitionDigest: runtimeDigest}
			}

			steps, _, err := toBuildSteps(def, capture, true)
			require.NoError(t, err)
			for _, step := range steps {
				if step.Op.GetExec() != nil {
					require.Same(t, want, step.ResourceUsage)
					return
				}
			}
			t.Fatal("exec build step not found")
		})
	}
}

func TestProxyNetworkLayerKeepsBuildStepAssociation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy bool
	}{
		{name: "disabled", proxy: false},
		{name: "enabled", proxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def, definitionDigest, runtimeDigest := proxyNetworkBuildConfigDefinition(t, tc.proxy)
			capture := &Capture{}
			if tc.proxy {
				capture.DigestMapping = map[digest.Digest]digest.Digest{definitionDigest: runtimeDigest}
			}
			predicate := &provenancetypes.ProvenancePredicateSLSA1{}
			indexes, err := AddBuildConfig(t.Context(), predicate, capture, &definitionResultProxy{definition: def}, false)
			require.NoError(t, err)

			definitionIndex, ok := indexes[definitionDigest]
			require.True(t, ok)
			runtimeIndex, ok := indexes[runtimeDigest]
			require.True(t, ok)
			require.Equal(t, definitionIndex, runtimeIndex)
		})
	}
}

type definitionResultProxy struct {
	solver.ResultProxy
	definition *pb.Definition
}

func (p *definitionResultProxy) Definition() *pb.Definition {
	return p.definition
}

func proxyNetworkBuildConfigDefinition(t *testing.T, proxy bool) (*pb.Definition, digest.Digest, digest.Digest) {
	t.Helper()
	marshal := func(op *pb.Op) (digest.Digest, []byte) {
		dt, err := op.Marshal()
		require.NoError(t, err)
		return digest.FromBytes(dt), dt
	}

	sourceDigest, sourceBytes := marshal(&pb.Op{Op: &pb.Op_Source{Source: &pb.SourceOp{Identifier: "local://context"}}})
	execDigest, execBytes := marshal(&pb.Op{
		Inputs: []*pb.Input{{Digest: sourceDigest.String()}},
		Op: &pb.Op_Exec{Exec: &pb.ExecOp{
			Meta:   &pb.Meta{Args: []string{"true"}},
			Mounts: []*pb.Mount{{Input: 0, Dest: pb.RootMount}},
		}},
	})
	_, rootBytes := marshal(&pb.Op{Inputs: []*pb.Input{{Digest: execDigest.String()}}})

	runtimeDigest := execDigest
	if proxy {
		salted := append([]byte(nil), execBytes...)
		salted = append(salted, []byte("\x00buildkit.proxy-network.v0")...)
		runtimeDigest = digest.FromBytes(salted)
	}
	return &pb.Definition{Def: [][]byte{sourceBytes, execBytes, rootBytes}}, execDigest, runtimeDigest
}
