package solver

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/docker/docker/pkg/symlink"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/snapshot"
	solver "github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/solver/reference"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

const buildCacheType = "buildkit.build.v0"

type buildOp struct {
	op *pb.BuildOp
	s  *Solver
	v  solver.Vertex
}

func newBuildOp(v solver.Vertex, op *pb.Op_Build, s *Solver) (solver.Op, error) {
	return &buildOp{
		op: op.Build,
		s:  s,
		v:  v,
	}, nil
}

func (b *buildOp) CacheKey(ctx context.Context) (digest.Digest, error) {
	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.BuildOp
	}{
		Type: buildCacheType,
		Exec: b.op,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

func (b *buildOp) Run(ctx context.Context, inputs []solver.Ref) (outputs []solver.Ref, retErr error) {
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

	ref, ok := reference.ToImmutableRef(inp)
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

	newref, err := b.s.subBuild(ctx, b.v.Digest(), solver.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, err
	}

	return []solver.Ref{newref}, err
}

func (b *buildOp) ContentMask(context.Context) (digest.Digest, [][]string, error) {
	return "", nil, nil
}
