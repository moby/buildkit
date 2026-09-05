package llbsolver

import (
	_ "embed"
	"fmt"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/entitlements"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecomputeDigests(t *testing.T) {
	op1 := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}
	oldData, err := op1.Marshal()
	require.NoError(t, err)
	oldDigest := digest.FromBytes(oldData)

	op1.GetOp().(*pb.Op_Source).Source.Identifier = "docker-image://docker.io/library/busybox:1.31.1"
	newData, err := op1.Marshal()
	require.NoError(t, err)
	newDigest := digest.FromBytes(newData)

	op2 := &pb.Op{
		Inputs: []*pb.Input{
			{Digest: string(oldDigest)}, // Input is the old digest, this should be updated after recomputeDigests
		},
	}
	op2Data, err := op2.Marshal()
	require.NoError(t, err)
	op2Digest := digest.FromBytes(op2Data)

	all := map[digest.Digest]*op{
		newDigest: {Op: op1},
		op2Digest: {Op: op2},
	}
	visited := map[digest.Digest]digest.Digest{oldDigest: newDigest}

	updated, err := recomputeDigests(t.Context(), all, visited, op2Digest)
	require.NoError(t, err)
	require.Len(t, visited, 2)
	require.Len(t, all, 2)
	assert.Equal(t, op1, all[newDigest].Op)
	require.Equal(t, newDigest, visited[oldDigest])
	require.Equal(t, op1, all[newDigest].Op)
	assert.Equal(t, op2, all[updated].Op)
	require.Equal(t, newDigest, digest.Digest(op2.Inputs[0].Digest))
	assert.NotEqual(t, op2Digest, updated)
}

//go:embed testdata/gogoproto.data
var gogoprotoData []byte

func TestIngestDigest(t *testing.T) {
	op1 := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}
	op1Data, err := op1.Marshal()
	require.NoError(t, err)
	op1Digest := digest.FromBytes(op1Data)

	op2 := &pb.Op{
		Inputs: []*pb.Input{
			{Digest: string(op1Digest)}, // Input is the old digest, this should be updated after recomputeDigests
		},
	}
	op2Data, err := op2.Marshal()
	require.NoError(t, err)
	op2Digest := digest.FromBytes(op2Data)

	var def pb.Definition
	err = def.Unmarshal(gogoprotoData)
	require.NoError(t, err)
	require.Len(t, def.Def, 2)

	// Read the definition from the test data and ensure it uses the
	// canonical digests after recompute.
	var lastDgst digest.Digest
	all := map[digest.Digest]*op{}
	for _, in := range def.Def {
		opNew := new(pb.Op)
		err := opNew.Unmarshal(in)
		require.NoError(t, err)

		lastDgst = digest.FromBytes(in)
		all[lastDgst] = &op{Op: opNew}
	}
	fmt.Println(all, lastDgst)

	visited := map[digest.Digest]digest.Digest{}
	newDgst, err := recomputeDigests(t.Context(), all, visited, lastDgst)
	require.NoError(t, err)
	require.Len(t, visited, 2)
	require.Equal(t, op2Digest, newDgst)
	require.Equal(t, op2Digest, visited[newDgst])
	delete(visited, newDgst)

	// Last element should correspond to op1.
	// The old digest doesn't really matter.
	require.Len(t, visited, 1)
	for _, newDgst := range visited {
		require.Equal(t, op1Digest, newDgst)
	}
}

func TestWithProxyNetworkAffectsVertexDigest(t *testing.T) {
	def := proxyNetworkTestDefinition(t)

	defaultEdge, err := Load(t.Context(), def, nil)
	require.NoError(t, err)
	defaultOp := requireVertexOp(t, defaultEdge.Vertex)
	require.Equal(t, pb.NetMode_UNSET, defaultOp.GetExec().Network)

	proxyEdge, err := loadWithProxyNetwork(t.Context(), def, nil, true)
	require.NoError(t, err)
	proxyOp := requireVertexOp(t, proxyEdge.Vertex)
	require.Equal(t, pb.NetMode_UNSET, proxyOp.GetExec().Network)

	require.NotEqual(t, defaultEdge.Vertex.Digest(), proxyEdge.Vertex.Digest())
}

func TestNormalizeRuntimePlatformsDoesNotAffectVertexDigest(t *testing.T) {
	def := proxyNetworkTestDefinition(t)

	defaultEdge, err := Load(t.Context(), def, nil)
	require.NoError(t, err)

	normalizedEdge, err := Load(t.Context(), def, nil, NormalizeRuntimePlatforms())
	require.NoError(t, err)
	normalizedOp := requireVertexOp(t, normalizedEdge.Vertex)
	require.NotNil(t, normalizedOp.Platform)

	require.Equal(t, defaultEdge.Vertex.Digest(), normalizedEdge.Vertex.Digest())
}

func TestWithProxyNetworkPreservesNoneMode(t *testing.T) {
	def := proxyNetworkTestDefinition(t, func(exec *pb.ExecOp) {
		exec.Network = pb.NetMode_NONE
	})

	defaultEdge, err := Load(t.Context(), def, nil)
	require.NoError(t, err)

	proxyEdge, err := loadWithProxyNetwork(t.Context(), def, nil, true)
	require.NoError(t, err)
	proxyOp := requireVertexOp(t, proxyEdge.Vertex)
	require.Equal(t, pb.NetMode_NONE, proxyOp.GetExec().Network)
	require.Equal(t, defaultEdge.Vertex.Digest(), proxyEdge.Vertex.Digest())
}

func TestWithProxyNetworkPreservesHostMode(t *testing.T) {
	def := proxyNetworkTestDefinition(t, func(exec *pb.ExecOp) {
		exec.Network = pb.NetMode_HOST
	})

	defaultEdge, err := Load(t.Context(), def, nil)
	require.NoError(t, err)

	proxyEdge, err := loadWithProxyNetwork(t.Context(), def, nil, true)
	require.NoError(t, err)
	proxyOp := requireVertexOp(t, proxyEdge.Vertex)
	require.Equal(t, pb.NetMode_HOST, proxyOp.GetExec().Network)
	require.NotEqual(t, defaultEdge.Vertex.Digest(), proxyEdge.Vertex.Digest())
}

func TestWithProxyNetworkHostEgressRequiresEntitlement(t *testing.T) {
	def := proxyNetworkTestDefinition(t, func(exec *pb.ExecOp) {
		exec.Network = pb.NetMode_HOST
	})

	_, err := loadWithProxyNetwork(t.Context(), def, nil, true, ValidateEntitlements(entitlements.Set{}, nil))
	require.Error(t, err)
	require.ErrorContains(t, err, "network.host is not allowed")

	_, err = loadWithProxyNetwork(t.Context(), def, nil, true, ValidateEntitlements(entitlements.Set{
		entitlements.EntitlementNetworkHost: nil,
	}, nil))
	require.NoError(t, err)
}

func TestValidateEntitlementsRejectsDevicesWhenCDIDisabled(t *testing.T) {
	def := proxyNetworkTestDefinition(t, func(exec *pb.ExecOp) {
		exec.CdiDevices = []*pb.CDIDevice{
			{Name: "example.invalid/device=optional", Optional: true},
			{Name: "example.invalid/device=required"},
		}
	})
	setExecCustomName(t, def, "RUN --device=example.invalid/device=required true")

	for _, tt := range []struct {
		name         string
		entitlements entitlements.Set
	}{
		{
			name:         "no device entitlement",
			entitlements: entitlements.Set{},
		},
		{
			name: "unrestricted device entitlement",
			entitlements: entitlements.Set{
				entitlements.EntitlementDevice: &entitlements.DevicesConfig{All: true},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(t.Context(), def, nil, ValidateEntitlements(tt.entitlements, nil))
			require.EqualError(t, err, `CDI device "example.invalid/device=required" is required by step "RUN --device=example.invalid/device=required true", but CDI device support is disabled`)
		})
	}
}

func TestValidateEntitlementsDropsOptionalDevicesWhenCDIDisabled(t *testing.T) {
	def := proxyNetworkTestDefinition(t, func(exec *pb.ExecOp) {
		exec.CdiDevices = []*pb.CDIDevice{{Name: "example.invalid/device=optional", Optional: true}}
	})

	edge, err := Load(t.Context(), def, nil, ValidateEntitlements(entitlements.Set{}, nil))
	require.NoError(t, err)
	require.Empty(t, requireVertexOp(t, edge.Vertex).GetExec().CdiDevices)
}

func TestBridgeUsesDefaultProxyNetwork(t *testing.T) {
	s := &Solver{proxyNetwork: true}

	br := s.bridge(nil)

	require.True(t, br.proxyNetwork)
}

func TestLoadRejectsNegativeInputIndex(t *testing.T) {
	for _, tt := range []struct {
		name           string
		execInputIndex int64
		rootInputIndex int64
	}{
		{
			name:           "vertex input",
			execInputIndex: -1,
			rootInputIndex: 0,
		},
		{
			name:           "root input",
			execInputIndex: 0,
			rootInputIndex: -1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(t.Context(), inputIndexTestDefinition(t, tt.execInputIndex, tt.rootInputIndex), nil)
			require.ErrorContains(t, err, "invalid input 0 output index -1")
		})
	}
}

func TestLoadRejectsDependencyCountMismatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		op      func(digest.Digest) *pb.Op
		wantErr string
	}{
		{
			name: "source",
			op: func(input digest.Digest) *pb.Op {
				return &pb.Op{
					Inputs: []*pb.Input{{Digest: string(input)}},
					Op:     &pb.Op_Source{Source: &pb.SourceOp{Identifier: "local://malformed"}},
				}
			},
			wantErr: "invalid source op with 1 inputs",
		},
		{
			name: "diff",
			op: func(input digest.Digest) *pb.Op {
				return &pb.Op{
					Inputs: []*pb.Input{{Digest: string(input)}},
					Op: &pb.Op_Diff{Diff: &pb.DiffOp{
						Lower: &pb.LowerDiffInput{Input: int64(pb.Empty)},
						Upper: &pb.UpperDiffInput{Input: int64(pb.Empty)},
					}},
				}
			},
			wantErr: "invalid diff op with 0 inner inputs and 1 outer inputs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseDigest, baseBytes := marshalTestOp(t, &pb.Op{
				Op: &pb.Op_Source{Source: &pb.SourceOp{Identifier: "local://base"}},
			})
			malformedDigest, malformedBytes := marshalTestOp(t, tc.op(baseDigest))
			_, rootBytes := marshalTestOp(t, &pb.Op{
				Inputs: []*pb.Input{{Digest: string(malformedDigest)}},
			})

			_, err := Load(t.Context(), &pb.Definition{
				Def: [][]byte{baseBytes, malformedBytes, rootBytes},
			}, nil)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func proxyNetworkTestDefinition(t *testing.T, opts ...func(*pb.ExecOp)) *pb.Definition {
	t.Helper()
	source := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{Identifier: "local://context"},
		},
	}
	sourceDigest, sourceBytes := marshalTestOp(t, source)

	exec := &pb.Op{
		Inputs: []*pb.Input{{Digest: string(sourceDigest)}},
		Op: &pb.Op_Exec{
			Exec: &pb.ExecOp{
				Meta: &pb.Meta{Args: []string{"true"}},
				Mounts: []*pb.Mount{{
					Input: 0,
					Dest:  pb.RootMount,
				}},
			},
		},
	}
	for _, opt := range opts {
		opt(exec.GetExec())
	}
	execDigest, execBytes := marshalTestOp(t, exec)

	root := &pb.Op{
		Inputs: []*pb.Input{{Digest: string(execDigest)}},
	}
	_, rootBytes := marshalTestOp(t, root)

	return &pb.Definition{Def: [][]byte{sourceBytes, execBytes, rootBytes}}
}

func inputIndexTestDefinition(t *testing.T, execInputIndex, rootInputIndex int64) *pb.Definition {
	t.Helper()
	source := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{Identifier: "local://context"},
		},
	}
	sourceDigest, sourceBytes := marshalTestOp(t, source)

	exec := &pb.Op{
		Inputs: []*pb.Input{{Digest: string(sourceDigest), Index: execInputIndex}},
		Op: &pb.Op_Exec{
			Exec: &pb.ExecOp{
				Meta: &pb.Meta{Args: []string{"true"}},
				Mounts: []*pb.Mount{{
					Input: 0,
					Dest:  pb.RootMount,
				}},
			},
		},
	}
	execDigest, execBytes := marshalTestOp(t, exec)

	root := &pb.Op{
		Inputs: []*pb.Input{{Digest: string(execDigest), Index: rootInputIndex}},
	}
	_, rootBytes := marshalTestOp(t, root)

	return &pb.Definition{Def: [][]byte{sourceBytes, execBytes, rootBytes}}
}

func marshalTestOp(t *testing.T, op *pb.Op) (digest.Digest, []byte) {
	t.Helper()
	dt, err := op.Marshal()
	require.NoError(t, err)
	return digest.FromBytes(dt), dt
}

func setExecCustomName(t *testing.T, def *pb.Definition, name string) {
	t.Helper()
	for _, dt := range def.Def {
		op := new(pb.Op)
		require.NoError(t, op.Unmarshal(dt))
		if op.GetExec() == nil {
			continue
		}
		def.Metadata = map[string]*pb.OpMetadata{
			digest.FromBytes(dt).String(): {
				Description: map[string]string{"llb.customname": name},
			},
		}
		return
	}
	require.FailNow(t, "definition does not contain an exec op")
}

func requireVertexOp(t *testing.T, v interface{ Sys() any }) *pb.Op {
	t.Helper()
	op, ok := v.Sys().(*pb.Op)
	require.True(t, ok)
	return op
}
