package containerd

import (
	"context"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	containerdpkg "github.com/containerd/containerd"
)

func CheckVersion(t *testing.T, cdAddress, constraint string) {
	t.Helper()
	constraintSemVer, err := semver.NewConstraint(constraint)
	if err != nil {
		t.Fatal(err)
	}

	cdClient, err := containerdpkg.New(cdAddress, containerdpkg.WithTimeout(60*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer cdClient.Close()
	ctx := context.TODO()
	cdVersion, err := cdClient.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cdVersionSemVer, err := semver.NewVersion(cdVersion.Version)
	if err != nil {
		t.Fatal(err)
	}

	if !constraintSemVer.Check(cdVersionSemVer) {
		t.Skipf("containerd version %q does not satisfy the constraint %q", cdVersion.Version, constraint)
	}
}
