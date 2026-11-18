//go:build windows

package cniprovider

import (
	"github.com/Microsoft/hcsshim/hcn"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func createNetNS(_ *cniProvider, _ string) (string, error) {
	nsTemplate := hcn.NewNamespace(hcn.NamespaceTypeGuest)
	ns, err := nsTemplate.Create()
	if err != nil {
		return "", errors.Wrapf(err, "HostComputeNamespace.Create failed for %s", nsTemplate.Id)
	}

	return ns.Id, nil
}

func setNetNS(s *specs.Spec, nativeID string) error {
	return oci.WithWindowsNetworkNamespace(nativeID)(nil, nil, nil, s)
}

func unmountNetNS(_ string) error {
	// We don't need to unmount the NS.
	return nil
}

func deleteNetNS(nativeID string) error {
	ns, err := hcn.GetNamespaceByID(nativeID)
	if err != nil {
		return errors.Wrapf(err, "failed to get namespace %s", nativeID)
	}

	return ns.Delete()
}

func cleanOldNamespaces(_ *cniProvider) {
	// not implemented on Windows
}
