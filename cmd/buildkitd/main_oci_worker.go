// +build linux,!no_oci_worker

package main

import (
	"os/exec"
	"strconv"

	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/containerd/containerd/snapshots/overlay"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/runc"
	"github.com/opencontainers/runc/libcontainer/system"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func init() {
	defaultConf, _ := defaultConf()

	enabledValue := func(b *bool) string {
		if b == nil {
			return "auto"
		}
		return strconv.FormatBool(*b)
	}

	if defaultConf.Workers.OCI.Snapshotter == "" {
		defaultConf.Workers.OCI.Snapshotter = "auto"
	}

	flags := []cli.Flag{
		cli.StringFlag{
			Name:  "oci-worker",
			Usage: "enable oci workers (true/false/auto)",
			Value: enabledValue(defaultConf.Workers.OCI.Enabled),
		},
		cli.StringSliceFlag{
			Name:  "oci-worker-labels",
			Usage: "user-specific annotation labels (com.example.foo=bar)",
		},
		cli.StringFlag{
			Name:  "oci-worker-snapshotter",
			Usage: "name of snapshotter (overlayfs or native)",
			Value: defaultConf.Workers.OCI.Snapshotter,
		},
		cli.StringSliceFlag{
			Name:  "oci-worker-platform",
			Usage: "override supported platforms for worker",
		},
	}
	n := "oci-worker-rootless"
	u := "enable rootless mode"
	if system.RunningInUserNS() {
		flags = append(flags, cli.BoolTFlag{
			Name:  n,
			Usage: u,
		})
	} else {
		flags = append(flags, cli.BoolFlag{
			Name:  n,
			Usage: u,
		})
	}
	flags = append(flags, cli.BoolFlag{
		Name:  "oci-worker-no-process-sandbox",
		Usage: "use the host PID namespace and procfs (WARNING: allows build containers to kill (and potentially ptrace) an arbitrary process in the host namespace)",
	})
	if defaultConf.Workers.OCI.GC == nil || *defaultConf.Workers.OCI.GC {
		flags = append(flags, cli.BoolTFlag{
			Name:  "oci-worker-gc",
			Usage: "Enable automatic garbage collection on worker",
		})
	} else {
		flags = append(flags, cli.BoolFlag{
			Name:  "oci-worker-gc",
			Usage: "Enable automatic garbage collection on worker",
		})
	}
	flags = append(flags, cli.Int64Flag{
		Name:  "oci-worker-gc-keepstorage",
		Usage: "Amount of storage GC keep locally (MB)",
		Value: func() int64 {
			if defaultConf.Workers.OCI.GCKeepStorage != 0 {
				return defaultConf.Workers.OCI.GCKeepStorage / 1e6
			}
			return config.DetectDefaultGCCap(defaultConf.Root) / 1e6
		}(),
		Hidden: len(defaultConf.Workers.OCI.GCPolicy) != 0,
	})

	registerWorkerInitializer(
		workerInitializer{
			fn:       ociWorkerInitializer,
			priority: 0,
		},
		flags...,
	)
	// TODO: allow multiple oci runtimes
}

func applyOCIFlags(c *cli.Context, cfg *config.Config) error {
	if cfg.Workers.OCI.Snapshotter == "" {
		cfg.Workers.OCI.Snapshotter = "auto"
	}

	if c.GlobalIsSet("oci-worker") {
		boolOrAuto, err := parseBoolOrAuto(c.GlobalString("oci-worker"))
		if err != nil {
			return err
		}
		cfg.Workers.OCI.Enabled = boolOrAuto
	}

	labels, err := attrMap(c.GlobalStringSlice("oci-worker-labels"))
	if err != nil {
		return err
	}
	if cfg.Workers.OCI.Labels == nil {
		cfg.Workers.OCI.Labels = make(map[string]string)
	}
	for k, v := range labels {
		cfg.Workers.OCI.Labels[k] = v
	}
	if c.GlobalIsSet("oci-worker-snapshotter") {
		cfg.Workers.OCI.Snapshotter = c.GlobalString("oci-worker-snapshotter")
	}

	if c.GlobalIsSet("rootless") || c.GlobalBool("rootless") {
		cfg.Workers.OCI.Rootless = c.GlobalBool("rootless")
	}
	if c.GlobalIsSet("oci-worker-rootless") {
		cfg.Workers.OCI.Rootless = c.GlobalBool("oci-worker-rootless")
	}
	if c.GlobalIsSet("oci-worker-no-process-sandbox") {
		cfg.Workers.OCI.NoProcessSandbox = c.GlobalBool("oci-worker-no-process-sandbox")
	}

	if platforms := c.GlobalStringSlice("oci-worker-platform"); len(platforms) != 0 {
		cfg.Workers.OCI.Platforms = platforms
	}

	if c.GlobalIsSet("oci-worker-gc") {
		v := c.GlobalBool("oci-worker-gc")
		cfg.Workers.OCI.GC = &v
	}

	if c.GlobalIsSet("oci-worker-gc-keepstorage") {
		cfg.Workers.OCI.GCKeepStorage = c.GlobalInt64("oci-worker-gc-keepstorage") * 1e6
	}

	return nil
}

func ociWorkerInitializer(c *cli.Context, common workerInitializerOpt) ([]worker.Worker, error) {
	if err := applyOCIFlags(c, common.config); err != nil {
		return nil, err
	}

	cfg := common.config.Workers.OCI

	if (cfg.Enabled == nil && !validOCIBinary()) || (cfg.Enabled != nil && !*cfg.Enabled) {
		return nil, nil
	}

	snFactory, err := snapshotterFactory(common.config.Root, cfg.Snapshotter)
	if err != nil {
		return nil, err
	}

	if cfg.Rootless {
		logrus.Debugf("running in rootless mode")
	}

	processMode := oci.ProcessSandbox
	if cfg.NoProcessSandbox {
		logrus.Warn("NoProcessSandbox is enabled. Note that NoProcessSandbox allows build containers to kill (and potentially ptrace) an arbitrary process in the BuildKit host namespace. NoProcessSandbox should be enabled only when the BuildKit is running in a container as an unprivileged user.")
		if !cfg.Rootless {
			return nil, errors.New("can't enable NoProcessSandbox without Rootless")
		}
		processMode = oci.NoProcessSandbox
	}

	opt, err := runc.NewWorkerOpt(common.config.Root, snFactory, cfg.Rootless, processMode, cfg.Labels)
	if err != nil {
		return nil, err
	}
	opt.SessionManager = common.sessionManager
	opt.GCPolicy = getGCPolicy(cfg.GCConfig, common.config.Root)
	opt.ResolveOptionsFunc = resolverFunc(common.config)

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

func snapshotterFactory(commonRoot, name string) (runc.SnapshotterFactory, error) {
	if name == "auto" {
		if err := overlay.Supported(commonRoot); err == nil {
			logrus.Debug("auto snapshotter: using overlayfs")
			name = "overlayfs"
		} else {
			logrus.Debugf("auto snapshotter: using native, because overlayfs is not available for %s: %v", commonRoot, err)
			name = "native"
		}
	}
	snFactory := runc.SnapshotterFactory{
		Name: name,
	}
	switch name {
	case "native":
		snFactory.New = native.NewSnapshotter
	case "overlayfs": // not "overlay", for consistency with containerd snapshotter plugin ID.
		snFactory.New = func(root string) (ctdsnapshot.Snapshotter, error) {
			return overlay.NewSnapshotter(root)
		}
	default:
		return snFactory, errors.Errorf("unknown snapshotter name: %q", name)
	}
	return snFactory, nil
}

func validOCIBinary() bool {
	_, err := exec.LookPath("runc")
	_, err1 := exec.LookPath("buildkit-runc")
	if err != nil && err1 != nil {
		logrus.Warnf("skipping oci worker, as runc does not exist")
		return false
	}
	return true
}
