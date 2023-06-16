package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"

	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

func InitOCIWorker() {
	Register(&OCI{ID: "oci"})

	// the rootless uid is defined in Dockerfile
	if s := os.Getenv("BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR"); s != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(s, "%d:%d", &uid, &gid); err != nil {
			bklog.L.Fatalf("unexpected BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR: %q", s)
		}
		if rootlessSupported(uid) {
			Register(&OCI{ID: "oci-rootless", UID: uid, GID: gid})
		}
	}

	if s := os.Getenv("BUILDKIT_INTEGRATION_SNAPSHOTTER"); s != "" {
		Register(&OCI{ID: "oci-snapshotter-" + s, Snapshotter: s})
	}
}

type OCI struct {
	ID          string
	UID         int
	GID         int
	Snapshotter string
}

func (s *OCI) Name() string {
	return s.ID
}

func (s *OCI) Rootless() bool {
	return s.UID != 0
}

func (s *OCI) New(ctx context.Context, cfg *BackendConfig) (Backend, func() error, error) {
	if err := lookupBinary("buildkitd"); err != nil {
		return nil, nil, err
	}
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}
	// Include use of --oci-worker-labels to trigger https://github.com/moby/buildkit/pull/603
	buildkitdArgs := []string{"buildkitd", "--oci-worker=true", "--containerd-worker=false", "--oci-worker-gc=false", "--oci-worker-labels=org.mobyproject.buildkit.worker.sandbox=true"}

	if s.Snapshotter != "" {
		buildkitdArgs = append(buildkitdArgs,
			fmt.Sprintf("--oci-worker-snapshotter=%s", s.Snapshotter))
	}

	if s.UID != 0 {
		if s.GID == 0 {
			return nil, nil, errors.Errorf("unsupported id pair: uid=%d, gid=%d", s.UID, s.GID)
		}
		// TODO: make sure the user exists and subuid/subgid are configured.
		buildkitdArgs = append([]string{"sudo", "-u", fmt.Sprintf("#%d", s.UID), "-i", "--", "exec", "rootlesskit"}, buildkitdArgs...)
	}

	var extraEnv []string
	if runtime.GOOS != "windows" && s.Snapshotter != "native" {
		extraEnv = append(extraEnv, "BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true")
	}
	buildkitdSock, stop, err := runBuildkitd(ctx, cfg, buildkitdArgs, cfg.Logs, s.UID, s.GID, extraEnv)
	if err != nil {
		printLogs(cfg.Logs, log.Println)
		return nil, nil, err
	}

	return backend{
		address:     buildkitdSock,
		rootless:    s.UID != 0,
		snapshotter: s.Snapshotter,
	}, stop, nil
}

func (s *OCI) Close() error {
	return nil
}
