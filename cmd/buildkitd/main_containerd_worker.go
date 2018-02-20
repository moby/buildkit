// +build linux,!no_containerd_worker

package main

import (
	"os"
	"strings"

	ctd "github.com/containerd/containerd"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/containerd"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func init() {
	registerWorkerInitializer(
		workerInitializer{
			fn: containerdWorkerInitializer,
			// 1 is less preferred than 0 (runcCtor)
			priority: 1,
		},
		cli.StringFlag{
			Name:  "containerd-worker",
			Usage: "enable containerd workers (true/false/auto)",
			Value: "auto",
		},
		cli.StringFlag{
			Name:  "containerd-worker-addr",
			Usage: "containerd socket",
			Value: "/run/containerd/containerd.sock",
		},
		cli.StringSliceFlag{
			Name:  "containerd-worker-labels",
			Usage: "user-specific annotation labels (com.example.foo=bar)",
		},
	)
	// TODO(AkihiroSuda): allow using multiple snapshotters. should be useful for some applications that does not work with the default overlay snapshotter. e.g. mysql (docker/for-linux#72)",
}

func containerdWorkerInitializer(c *cli.Context, common workerInitializerOpt) ([]worker.Worker, error) {
	socket := c.GlobalString("containerd-worker-addr")
	bridge := c.GlobalString("bridge")
	boolOrAuto, err := parseBoolOrAuto(c.GlobalString("containerd-worker"))
	if err != nil {
		return nil, err
	}
	if (boolOrAuto == nil && !validContainerdSocket(socket)) || (boolOrAuto != nil && !*boolOrAuto) {
		return nil, nil
	}
	labels, err := attrMap(c.GlobalStringSlice("containerd-worker-labels"))
	if err != nil {
		return nil, err
	}
	opt, err := containerd.NewWorkerOpt(common.root, socket, ctd.DefaultSnapshotter, labels, bridge)
	if err != nil {
		return nil, err
	}
	opt.SessionManager = common.sessionManager
	w, err := base.NewWorker(opt)
	if err != nil {
		return nil, err
	}
	return []worker.Worker{w}, nil
}

func validContainerdSocket(socket string) bool {
	if strings.HasPrefix(socket, "tcp://") {
		// FIXME(AkihiroSuda): prohibit tcp?
		return true
	}
	socketPath := strings.TrimPrefix(socket, "unix://")
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		// FIXME(AkihiroSuda): add more conditions
		logrus.Warnf("skipping containerd worker, as %q does not exist", socketPath)
		return false
	}
	// TODO: actually dial and call introspection API
	return true
}
