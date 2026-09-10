//go:build !linux

package snapshot

import (
	"context"
	"runtime"

	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/pkg/errors"
)

func (sn *mergeSnapshotter) diffApply(context.Context, Mountable, ...Diff) (_ snapshots.Usage, rerr error) {
	return snapshots.Usage{}, errors.New("diffApply not yet supported on " + runtime.GOOS)
}

func needsUserXAttr(context.Context, Snapshotter, leases.Manager) (bool, error) {
	return false, errors.New("needs userxattr not supported on " + runtime.GOOS)
}
