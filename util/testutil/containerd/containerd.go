package containerd

import (
	"testing"
	"time"

	containerdpkg "github.com/containerd/containerd/v2/client"
)

func GetVersion(t *testing.T, cdAddress string) string {
	t.Helper()

	cdClient, err := containerdpkg.New(cdAddress, containerdpkg.WithTimeout(60*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer cdClient.Close()
	ctx := t.Context()
	cdVersion, err := cdClient.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cdVersion.Version
}
