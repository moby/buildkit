package file

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
	"golang.org/x/sys/unix"
)

func mkdir(ctx context.Context, d string, action pb.FileActionMkDir) error {
	p, err := fs.RootPath(d, filepath.Join(filepath.Join("/", action.Path)))
	if err != nil {
		return err
	}

	if action.MakeParents {
		if err := os.MkdirAll(p, os.FileMode(action.Mode)&0777); err != nil {
			return err
		}
	} else {
		if err := os.Mkdir(p, os.FileMode(action.Mode)&0777); err != nil {
			return err
		}
	}

	if action.Timestamp != -1 {
		st := unix.Timespec{Sec: action.Timestamp / 1e9, Nsec: action.Timestamp % 1e9}
		timespec := []unix.Timespec{st, st}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, timespec, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return errors.Wrapf(err, "failed to utime %s", p)
		}
	}

	return nil
}

func mkfile(ctx context.Context, d string, action pb.FileActionMkFile) error {
	p, err := fs.RootPath(d, filepath.Join(filepath.Join("/", action.Path)))
	if err != nil {
		return err
	}

	if err := ioutil.WriteFile(p, action.Data, os.FileMode(action.Mode)&0777); err != nil {
		return err
	}

	if action.Timestamp != -1 {
		st := unix.Timespec{Sec: action.Timestamp / 1e9, Nsec: action.Timestamp % 1e9}
		timespec := []unix.Timespec{st, st}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, timespec, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return errors.Wrapf(err, "failed to utime %s", p)
		}
	}
	return nil
}

func rm(ctx context.Context, d string, action pb.FileActionRm) error {
	p, err := fs.RootPath(d, filepath.Join(filepath.Join("/", action.Path)))
	if err != nil {
		return err
	}

	if err := os.RemoveAll(p); err != nil {
		if os.IsNotExist(errors.Cause(err)) && action.AllowNotFound {
			return nil
		}
		return err
	}

	return nil
}

func docopy(ctx context.Context, src, dest string, action pb.FileActionCopy) error {
	// // src is the source path
	// Src string `protobuf:"bytes,1,opt,name=src,proto3" json:"src,omitempty"`
	// // dest path
	// Dest string `protobuf:"bytes,2,opt,name=dest,proto3" json:"dest,omitempty"`
	// // optional owner override
	// Owner *ChownOpt `protobuf:"bytes,4,opt,name=owner" json:"owner,omitempty"`
	// // optional permission bits override
	// Mode int32 `protobuf:"varint,5,opt,name=mode,proto3" json:"mode,omitempty"`
	// // followSymlink resolves symlinks in src
	// FollowSymlink bool `protobuf:"varint,6,opt,name=followSymlink,proto3" json:"followSymlink,omitempty"`
	// // dirCopyContents only copies contents if src is a directory
	// DirCopyContents bool `protobuf:"varint,7,opt,name=dirCopyContents,proto3" json:"dirCopyContents,omitempty"`
	// // attemptUnpackDockerCompatibility detects if src is an archive to unpack it instead
	// AttemptUnpackDockerCompatibility bool `protobuf:"varint,8,opt,name=attemptUnpackDockerCompatibility,proto3" json:"attemptUnpackDockerCompatibility,omitempty"`
	// // createDestPath creates dest path directories if needed
	// CreateDestPath bool `protobuf:"varint,9,opt,name=createDestPath,proto3" json:"createDestPath,omitempty"`
	// // allowWildcard allows filepath.Match wildcards in src path
	// AllowWildcard bool `protobuf:"varint,10,opt,name=allowWildcard,proto3" json:"allowWildcard,omitempty"`
	// // allowEmptyWildcard doesn't fail the whole copy if wildcard doesn't resolve to files
	// AllowEmptyWildcard bool `protobuf:"varint,11,opt,name=allowEmptyWildcard,proto3" json:"allowEmptyWildcard,omitempty"`
	// // optional created time override
	// Timestamp int64 `protobuf:"varint,12,opt,name=timestamp,proto3" json:"timestamp,omitempty"`

	srcp, err := fs.RootPath(src, filepath.Join(filepath.Join("/", action.Src)))
	if err != nil {
		return err
	}

	destp, err := fs.RootPath(dest, filepath.Join(filepath.Join("/", action.Dest)))
	if err != nil {
		return err
	}

	var opt []copy.Opt

	if action.AllowWildcard {
		opt = append(opt, copy.AllowWildcards)
	}

	if err := copy.Copy(ctx, srcp, destp, opt...); err != nil {
		return err
	}

	return nil
}

type Backend struct {
}

func (fb *Backend) Mkdir(ctx context.Context, m fileoptypes.Mount, action pb.FileActionMkDir) error {
	mnt, ok := m.(*Mount)
	if !ok {
		return errors.Errorf("invalid mount type %T", m)
	}

	lm := snapshot.LocalMounter(mnt.m)
	dir, err := lm.Mount()
	if err != nil {
		return err
	}
	defer lm.Unmount()

	return mkdir(ctx, dir, action)
}

func (fb *Backend) Mkfile(ctx context.Context, m fileoptypes.Mount, action pb.FileActionMkFile) error {
	mnt, ok := m.(*Mount)
	if !ok {
		return errors.Errorf("invalid mount type %T", m)
	}

	lm := snapshot.LocalMounter(mnt.m)
	dir, err := lm.Mount()
	if err != nil {
		return err
	}
	defer lm.Unmount()

	return mkfile(ctx, dir, action)
}
func (fb *Backend) Rm(ctx context.Context, m fileoptypes.Mount, action pb.FileActionRm) error {
	mnt, ok := m.(*Mount)
	if !ok {
		return errors.Errorf("invalid mount type %T", m)
	}

	lm := snapshot.LocalMounter(mnt.m)
	dir, err := lm.Mount()
	if err != nil {
		return err
	}
	defer lm.Unmount()

	return rm(ctx, dir, action)
}
func (fb *Backend) Copy(ctx context.Context, m1 fileoptypes.Mount, m2 fileoptypes.Mount, action pb.FileActionCopy) error {
	mnt1, ok := m1.(*Mount)
	if !ok {
		return errors.Errorf("invalid mount type %T", m1)
	}
	mnt2, ok := m2.(*Mount)
	if !ok {
		return errors.Errorf("invalid mount type %T", m2)
	}

	lm := snapshot.LocalMounter(mnt1.m)
	src, err := lm.Mount()
	if err != nil {
		return err
	}
	defer lm.Unmount()

	lm2 := snapshot.LocalMounter(mnt2.m)
	dest, err := lm2.Mount()
	if err != nil {
		return err
	}
	defer lm2.Unmount()

	return docopy(ctx, src, dest, action)
}
