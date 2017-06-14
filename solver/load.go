package solver

import (
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/client"
	"github.com/tonistiigi/buildkit_poc/solver/pb"
)

func Load(ops [][]byte) (*opVertex, error) {
	if len(ops) == 0 {
		return nil, errors.New("invalid empty definition")
	}

	m := make(map[digest.Digest]*pb.Op)

	var lastOp *pb.Op
	var dgst digest.Digest

	for i, dt := range ops {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, errors.Wrap(err, "failed to parse op")
		}
		lastOp = &op
		dgst = digest.FromBytes(dt)
		if i != len(ops)-1 {
			m[dgst] = &op
		}
		// logrus.Debugf("op %d %s %#v", i, dgst, op)
	}

	cache := make(map[digest.Digest]*opVertex)

	// TODO: validate the connections
	vtx, err := loadReqursive(dgst, lastOp, m, cache)
	if err != nil {
		return nil, err
	}

	return vtx, err
}

func loadReqursive(dgst digest.Digest, op *pb.Op, inputs map[digest.Digest]*pb.Op, cache map[digest.Digest]*opVertex) (*opVertex, error) {
	if v, ok := cache[dgst]; ok {
		return v, nil
	}
	vtx := &opVertex{op: op, dgst: dgst}
	inputDigests := make([]digest.Digest, 0, len(op.Inputs))
	for _, in := range op.Inputs {
		dgst := digest.Digest(in.Digest)
		inputDigests = append(inputDigests, dgst)
		op, ok := inputs[dgst]
		if !ok {
			return nil, errors.Errorf("failed to find %s", in)
		}
		sub, err := loadReqursive(dgst, op, inputs, cache)
		if err != nil {
			return nil, err
		}
		vtx.inputs = append(vtx.inputs, sub)
	}
	vtx.vtx = client.Vertex{
		Inputs: inputDigests,
		Name:   vtx.name(),
		ID:     dgst,
	}
	cache[dgst] = vtx
	return vtx, nil
}
