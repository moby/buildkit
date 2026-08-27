package ops

import (
	"encoding/json"
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/cachedigest"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestBuildOpCacheMapIncludesProxyNetwork(t *testing.T) {
	vertex := &buildTestVertex{}
	disabled := &BuildOp{op: &pb.BuildOp{}, v: vertex}
	enabled := &BuildOp{op: &pb.BuildOp{}, v: vertex, proxyNetwork: true}

	disabledMap, _, err := disabled.CacheMap(t.Context(), nil, 0)
	require.NoError(t, err)
	enabledMap, _, err := enabled.CacheMap(t.Context(), nil, 0)
	require.NoError(t, err)
	require.NotEqual(t, disabledMap.Digest, enabledMap.Digest)
}

func TestBuildOpCacheMapDoesNotMatchLegacy(t *testing.T) {
	build := &pb.BuildOp{}
	current := &BuildOp{op: build, v: &buildTestVertex{}}

	currentMap, _, err := current.CacheMap(t.Context(), nil, 0)
	require.NoError(t, err)

	legacyPayload, err := json.Marshal(struct {
		Type string
		Exec *pb.BuildOp
	}{
		Type: "buildkit.build.v0",
		Exec: build,
	})
	require.NoError(t, err)
	legacyDigest, err := cachedigest.FromBytes(legacyPayload, cachedigest.TypeJSON)
	require.NoError(t, err)

	require.NotEqual(t, legacyDigest, currentMap.Digest)
}

type buildTestVertex struct{}

func (*buildTestVertex) Digest() digest.Digest {
	return ""
}

func (*buildTestVertex) Sys() any {
	return nil
}

func (*buildTestVertex) Options() solver.VertexOptions {
	return solver.VertexOptions{}
}

func (*buildTestVertex) Inputs() []solver.Edge {
	return nil
}

func (*buildTestVertex) Name() string {
	return "build test"
}

var _ solver.Vertex = (*buildTestVertex)(nil)
