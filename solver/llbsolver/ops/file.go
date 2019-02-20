package ops

import (
	"context"
	"fmt"
	"sync"

	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func NewFileOpSolver(b fileoptypes.Backend, r fileoptypes.RefManager) *FileOpSolver {
	return &FileOpSolver{
		b:    b,
		r:    r,
		outs: map[int]int{},
		ins:  map[int]input{},
	}
}

type FileOpSolver struct {
	b fileoptypes.Backend
	r fileoptypes.RefManager

	mu   sync.Mutex
	outs map[int]int
	ins  map[int]input
	g    flightcontrol.Group
}

type input struct {
	requiresCommit bool
	mount          fileoptypes.Mount
	ref            fileoptypes.Ref
}

func (s *FileOpSolver) Solve(ctx context.Context, inputs []fileoptypes.Ref, actions []*pb.FileAction) ([]fileoptypes.Ref, error) {
	for i, a := range actions {
		if int(a.Input) < -1 || int(a.Input) >= len(inputs)+len(actions) {
			return nil, errors.Errorf("invalid input index %d, %d provided", a.Input, len(inputs))
		}
		if int(a.SecondaryInput) < -1 || int(a.SecondaryInput) >= len(inputs)+len(actions) {
			return nil, errors.Errorf("invalid secondary input index %d, %d provided", a.Input, len(inputs))
		}

		inp, ok := s.ins[int(a.Input)]
		if ok {
			inp.requiresCommit = true
		}
		s.ins[int(a.Input)] = inp

		inp, ok = s.ins[int(a.SecondaryInput)]
		if ok {
			inp.requiresCommit = true
		}
		s.ins[int(a.SecondaryInput)] = inp

		if a.Output != -1 {
			if _, ok := s.outs[int(a.Output)]; ok {
				return nil, errors.Errorf("duplicate output %d", a.Output)
			}
			idx := len(inputs) + i
			s.outs[int(a.Output)] = idx
			s.ins[idx] = input{requiresCommit: true}
		}
	}

	if len(s.outs) == 0 {
		return nil, errors.Errorf("no outputs specified")
	}

	for i := 0; i < len(s.outs); i++ {
		if _, ok := s.outs[i]; !ok {
			return nil, errors.Errorf("missing output index %d", i)
		}
	}

	defer func() {
		for _, in := range s.ins {
			if in.ref == nil && in.mount != nil {
				in.mount.Release(context.TODO())
			}
		}
	}()

	outs := make([]fileoptypes.Ref, len(s.outs))

	eg, ctx := errgroup.WithContext(ctx)
	for i, idx := range s.outs {
		func(i, idx int) {
			eg.Go(func() error {
				if err := s.validate(idx, inputs, actions, nil); err != nil {
					return err
				}
				inp, err := s.getInput(ctx, idx, inputs, actions)
				if err != nil {
					return err
				}
				outs[i] = inp.ref
				return nil
			})
		}(i, idx)
	}

	if err := eg.Wait(); err != nil {
		for _, r := range outs {
			if r != nil {
				r.Release(context.TODO())
			}
		}
		return nil, err
	}

	return outs, nil
}

func (s *FileOpSolver) validate(idx int, inputs []fileoptypes.Ref, actions []*pb.FileAction, loaded []int) error {
	for _, check := range loaded {
		if idx == check {
			return errors.Errorf("loop from index %d", idx)
		}
	}
	if idx < len(inputs) {
		return nil
	}
	loaded = append(loaded, idx)
	action := actions[idx-len(inputs)]
	for _, inp := range []int{int(action.Input), int(action.SecondaryInput)} {
		if err := s.validate(inp, inputs, actions, loaded); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileOpSolver) getInput(ctx context.Context, idx int, inputs []fileoptypes.Ref, actions []*pb.FileAction) (input, error) {
	inp, err := s.g.Do(ctx, fmt.Sprintf("inp-%d", idx), func(ctx context.Context) (interface{}, error) {
		s.mu.Lock()
		inp := s.ins[idx]
		s.mu.Unlock()
		if inp.mount != nil || inp.ref != nil {
			return inp, nil
		}

		if idx < len(inputs) {
			inp.ref = inputs[idx]
			s.mu.Lock()
			s.ins[idx] = inp
			s.mu.Unlock()
			return inp, nil
		}

		var inpMount, inpMountSecondary fileoptypes.Mount
		action := actions[idx-len(inputs)]

		loadInput := func(ctx context.Context) func() error {
			return func() error {
				inp, err := s.getInput(ctx, int(action.Input), inputs, actions)
				if err != nil {
					return err
				}
				if inp.ref != nil {
					m, err := s.r.Prepare(ctx, inp.ref, false)
					if err != nil {
						return err
					}
					inpMount = m
					return nil
				}
				inpMount = inp.mount
				return nil
			}
		}

		loadSecondaryInput := func(ctx context.Context) func() error {
			return func() error {
				inp, err := s.getInput(ctx, int(action.SecondaryInput), inputs, actions)
				if err != nil {
					return err
				}
				if inp.ref != nil {
					m, err := s.r.Prepare(ctx, inp.ref, true)
					if err != nil {
						return err
					}
					inpMountSecondary = m
					return nil
				}
				inpMountSecondary = inp.mount
				return nil
			}
		}

		if action.Input != -1 && action.SecondaryInput != -1 {
			eg, ctx := errgroup.WithContext(ctx)
			eg.Go(loadInput(ctx))
			eg.Go(loadSecondaryInput(ctx))
			if err := eg.Wait(); err != nil {
				return nil, err
			}
		} else {
			if action.Input != -1 {
				if err := loadInput(ctx)(); err != nil {
					return nil, err
				}
			}
			if action.SecondaryInput != -1 {
				if err := loadSecondaryInput(ctx)(); err != nil {
					return nil, err
				}
			}
		}

		if inpMount == nil {
			m, err := s.r.Prepare(ctx, nil, false)
			if err != nil {
				return nil, err
			}
			inpMount = m
		}

		switch a := action.Action.(type) {
		case *pb.FileAction_Mkdir:
			if err := s.b.Mkdir(ctx, inpMount, *a.Mkdir); err != nil {
				return nil, err
			}
		case *pb.FileAction_Mkfile:
			if err := s.b.Mkfile(ctx, inpMount, *a.Mkfile); err != nil {
				return nil, err
			}
		case *pb.FileAction_Rm:
			if err := s.b.Rm(ctx, inpMount, *a.Rm); err != nil {
				return nil, err
			}
		case *pb.FileAction_Copy:
			if inpMountSecondary == nil {
				m, err := s.r.Prepare(ctx, nil, true)
				if err != nil {
					return nil, err
				}
				inpMountSecondary = m
			}
			if err := s.b.Copy(ctx, inpMountSecondary, inpMount, *a.Copy); err != nil {
				return nil, err
			}
		default:
			return nil, errors.Errorf("invalid action type %T", action.Action)
		}

		if inp.requiresCommit {
			ref, err := s.r.Commit(ctx, inpMount)
			if err != nil {
				return nil, err
			}
			inp.ref = ref
		} else {
			inp.mount = inpMount
		}
		s.mu.Lock()
		s.ins[idx] = inp
		s.mu.Unlock()
		return inp, nil
	})
	if err != nil {
		return input{}, err
	}
	return inp.(input), err
}
