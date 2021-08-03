//go:build windows
// +build windows

package snapshot

import (
	"context"

	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/snapshots"
	"github.com/pkg/errors"
)

func diffApply(ctx context.Context, lowerMountable, upperMountable, applyMountable Mountable, useHardlink bool, externalHardlinks map[uint64]struct{}) error {
	return errors.New("diff apply not supported on windows")
}

func diskUsage(ctx context.Context, mountable Mountable, externalHardlinks map[uint64]struct{}) (snapshots.Usage, error) {
	return snapshots.Usage{}, errors.New("disk usage not supported on windows")
}

func needsUserXAttr(ctx context.Context, sn Snapshotter, lm leases.Manager) (bool, error) {
	return false, errors.New("needs userxattr not supported on windows")
}
