package solver

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/docker/docker/pkg/symlink"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type buildOp struct {
	op *pb.BuildOp
	s  *Solver
	v  Vertex
}

func newBuildOp(v Vertex, op *pb.Op_Build, s *Solver) (Op, error) {
	return &buildOp{
		op: op.Build,
		s:  s,
		v:  v,
	}, nil
}

func (b *buildOp) CacheKey(ctx context.Context, inputs []string) (string, int, error) {
	dt, err := json.Marshal(struct {
		Inputs []string
		Exec   *pb.BuildOp
	}{
		Inputs: inputs,
		Exec:   b.op,
	})
	if err != nil {
		return "", 0, err
	}
	return digest.FromBytes(dt).String(), 1, nil // TODO: other builders should support many outputs
}

func (b *buildOp) Run(ctx context.Context, inputs []Reference) (outputs []Reference, retErr error) {
	if b.op.Builder != pb.LLBBuilder {
		return nil, errors.Errorf("only llb builder is currently allowed")
	}

	builderInputs := b.op.Inputs
	llbDef, ok := builderInputs[pb.LLBDefinitionInput]
	if !ok {
		return nil, errors.Errorf("no llb definition input %s found", pb.LLBDefinitionInput)
	}

	i := int(llbDef.Input)
	if i >= len(inputs) {
		return nil, errors.Errorf("invalid index %v", i) // TODO: this should be validated before
	}
	inp := inputs[i]

	ref, ok := toImmutableRef(inp)
	if !ok {
		return nil, errors.Errorf("invalid reference for build %T", inp)
	}

	mount, err := ref.Mount(ctx, false)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)

	root, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer func() {
		if retErr != nil && lm != nil {
			lm.Unmount()
		}
	}()

	fn := pb.LLBDefaultDefinitionFile
	if override, ok := b.op.Attrs[pb.AttrLLBDefinitionFilename]; ok {
		fn = override
	}

	newfn, err := symlink.FollowSymlinkInScope(filepath.Join(root, fn), root)
	if err != nil {
		return nil, errors.Wrapf(err, "working dir %s points to invalid target", fn)
	}

	f, err := os.Open(newfn)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open %s", newfn)
	}

	def, err := llb.ReadFrom(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	f.Close()
	lm.Unmount()
	lm = nil

	v, err := LoadLLB(def)
	if err != nil {
		return nil, err
	}

	if len(v.Inputs()) == 0 {
		return nil, errors.New("required vertex needs to have inputs")
	}

	index := v.Inputs()[0].Index
	v = v.Inputs()[0].Vertex

	vv := toInternalVertex(v)

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("parentVertex", b.v.Digest()))
	defer pw.Close()

	refs, err := b.s.getRefs(ctx, vv)

	// filer out only the required ref
	var out Reference
	for i, r := range refs {
		if i == index {
			out = r
		} else {
			go r.Release(context.TODO())
		}
	}

	return []Reference{out}, err
}
