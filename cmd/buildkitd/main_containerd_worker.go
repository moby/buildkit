// +build linux,!no_containerd_worker windows,!no_containerd_worker

package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	ctd "github.com/containerd/containerd"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/util/network/cniprovider"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/containerd"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"golang.org/x/sync/semaphore"
)

const (
	defaultContainerdAddress   = "/run/containerd/containerd.sock"
	defaultContainerdNamespace = "buildkit"
)

func init() {
	defaultConf, _, _ := defaultConf()

	enabledValue := func(b *bool) string {
		if b == nil {
			return "auto"
		}
		return strconv.FormatBool(*b)
	}

	if defaultConf.Workers.Containerd.Address == "" {
		defaultConf.Workers.Containerd.Address = defaultContainerdAddress
	}

	if defaultConf.Workers.Containerd.Namespace == "" {
		defaultConf.Workers.Containerd.Namespace = defaultContainerdNamespace
	}

	flags := []cli.Flag{
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
		cli.StringFlag{
			Name:   "containerd-worker-namespace",
			Usage:  "override containerd namespace",
			Value:  defaultConf.Workers.Containerd.Namespace,
			Hidden: true,
		},
		cli.StringFlag{
			Name:  "containerd-worker-net",
			Usage: "worker network type (auto, cni or host)",
			Value: defaultConf.Workers.Containerd.NetworkConfig.Mode,
		},
		cli.StringFlag{
			Name:  "containerd-cni-config-path",
			Usage: "path of cni config file",
			Value: defaultConf.Workers.Containerd.NetworkConfig.CNIConfigPath,
		},
		cli.StringFlag{
			Name:  "containerd-cni-binary-dir",
			Usage: "path of cni binary files",
			Value: defaultConf.Workers.Containerd.NetworkConfig.CNIBinaryPath,
		},
		cli.StringFlag{
			Name:  "containerd-worker-snapshotter",
			Usage: "snapshotter name to use",
			Value: ctd.DefaultSnapshotter,
		},
		cli.StringFlag{
			Name:  "containerd-worker-apparmor-profile",
			Usage: "set the name of the apparmor profile applied to containers",
		},
	}

	if defaultConf.Workers.Containerd.GC == nil || *defaultConf.Workers.Containerd.GC {
		flags = append(flags, cli.BoolTFlag{
			Name:  "containerd-worker-gc",
			Usage: "Enable automatic garbage collection on worker",
		})
	} else {
		flags = append(flags, cli.BoolFlag{
			Name:  "containerd-worker-gc",
			Usage: "Enable automatic garbage collection on worker",
		})
	}
	flags = append(flags, cli.Int64Flag{
		Name:  "containerd-worker-gc-keepstorage",
		Usage: "Amount of storage GC keep locally (MB)",
		Value: func() int64 {
			if defaultConf.Workers.Containerd.GCKeepStorage != 0 {
				return defaultConf.Workers.Containerd.GCKeepStorage / 1e6
			}
			return config.DetectDefaultGCCap(defaultConf.Root) / 1e6
		}(),
		Hidden: len(defaultConf.Workers.Containerd.GCPolicy) != 0,
	})

	registerWorkerInitializer(
		workerInitializer{
			fn: containerdWorkerInitializer,
			// 1 is less preferred than 0 (runcCtor)
			priority: 1,
		},
		flags...,
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
	if cfg.Workers.Containerd.Labels == nil {
		cfg.Workers.Containerd.Labels = make(map[string]string)
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

	if c.GlobalIsSet("containerd-worker-namespace") || cfg.Workers.Containerd.Namespace == "" {
		cfg.Workers.Containerd.Namespace = c.GlobalString("containerd-worker-namespace")
	}

	if c.GlobalIsSet("containerd-worker-gc") {
		v := c.GlobalBool("containerd-worker-gc")
		cfg.Workers.Containerd.GC = &v
	}

	if c.GlobalIsSet("containerd-worker-gc-keepstorage") {
		cfg.Workers.Containerd.GCKeepStorage = c.GlobalInt64("containerd-worker-gc-keepstorage") * 1e6
	}

	if c.GlobalIsSet("containerd-worker-net") {
		cfg.Workers.Containerd.NetworkConfig.Mode = c.GlobalString("containerd-worker-net")
	}
	if c.GlobalIsSet("containerd-cni-config-path") {
		cfg.Workers.Containerd.NetworkConfig.CNIConfigPath = c.GlobalString("containerd-cni-config-path")
	}
	if c.GlobalIsSet("containerd-cni-binary-dir") {
		cfg.Workers.Containerd.NetworkConfig.CNIBinaryPath = c.GlobalString("containerd-cni-binary-dir")
	}
	if c.GlobalIsSet("containerd-worker-snapshotter") {
		cfg.Workers.Containerd.Snapshotter = c.GlobalString("containerd-worker-snapshotter")
	}
	if c.GlobalIsSet("containerd-worker-apparmor-profile") {
		cfg.Workers.Containerd.ApparmorProfile = c.GlobalString("containerd-worker-apparmor-profile")
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

	dns := getDNSConfig(common.config.DNS)

	nc := netproviders.Opt{
		Mode: common.config.Workers.Containerd.NetworkConfig.Mode,
		CNI: cniprovider.Opt{
			Root:       common.config.Root,
			ConfigPath: common.config.Workers.Containerd.CNIConfigPath,
			BinaryDir:  common.config.Workers.Containerd.CNIBinaryPath,
		},
	}

	var parallelismSem *semaphore.Weighted
	if cfg.MaxParallelism > 0 {
		parallelismSem = semaphore.NewWeighted(int64(cfg.MaxParallelism))
	}

	snapshotter := ctd.DefaultSnapshotter
	if cfg.Snapshotter != "" {
		snapshotter = cfg.Snapshotter
	}
	opt, err := containerd.NewWorkerOpt(common.config.Root, cfg.Address, snapshotter, cfg.Namespace, cfg.Labels, dns, nc, common.config.Workers.Containerd.ApparmorProfile, parallelismSem, common.traceSocket, ctd.WithTimeout(60*time.Second))
	if err != nil {
		return nil, err
	}
	opt.GCPolicy = getGCPolicy(cfg.GCConfig, common.config.Root)
	opt.RegistryHosts = resolverFunc(common.config)

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
	if _, err := os.Stat(socketPath); errors.Is(err, os.ErrNotExist) {
		// FIXME(AkihiroSuda): add more conditions
		logrus.Warnf("skipping containerd worker, as %q does not exist", socketPath)
		return false
	}
	// TODO: actually dial and call introspection API
	return true
}
