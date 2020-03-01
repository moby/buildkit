package integration

import (
	"fmt"
	"log"
	"os"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func InitOCIWorker() {
	Register(&oci{})

	// the rootless uid is defined in Dockerfile
	if s := os.Getenv("BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR"); s != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(s, "%d:%d", &uid, &gid); err != nil {
			logrus.Fatalf("unexpected BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR: %q", s)
		}
		if rootlessSupported(uid) {
			Register(&oci{uid: uid, gid: gid})
		}
	}

}

type oci struct {
	uid int
	gid int
}

func (s *oci) Name() string {
	if s.uid != 0 {
		return "oci-rootless"
	}
	return "oci"
}

func (s *oci) New(cfg *BackendConfig) (Backend, func() error, error) {
	if err := lookupBinary("buildkitd"); err != nil {
		return nil, nil, err
	}
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}
	// Include use of --oci-worker-labels to trigger https://github.com/moby/buildkit/pull/603
	buildkitdArgs := []string{"buildkitd", "--oci-worker=true", "--containerd-worker=false", "--oci-worker-gc=false", "--oci-worker-labels=org.mobyproject.buildkit.worker.sandbox=true"}

	if s.uid != 0 {
		if s.gid == 0 {
			return nil, nil, errors.Errorf("unsupported id pair: uid=%d, gid=%d", s.uid, s.gid)
		}
		// TODO: make sure the user exists and subuid/subgid are configured.
		buildkitdArgs = append([]string{"sudo", "-u", fmt.Sprintf("#%d", s.uid), "-i", "--", "rootlesskit"}, buildkitdArgs...)
	}

	buildkitdSock, stop, err := runBuildkitd(cfg, buildkitdArgs, cfg.Logs, s.uid, s.gid)
	if err != nil {
		printLogs(cfg.Logs, log.Println)
		return nil, nil, err
	}

	return backend{
		address:  buildkitdSock,
		rootless: s.uid != 0,
	}, stop, nil
}
