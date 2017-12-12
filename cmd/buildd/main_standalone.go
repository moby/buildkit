// +build standalone

// TODO(AkihiroSuda): s/standalone/oci/g

package main

import (
	"os/exec"

	"github.com/moby/buildkit/worker"
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
		})
	// TODO: allow multiple oci runtimes and snapshotters
}

func ociWorkerInitializer(c *cli.Context, common workerInitializerOpt) ([]*worker.Worker, error) {
	boolOrAuto, err := parseBoolOrAuto(c.GlobalString("oci-worker"))
	if err != nil {
		return nil, err
	}
	if (boolOrAuto == nil && !validOCIBinary()) || (boolOrAuto != nil && !*boolOrAuto) {
		return nil, nil
	}
	opt, err := runc.NewWorkerOpt(common.root)
	if err != nil {
		return nil, err
	}
	opt.SessionManager = common.sessionManager
	w, err := worker.NewWorker(opt)
	if err != nil {
		return nil, err
	}
	return []*worker.Worker{w}, nil
}

func validOCIBinary() bool {
	_, err := exec.LookPath("runc")
	if err != nil {
		logrus.Warnf("skipping oci worker, as runc does not exist")
		return false
	}
	return true
}
