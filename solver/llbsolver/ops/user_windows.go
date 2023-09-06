package ops

import (
	"context"

	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/solver/llbsolver/file"
	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/windows"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

func getReadUserFn(exec executor.Executor) func(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount) (*copy.User, error) {
	return func(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount) (*copy.User, error) {
		return readUser(chopt, mu, mg, exec)
	}
}

func readUser(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount, exec executor.Executor) (*copy.User, error) {
	if chopt == nil {
		return nil, nil
	}

	if chopt.User != nil {
		switch u := chopt.User.User.(type) {
		case *pb.UserOpt_ByName:
			if mu == nil {
				return nil, errors.Errorf("invalid missing user mount")
			}
			mmu, ok := mu.(*file.Mount)
			if !ok {
				return nil, errors.Errorf("invalid mount type %T", mu)
			}
			mountable := mmu.Mountable()
			if mountable == nil {
				return nil, errors.Errorf("invalid mountable")
			}

			rootMounts, release, err := mountable.Mount()
			if err != nil {
				return nil, err
			}
			defer release()
			ident, err := windows.ResolveUsernameToSID(context.Background(), exec, rootMounts, u.ByName.Name)
			if err != nil {
				return nil, err
			}
			return &copy.User{SID: ident.SID}, nil
		default:
			return &copy.User{SID: idtools.ContainerAdministratorSidString}, nil
		}
	}
	return &copy.User{SID: idtools.ContainerAdministratorSidString}, nil
}
