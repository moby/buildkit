package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/containerd/containerd/sys"
	"github.com/docker/go-connections/sockets"
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/control"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile"
	"github.com/moby/buildkit/frontend/gateway"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/profiler"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

type workerInitializerOpt struct {
	sessionManager *session.Manager
	root           string
}

type workerInitializer struct {
	fn func(c *cli.Context, common workerInitializerOpt) ([]worker.Worker, error)
	// less priority number, more preferred
	priority int
}

var (
	appFlags           []cli.Flag
	workerInitializers []workerInitializer
)

func registerWorkerInitializer(wi workerInitializer, flags ...cli.Flag) {
	workerInitializers = append(workerInitializers, wi)
	sort.Slice(workerInitializers,
		func(i, j int) bool {
			return workerInitializers[i].priority < workerInitializers[j].priority
		})
	appFlags = append(appFlags, flags...)
}

func main() {
	app := cli.NewApp()
	app.Name = "buildkitd"
	app.Usage = "build daemon"

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug output in logs",
		},
		cli.StringFlag{
			Name:  "root",
			Usage: "path to state directory",
			Value: appdefaults.Root,
		},
		cli.StringSliceFlag{
			Name:  "addr",
			Usage: "listening address (socket or tcp)",
			Value: &cli.StringSlice{appdefaults.Address},
		},
		cli.StringFlag{
			Name:  "debugaddr",
			Usage: "debugging address (eg. 0.0.0.0:6060)",
			Value: "",
		},
	}

	app.Flags = append(app.Flags, appFlags...)

	app.Action = func(c *cli.Context) error {
		ctx, cancel := context.WithCancel(appcontext.Context())

		if debugAddr := c.GlobalString("debugaddr"); debugAddr != "" {
			if err := setupDebugHandlers(debugAddr); err != nil {
				return err
			}
		}

		server := grpc.NewServer(unaryInterceptor(ctx))

		// relative path does not work with nightlyone/lockfile
		root, err := filepath.Abs(c.GlobalString("root"))
		if err != nil {
			return err
		}

		if err := os.MkdirAll(root, 0700); err != nil {
			return errors.Wrapf(err, "failed to create %s", root)
		}

		controller, err := newController(c, root)
		if err != nil {
			return err
		}

		controller.Register(server)

		errCh := make(chan error, 1)
		addrs := c.GlobalStringSlice("addr")
		if len(addrs) > 1 {
			addrs = addrs[1:] // https://github.com/urfave/cli/issues/160
		}
		if err := serveGRPC(server, addrs, errCh); err != nil {
			return err
		}

		select {
		case serverErr := <-errCh:
			err = serverErr
			cancel()
		case <-ctx.Done():
			err = ctx.Err()
		}

		logrus.Infof("stopping server")
		server.GracefulStop()

		return err
	}
	app.Before = func(context *cli.Context) error {
		if context.GlobalBool("debug") {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return nil
	}

	profiler.Attach(app)

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "buildkitd: %s\n", err)
		os.Exit(1)
	}
}

func serveGRPC(server *grpc.Server, addrs []string, errCh chan error) error {
	if len(addrs) == 0 {
		return errors.New("--addr cannot be empty")
	}
	eg, _ := errgroup.WithContext(context.Background())
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		l, err := getListener(addr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return err
		}
		listeners = append(listeners, l)
	}
	for _, l := range listeners {
		func(l net.Listener) {
			eg.Go(func() error {
				defer l.Close()
				logrus.Infof("running server on %s", l.Addr())
				return server.Serve(l)
			})
		}(l)
	}
	go func() {
		errCh <- eg.Wait()
	}()
	return nil
}

func getListener(addr string) (net.Listener, error) {
	addrSlice := strings.SplitN(addr, "://", 2)
	proto := addrSlice[0]
	listenAddr := addrSlice[1]
	switch proto {
	case "unix", "npipe":
		return sys.GetLocalListener(listenAddr, os.Getuid(), os.Getgid())
	case "tcp":
		return sockets.NewTCPSocket(listenAddr, nil)
	default:
		return nil, errors.Errorf("addr %s not supported", addr)
	}
}

func unaryInterceptor(globalCtx context.Context) grpc.ServerOption {
	return grpc.UnaryInterceptor(func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		go func() {
			select {
			case <-ctx.Done():
			case <-globalCtx.Done():
				cancel()
			}
		}()

		resp, err = handler(ctx, req)
		if err != nil {
			logrus.Errorf("%s returned error: %+v", info.FullMethod, err)
		}
		return
	})
}

func newController(c *cli.Context, root string) (*control.Controller, error) {
	sessionManager, err := session.NewManager()
	if err != nil {
		return nil, err
	}
	wc, err := newWorkerController(c, workerInitializerOpt{
		sessionManager: sessionManager,
		root:           root,
	})
	if err != nil {
		return nil, err
	}
	frontends := map[string]frontend.Frontend{}
	frontends["dockerfile.v0"] = dockerfile.NewDockerfileFrontend()
	frontends["gateway.v0"] = gateway.NewGatewayFrontend()

	// cache exporter and importer are manager concepts but as there is no
	// way to pull data into specific worker yet we currently set them up
	// as part of default worker
	var ce *cacheimport.CacheExporter
	var ci *cacheimport.CacheImporter

	w, err := wc.GetDefault()
	if err != nil {
		return nil, err
	}

	wt := w.(*base.Worker)
	ce = wt.CacheExporter
	ci = wt.CacheImporter

	return control.NewController(control.Opt{
		SessionManager:   sessionManager,
		WorkerController: wc,
		Frontends:        frontends,
		CacheExporter:    ce,
		CacheImporter:    ci,
	})
}

func newWorkerController(c *cli.Context, wiOpt workerInitializerOpt) (*worker.Controller, error) {
	wc := &worker.Controller{}
	for _, wi := range workerInitializers {
		ws, err := wi.fn(c, wiOpt)
		if err != nil {
			return nil, err
		}
		for _, w := range ws {
			logrus.Infof("found worker %q", w.Name())
			if err = wc.Add(w); err != nil {
				return nil, err
			}
		}
	}
	nWorkers := len(wc.GetAll())
	if nWorkers == 0 {
		return nil, errors.New("no worker found, rebuild the buildkit daemon?")
	}
	defaultWorker, err := wc.GetDefault()
	if err != nil {
		return nil, err
	}
	logrus.Infof("found %d workers, default=%q", nWorkers, defaultWorker.Name)
	logrus.Warn("currently, only the default worker can be used.")
	return wc, nil
}
