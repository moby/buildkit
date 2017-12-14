package pbload

import (
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// Load loads PB.
// fn is executed sequentially.
func Load(def *pb.Definition, fn func(digest.Digest, *pb.Op, func(digest.Digest) (interface{}, error)) (interface{}, error)) (interface{}, pb.OutputIndex, error) {
	if len(def.Def) == 0 {
		return nil, 0, errors.New("invalid empty definition")
	}

	allOps := make(map[digest.Digest]*pb.Op)

	var dgst digest.Digest

	for _, dt := range def.Def {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, 0, errors.Wrap(err, "failed to parse llb proto op")
		}
		dgst = digest.FromBytes(dt)
		allOps[dgst] = &op
	}

	lastOp := allOps[dgst]
	delete(allOps, dgst)
	dgst = lastOp.Inputs[0].Digest

	cache := make(map[digest.Digest]interface{})

	var rec func(dgst digest.Digest) (interface{}, error)
	rec = func(dgst digest.Digest) (interface{}, error) {
		if v, ok := cache[dgst]; ok {
			return v, nil
		}
		v, err := fn(dgst, allOps[dgst], rec)
		if err != nil {
			return nil, err
		}
		cache[dgst] = v
		return v, nil
	}

	v, err := rec(dgst)
	return v, lastOp.Inputs[0].Index, err
}
