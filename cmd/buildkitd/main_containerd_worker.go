// +build linux,!no_containerd_worker

package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	ctd "github.com/containerd/containerd"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/containerd"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

const defaultContainerdAddress = "/run/containerd/containerd.sock"

func init() {
	defaultConf, _ := defaultConf()

	enabledValue := func(b *bool) string {
		if b == nil {
			return "auto"
		}
		return strconv.FormatBool(*b)
	}

	if defaultConf.Workers.Containerd.Address == "" {
		defaultConf.Workers.Containerd.Address = defaultContainerdAddress
	}

	registerWorkerInitializer(
		workerInitializer{
			fn: containerdWorkerInitializer,
			// 1 is less preferred than 0 (runcCtor)
			priority: 1,
		},
		cli.StringFlag{
			Name:  "containerd-worker",
			Usage: "enable containerd workers (true/false/auto)",
			Value: enabledValue(defaultConf.Workers.Containerd.Enabled),
		},
		cli.StringFlag{
			Name:  "containerd-worker-addr",
			Usage: "containerd socket",
			Value: defaultConf.Workers.Containerd.Address,
		},
		cli.StringSliceFlag{
			Name:  "containerd-worker-labels",
			Usage: "user-specific annotation labels (com.example.foo=bar)",
		},
		// TODO: containerd-worker-platform should be replaced by ability
		// to set these from containerd configuration
		cli.StringSliceFlag{
			Name:   "containerd-worker-platform",
			Usage:  "override supported platforms for worker",
			Hidden: true,
		},
	)
	// TODO(AkihiroSuda): allow using multiple snapshotters. should be useful for some applications that does not work with the default overlay snapshotter. e.g. mysql (docker/for-linux#72)",
}

func applyContainerdFlags(c *cli.Context, cfg *config.Config) error {
	if cfg.Workers.Containerd.Address == "" {
		cfg.Workers.Containerd.Address = defaultContainerdAddress
	}

	if c.GlobalIsSet("containerd-worker") {
		boolOrAuto, err := parseBoolOrAuto(c.GlobalString("containerd-worker"))
		if err != nil {
			return err
		}
		cfg.Workers.Containerd.Enabled = boolOrAuto
	}

	// GlobalBool works for BoolT as well
	rootless := c.GlobalBool("rootless")
	if rootless {
		logrus.Warn("rootless mode is not supported for containerd workers. disabling containerd worker.")
		b := false
		cfg.Workers.Containerd.Enabled = &b
		return nil
	}

	labels, err := attrMap(c.GlobalStringSlice("containerd-worker-labels"))
	if err != nil {
		return err
	}
	for k, v := range labels {
		cfg.Workers.Containerd.Labels[k] = v
	}
	if c.GlobalIsSet("containerd-worker-addr") {
		cfg.Workers.Containerd.Address = c.GlobalString("containerd-worker-addr")
	}

	if platforms := c.GlobalStringSlice("containerd-worker-platform"); len(platforms) != 0 {
		cfg.Workers.Containerd.Platforms = platforms
	}

	return nil
}

func containerdWorkerInitializer(c *cli.Context, common workerInitializerOpt) ([]worker.Worker, error) {
	if err := applyContainerdFlags(c, common.config); err != nil {
		return nil, err
	}

	cfg := common.config.Workers.Containerd

	if (cfg.Enabled == nil && !validContainerdSocket(cfg.Address)) || (cfg.Enabled != nil && !*cfg.Enabled) {
		return nil, nil
	}

	opt, err := containerd.NewWorkerOpt(common.config.Root, cfg.Address, ctd.DefaultSnapshotter, cfg.Labels, ctd.WithTimeout(60*time.Second))
	if err != nil {
		return nil, err
	}
	opt.SessionManager = common.sessionManager

	if platformsStr := cfg.Platforms; len(platformsStr) != 0 {
		platforms, err := parsePlatforms(platformsStr)
		if err != nil {
			return nil, errors.Wrap(err, "invalid platforms")
		}
		opt.Platforms = platforms
	}
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
