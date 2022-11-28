//go:build (linux && !no_containerd_worker) || (windows && !no_containerd_worker)
// +build linux,!no_containerd_worker windows,!no_containerd_worker

package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/pkg/userns"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/util/network/cniprovider"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/containerd"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"golang.org/x/sync/semaphore"
)

const (
	defaultContainerdAddress   = "/run/containerd/containerd.sock"
	defaultContainerdNamespace = "buildkit"
)

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

	if defaultConf.Workers.Containerd.Namespace == "" {
		defaultConf.Workers.Containerd.Namespace = defaultContainerdNamespace
	}

	flags := []cli.Flag{
		&cli.StringFlag{
			Name:  "containerd-worker",
			Usage: "enable containerd workers (true/false/auto)",
			Value: enabledValue(defaultConf.Workers.Containerd.Enabled),
		},
		&cli.StringFlag{
			Name:  "containerd-worker-addr",
			Usage: "containerd socket",
			Value: defaultConf.Workers.Containerd.Address,
		},
		&cli.StringSliceFlag{
			Name:  "containerd-worker-labels",
			Usage: "user-specific annotation labels (com.example.foo=bar)",
		},
		// TODO: containerd-worker-platform should be replaced by ability
		// to set these from containerd configuration
		&cli.StringSliceFlag{
			Name:   "containerd-worker-platform",
			Usage:  "override supported platforms for worker",
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   "containerd-worker-namespace",
			Usage:  "override containerd namespace",
			Value:  defaultConf.Workers.Containerd.Namespace,
			Hidden: true,
		},
		&cli.StringFlag{
			Name:  "containerd-worker-net",
			Usage: "worker network type (auto, cni or host)",
			Value: defaultConf.Workers.Containerd.NetworkConfig.Mode,
		},
		&cli.StringFlag{
			Name:  "containerd-cni-config-path",
			Usage: "path of cni config file",
			Value: defaultConf.Workers.Containerd.NetworkConfig.CNIConfigPath,
		},
		&cli.StringFlag{
			Name:  "containerd-cni-binary-dir",
			Usage: "path of cni binary files",
			Value: defaultConf.Workers.Containerd.NetworkConfig.CNIBinaryPath,
		},
		&cli.IntFlag{
			Name:  "containerd-cni-pool-size",
			Usage: "size of cni network namespace pool",
			Value: defaultConf.Workers.Containerd.NetworkConfig.CNIPoolSize,
		},
		&cli.StringFlag{
			Name:  "containerd-worker-snapshotter",
			Usage: "snapshotter name to use",
			Value: ctd.DefaultSnapshotter,
		},
		&cli.StringFlag{
			Name:  "containerd-worker-apparmor-profile",
			Usage: "set the name of the apparmor profile applied to containers",
		},
		&cli.BoolFlag{
			Name:  "containerd-worker-selinux",
			Usage: "apply SELinux labels",
		},
	}
	n := "containerd-worker-rootless"
	u := "enable rootless mode"
	if userns.RunningInUserNS() {
		flags = append(flags, &cli.BoolFlag{
			Name:  n,
			Usage: u,
		})
	} else {
		flags = append(flags, &cli.BoolFlag{
			Name:  n,
			Usage: u,
		})
	}

	if defaultConf.Workers.Containerd.GC == nil || *defaultConf.Workers.Containerd.GC {
		flags = append(flags, &cli.BoolFlag{
			Name:  "containerd-worker-gc",
			Usage: "Enable automatic garbage collection on worker",
		})
	} else {
		flags = append(flags, &cli.BoolFlag{
			Name:  "containerd-worker-gc",
			Usage: "Enable automatic garbage collection on worker",
		})
	}
	flags = append(flags, &cli.Int64Flag{
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

	if c.IsSet("containerd-worker") {
		boolOrAuto, err := parseBoolOrAuto(c.String("containerd-worker"))
		if err != nil {
			return err
		}
		cfg.Workers.Containerd.Enabled = boolOrAuto
	}

	if c.IsSet("rootless") || c.Bool("rootless") {
		cfg.Workers.Containerd.Rootless = c.Bool("rootless")
	}
	if c.IsSet("containerd-worker-rootless") {
		if !userns.RunningInUserNS() || os.Geteuid() > 0 {
			return errors.New("rootless mode requires to be executed as the mapped root in a user namespace; you may use RootlessKit for setting up the namespace")
		}
		cfg.Workers.Containerd.Rootless = c.Bool("containerd-worker-rootless")
	}

	labels, err := attrMap(c.StringSlice("containerd-worker-labels"))
	if err != nil {
		return err
	}
	if cfg.Workers.Containerd.Labels == nil {
		cfg.Workers.Containerd.Labels = make(map[string]string)
	}
	for k, v := range labels {
		cfg.Workers.Containerd.Labels[k] = v
	}
	if c.IsSet("containerd-worker-addr") {
		cfg.Workers.Containerd.Address = c.String("containerd-worker-addr")
	}

	if platforms := c.StringSlice("containerd-worker-platform"); len(platforms) != 0 {
		cfg.Workers.Containerd.Platforms = platforms
	}

	if c.IsSet("containerd-worker-namespace") || cfg.Workers.Containerd.Namespace == "" {
		cfg.Workers.Containerd.Namespace = c.String("containerd-worker-namespace")
	}

	if c.IsSet("containerd-worker-gc") {
		v := c.Bool("containerd-worker-gc")
		cfg.Workers.Containerd.GC = &v
	}

	if c.IsSet("containerd-worker-gc-keepstorage") {
		cfg.Workers.Containerd.GCKeepStorage = c.Int64("containerd-worker-gc-keepstorage") * 1e6
	}

	if c.IsSet("containerd-worker-net") {
		cfg.Workers.Containerd.NetworkConfig.Mode = c.String("containerd-worker-net")
	}
	if c.IsSet("containerd-cni-config-path") {
		cfg.Workers.Containerd.NetworkConfig.CNIConfigPath = c.String("containerd-cni-config-path")
	}
	if c.IsSet("containerd-cni-pool-size") {
		cfg.Workers.Containerd.NetworkConfig.CNIPoolSize = c.Int("containerd-cni-pool-size")
	}
	if c.IsSet("containerd-cni-binary-dir") {
		cfg.Workers.Containerd.NetworkConfig.CNIBinaryPath = c.String("containerd-cni-binary-dir")
	}
	if c.IsSet("containerd-worker-snapshotter") {
		cfg.Workers.Containerd.Snapshotter = c.String("containerd-worker-snapshotter")
	}
	if c.IsSet("containerd-worker-apparmor-profile") {
		cfg.Workers.Containerd.ApparmorProfile = c.String("containerd-worker-apparmor-profile")
	}
	if c.IsSet("containerd-worker-selinux") {
		cfg.Workers.Containerd.SELinux = c.Bool("containerd-worker-selinux")
	}

	return nil
}

func containerdWorkerInitializer(c *cli.Context, common workerInitializerOpt) ([]worker.Worker, error) {
	if err := applyContainerdFlags(c, common.config); err != nil {
		return nil, err
	}

	cfg := common.config.Workers.Containerd

	if (cfg.Enabled == nil && !validContainerdSocket(cfg)) || (cfg.Enabled != nil && !*cfg.Enabled) {
		return nil, nil
	}

	if cfg.Rootless {
		logrus.Debugf("running in rootless mode")
		if common.config.Workers.Containerd.NetworkConfig.Mode == "auto" {
			common.config.Workers.Containerd.NetworkConfig.Mode = "host"
		}
	}

	dns := getDNSConfig(common.config.DNS)

	nc := netproviders.Opt{
		Mode: common.config.Workers.Containerd.NetworkConfig.Mode,
		CNI: cniprovider.Opt{
			Root:       common.config.Root,
			ConfigPath: common.config.Workers.Containerd.CNIConfigPath,
			BinaryDir:  common.config.Workers.Containerd.CNIBinaryPath,
			PoolSize:   common.config.Workers.Containerd.CNIPoolSize,
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
	opt, err := containerd.NewWorkerOpt(common.config.Root, cfg.Address, snapshotter, cfg.Namespace, cfg.Rootless, cfg.Labels, dns, nc, common.config.Workers.Containerd.ApparmorProfile, common.config.Workers.Containerd.SELinux, parallelismSem, common.traceSocket, ctd.WithTimeout(60*time.Second))
	if err != nil {
		return nil, err
	}
	opt.GCPolicy = getGCPolicy(cfg.GCConfig, common.config.Root)
	opt.BuildkitVersion = getBuildkitVersion()
	opt.RegistryHosts = resolverFunc(common.config)

	if platformsStr := cfg.Platforms; len(platformsStr) != 0 {
		platforms, err := parsePlatforms(platformsStr)
		if err != nil {
			return nil, errors.Wrap(err, "invalid platforms")
		}
		opt.Platforms = platforms
	}
	w, err := base.NewWorker(context.TODO(), opt)
	if err != nil {
		return nil, err
	}
	return []worker.Worker{w}, nil
}

func validContainerdSocket(cfg config.ContainerdConfig) bool {
	socket := cfg.Address
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
	c, err := ctd.New(socketPath, ctd.WithDefaultNamespace(cfg.Namespace))
	if err != nil {
		logrus.Warnf("skipping containerd worker, as failed to connect client to %q: %v", socketPath, err)
		return false
	}
	if _, err := c.Server(context.Background()); err != nil {
		logrus.Warnf("skipping containerd worker, as failed to call introspection API on %q: %v", socketPath, err)
		return false
	}
	return true
}
