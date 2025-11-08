package opsutils

import (
	"errors"

	"github.com/moby/buildkit/solver/pb"
)

func Validate(op *pb.Op) error {
	if op == nil {
		return errors.New("invalid nil op")
	}

	switch op := op.Op.(type) {
	case *pb.Op_Source:
		if op.Source == nil {
			return errors.New("invalid nil source op")
		}
	case *pb.Op_Exec:
		if op.Exec == nil {
			return errors.New("invalid nil exec op")
		}
		if op.Exec.Meta == nil {
			return errors.New("invalid exec op with no meta")
		}
		if len(op.Exec.Meta.Args) == 0 {
			return errors.New("invalid exec op with no args")
		}
		if len(op.Exec.Mounts) == 0 {
			return errors.New("invalid exec op with no mounts")
		}

		isRoot := false
		for _, m := range op.Exec.Mounts {
			if m.Dest == pb.RootMount {
				isRoot = true
				break
			}
		}
		if !isRoot {
			return errors.New("invalid exec op with no rootfs")
		}
	case *pb.Op_File:
		if op.File == nil {
			return errors.New("invalid nil file op")
		}
		if len(op.File.Actions) == 0 {
			return errors.New("invalid file op with no actions")
		}
	case *pb.Op_Build:
		if op.Build == nil {
			return errors.New("invalid nil build op")
		}
	case *pb.Op_Merge:
		if op.Merge == nil {
			return errors.New("invalid nil merge op")
		}
	case *pb.Op_Diff:
		if op.Diff == nil {
			return errors.New("invalid nil diff op")
		}
	}
	return nil
}
