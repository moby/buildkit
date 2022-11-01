package file

import (
	"fmt"
	"path/filepath"

	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/internal/winapi"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

func readUser(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount) (*copy.User, error) {
	if chopt == nil {
		return nil, nil
	}
	var us copy.User
	if chopt.User != nil {
		switch u := chopt.User.User.(type) {
		case *pb.UserOpt_ByName:
			if u.ByName.Name == "ContainerAdministrator" {
				us.SID = idtools.ContainerAdministratorSidString
				return &us, nil
			}
			if u.ByName.Name == "ContainerUser" {
				us.SID = idtools.ContainerUserSidString
				return &us, nil
			}

			if mu == nil {
				return nil, errors.Errorf("invalid missing user mount")
			}
			mmu, ok := mu.(*Mount)
			if !ok {
				return nil, errors.Errorf("invalid mount type %T", mu)
			}
			lm := snapshot.LocalMounter(mmu.m)
			dir, err := lm.Mount()
			if err != nil {
				return nil, err
			}
			defer lm.Unmount()

			passwdPath := filepath.Join(dir, "Windows/system32/config/SAM")
			users, err := winapi.GetUserInfoFromOfflineSAMHive(passwdPath)
			if err != nil {
				return nil, fmt.Errorf("parsing %s: %w", passwdPath, err)
			}

			for _, usr := range users {
				if usr.Username == u.ByName.Name {
					us.SID = usr.SIDString
					break
				}
			}
		case *pb.UserOpt_ByID:
			return nil, fmt.Errorf("UserOpt_ByID not supported on Windows")
		}
	}

	return &us, nil
}
