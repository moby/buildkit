package ops

import (
	"os"
	"syscall"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/llbsolver/file"
	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/runc/libcontainer/user"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

func getReadUserFn(exec executor.Executor) func(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount) (*copy.User, error) {
	return readUser
}

func readUser(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount) (*copy.User, error) {
	if chopt == nil {
		return nil, nil
	}
	var us copy.User
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

			lm := snapshot.LocalMounter(mountable)
			dir, err := lm.Mount()
			if err != nil {
				return nil, err
			}
			defer lm.Unmount()

			passwdPath, err := user.GetPasswdPath()
			if err != nil {
				return nil, err
			}

			passwdPath, err = fs.RootPath(dir, passwdPath)
			if err != nil {
				return nil, err
			}

			ufile, err := os.Open(passwdPath)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				// Couldn't open the file. Considering this case as not finding the user in the file.
				break
			}
			if err != nil {
				return nil, err
			}
			defer ufile.Close()

			users, err := user.ParsePasswdFilter(ufile, func(uu user.User) bool {
				return uu.Name == u.ByName.Name
			})
			if err != nil {
				return nil, err
			}

			if len(users) > 0 {
				us.UID = users[0].Uid
				us.GID = users[0].Gid
			}
		case *pb.UserOpt_ByID:
			us.UID = int(u.ByID)
			us.GID = int(u.ByID)
		}
	}

	if chopt.Group != nil {
		switch u := chopt.Group.User.(type) {
		case *pb.UserOpt_ByName:
			if mg == nil {
				return nil, errors.Errorf("invalid missing group mount")
			}
			mmg, ok := mg.(*file.Mount)
			if !ok {
				return nil, errors.Errorf("invalid mount type %T", mg)
			}
			mountable := mmg.Mountable()
			if mountable == nil {
				return nil, errors.Errorf("invalid mountable")
			}

			lm := snapshot.LocalMounter(mountable)
			dir, err := lm.Mount()
			if err != nil {
				return nil, err
			}
			defer lm.Unmount()

			groupPath, err := user.GetGroupPath()
			if err != nil {
				return nil, err
			}

			groupPath, err = fs.RootPath(dir, groupPath)
			if err != nil {
				return nil, err
			}

			gfile, err := os.Open(groupPath)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				// Couldn't open the file. Considering this case as not finding the group in the file.
				break
			}
			if err != nil {
				return nil, err
			}
			defer gfile.Close()

			groups, err := user.ParseGroupFilter(gfile, func(g user.Group) bool {
				return g.Name == u.ByName.Name
			})
			if err != nil {
				return nil, err
			}

			if len(groups) > 0 {
				us.GID = groups[0].Gid
			}
		case *pb.UserOpt_ByID:
			us.GID = int(u.ByID)
		}
	}

	return &us, nil
}
