// +build linux,!no_oci_worker

package main

import (
	"os/exec"

	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/runc"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func init() {
	registerWorkerInitializer(
		workerInitializer{
			fn:       ociWorkerInitializer,
			priority: 0,
		},
		cli.StringFlag{
			Name:  "oci-worker",
			Usage: "enable oci workers (true/false/auto)",
			Value: "auto",
		},
		cli.StringSliceFlag{
			Name:  "oci-worker-labels",
			Usage: "user-specific annotation labels (com.example.foo=bar)",
		},
	)
	// TODO: allow multiple oci runtimes and snapshotters
}

func ociWorkerInitializer(c *cli.Context, common workerInitializerOpt) ([]worker.Worker, error) {
	bridge := c.GlobalString("bridge")
	boolOrAuto, err := parseBoolOrAuto(c.GlobalString("oci-worker"))
	if err != nil {
		return nil, err
	}
	if (boolOrAuto == nil && !validOCIBinary()) || (boolOrAuto != nil && !*boolOrAuto) {
		return nil, nil
	}
	labels, err := attrMap(c.GlobalStringSlice("oci-worker-labels"))
	if err != nil {
		return nil, err
	}
	opt, err := runc.NewWorkerOpt(common.root, labels, bridge)
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

func validOCIBinary() bool {
	_, err := exec.LookPath("runc")
	if err != nil {
		logrus.Warnf("skipping oci worker, as runc does not exist")
		return false
	}
	return true
}
