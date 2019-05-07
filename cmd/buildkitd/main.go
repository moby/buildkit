package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/containerd/containerd/pkg/seed"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/sys"
	sddaemon "github.com/coreos/go-systemd/daemon"
	"github.com/desertbit/timer"
	"github.com/docker/docker/pkg/reexec"
	"github.com/docker/go-connections/sockets"
	"github.com/gofrs/flock"
	"github.com/grpc-ecosystem/grpc-opentracing/go/otgrpc"
	"github.com/moby/buildkit/cache/remotecache"
	inlineremotecache "github.com/moby/buildkit/cache/remotecache/inline"
	localremotecache "github.com/moby/buildkit/cache/remotecache/local"
	registryremotecache "github.com/moby/buildkit/cache/remotecache/registry"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/control"
	"github.com/moby/buildkit/frontend"
	dockerfile "github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/frontend/gateway"
	"github.com/moby/buildkit/frontend/gateway/forwarder"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/bboltcachestorage"
	"github.com/moby/buildkit/util/apicaps"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/binfmt_misc"
	"github.com/moby/buildkit/util/profiler"
	"github.com/moby/buildkit/util/resolver"
	"github.com/moby/buildkit/version"
	"github.com/moby/buildkit/worker"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runc/libcontainer/system"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func init() {
	apicaps.ExportedProduct = "buildkit"
	seed.WithTimeAndRand()
	reexec.Init()
}

type workerInitializerOpt struct {
	config *config.Config
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
	cli.VersionPrinter = func(c *cli.Context) {
		fmt.Println(c.App.Name, version.Package, c.App.Version, version.Revision)
	}
	app := cli.NewApp()
	app.Name = "buildkitd"
	app.Usage = "build daemon"
	app.Version = version.Version

	defaultConf, md, err := defaultConf()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%+v\n", err)
		os.Exit(1)
	}

	rootlessUsage := "set all the default options to be compatible with rootless containers"
	if system.RunningInUserNS() {
		app.Flags = append(app.Flags, cli.BoolTFlag{
			Name:  "rootless",
			Usage: rootlessUsage + " (default: true)",
		})
	} else {
		app.Flags = append(app.Flags, cli.BoolFlag{
			Name:  "rootless",
			Usage: rootlessUsage,
		})
	}

	groupValue := func(gid int) string {
		if md == nil || !md.IsDefined("grpc", "gid") {
			return ""
		}
		return strconv.Itoa(gid)
	}

	app.Flags = append(app.Flags,
		cli.StringFlag{
			Name:  "config",
			Usage: "path to config file",
			Value: defaultConfigPath(),
		},
		cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug output in logs",
		},
		cli.StringFlag{
			Name:  "root",
			Usage: "path to state directory",
			Value: defaultConf.Root,
		},
		cli.StringSliceFlag{
			Name:  "addr",
			Usage: "listening address (socket or tcp)",
			Value: &cli.StringSlice{defaultConf.GRPC.Address[0]},
		},
		cli.StringFlag{
			Name:  "group",
			Usage: "group (name or gid) which will own all Unix socket listening addresses",
			Value: groupValue(defaultConf.GRPC.GID),
		},
		cli.StringFlag{
			Name:  "debugaddr",
			Usage: "debugging address (eg. 0.0.0.0:6060)",
			Value: defaultConf.GRPC.DebugAddress,
		},
		cli.StringFlag{
			Name:  "tlscert",
			Usage: "certificate file to use",
			Value: defaultConf.GRPC.TLS.Cert,
		},
		cli.StringFlag{
			Name:  "tlskey",
			Usage: "key file to use",
			Value: defaultConf.GRPC.TLS.Key,
		},
		cli.StringFlag{
			Name:  "tlscacert",
			Usage: "ca certificate to verify clients",
			Value: defaultConf.GRPC.TLS.CA,
		},
		cli.StringSliceFlag{
			Name:  "allow-insecure-entitlement",
			Usage: "allows insecure entitlements e.g. network.host, security.insecure",
		},
		cli.IntFlag{
			Name:  "persistence",
			Usage: "shutdown if idle in value seconds",
		},
	)
	app.Flags = append(app.Flags, appFlags...)

	app.Action = func(c *cli.Context) error {
		if os.Geteuid() != 0 {
			return errors.New("rootless mode requires to be executed as the mapped root in a user namespace; you may use RootlessKit for setting up the namespace")
		}
		ctx, cancel := context.WithCancel(appcontext.Context())
		defer cancel()

		cfg, md, err := config.LoadFile(c.GlobalString("config"))
		if err != nil {
			return err
		}

		setDefaultConfig(&cfg)
		if err := applyMainFlags(c, &cfg, md); err != nil {
			return err
		}

		if cfg.Debug {
			logrus.SetLevel(logrus.DebugLevel)
		}

		if cfg.GRPC.DebugAddress != "" {
			if err := setupDebugHandlers(cfg.GRPC.DebugAddress); err != nil {
				return err
			}
		}
		persistence := time.Duration(c.GlobalInt("persistence")) * time.Second
		shutdownTimer := timer.NewStoppedTimer()
		stopShutdownTimer := func() {
			logrus.Debugf("stopping timer")
			shutdownTimer.Stop()
		}
		resetShutdownTimer := func() {
			if persistence != 0 {
				logrus.Debugf("resetting shutdown timer (persistence=%v)", persistence)
				shutdownTimer.Reset(persistence)
			}
		}
		opts := []grpc.ServerOption{unaryInterceptor(ctx, stopShutdownTimer, resetShutdownTimer), streamInterceptor(stopShutdownTimer, resetShutdownTimer)}
		creds, err := serverCredentials(cfg.GRPC.TLS)
		if err != nil {
			return err
		}
		if creds != nil {
			opts = append(opts, creds)
		}
		server := grpc.NewServer(opts...)

		// relative path does not work with nightlyone/lockfile
		root, err := filepath.Abs(cfg.Root)
		if err != nil {
			return err
		}
		cfg.Root = root

		if err := os.MkdirAll(root, 0700); err != nil {
			return errors.Wrapf(err, "failed to create %s", root)
		}

		lockPath := filepath.Join(root, "buildkitd.lock")
		lock := flock.New(lockPath)
		locked, err := lock.TryLock()
		if err != nil {
			return errors.Wrapf(err, "could not lock %s", lockPath)
		}
		if !locked {
			return errors.Errorf("could not lock %s, another instance running?", lockPath)
		}
		defer func() {
			lock.Unlock()
			os.RemoveAll(lockPath)
		}()

		controller, err := newController(c, &cfg)
		if err != nil {
			return err
		}

		controller.Register(server)

		ents := c.GlobalStringSlice("allow-insecure-entitlement")
		if len(ents) > 0 {
			cfg.Entitlements = []string{}
			for _, e := range ents {
				switch e {
				case "security.insecure":
					cfg.Entitlements = append(cfg.Entitlements, e)
				case "network.host":
					cfg.Entitlements = append(cfg.Entitlements, e)
				default:
					return fmt.Errorf("invalid entitlement : %v", e)
				}
			}
		}
		errCh := make(chan error, 1)
		if err := serveGRPC(cfg.GRPC, server, errCh); err != nil {
			return err
		}

		select {
		case serverErr := <-errCh:
			err = serverErr
			cancel()
		case <-ctx.Done():
			err = ctx.Err()
		case <-shutdownTimer.C:
			logrus.Infof("shutdown timer fired because no RPC activity in last %v", persistence)
			if persistence == 0 {
			}
		}

		logrus.Infof("stopping server")
		if os.Getenv("NOTIFY_SOCKET") != "" {
			notified, notifyErr := sddaemon.SdNotify(false, sddaemon.SdNotifyStopping)
			logrus.Debugf("SdNotifyStopping notified=%v, err=%v", notified, notifyErr)
		}
		server.GracefulStop()

		return err
	}

	app.After = func(context *cli.Context) error {
		if closeTracer != nil {
			return closeTracer.Close()
		}
		return nil
	}

	profiler.Attach(app)

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "buildkitd: %s\n", err)
		os.Exit(1)
	}
}

func serveGRPC(cfg config.GRPCConfig, server *grpc.Server, errCh chan error) error {
	addrs := cfg.Address
	if len(addrs) == 0 {
		return errors.New("--addr cannot be empty")
	}
	eg, _ := errgroup.WithContext(context.Background())
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		l, err := getListener(cfg, addr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return err
		}
		listeners = append(listeners, l)
	}

	if os.Getenv("NOTIFY_SOCKET") != "" {
		notified, notifyErr := sddaemon.SdNotify(false, sddaemon.SdNotifyReady)
		logrus.Debugf("SdNotifyReady notified=%v, err=%v", notified, notifyErr)
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

func defaultConfigPath() string {
	if system.RunningInUserNS() {
		return filepath.Join(appdefaults.UserConfigDir(), "buildkitd.toml")
	}
	return filepath.Join(appdefaults.ConfigDir, "buildkitd.toml")
}

func defaultConf() (config.Config, *toml.MetaData, error) {
	cfg, md, err := config.LoadFile(defaultConfigPath())
	if err != nil {
		if _, ok := errors.Cause(err).(*os.PathError); !ok {
			return config.Config{}, nil, err
		}
		return cfg, nil, nil
	}
	setDefaultConfig(&cfg)

	return cfg, md, nil
}

func setDefaultConfig(cfg *config.Config) {
	orig := *cfg

	if cfg.Root == "" {
		cfg.Root = appdefaults.Root
	}

	if len(cfg.GRPC.Address) == 0 {
		cfg.GRPC.Address = []string{appdefaults.Address}
	}

	if cfg.Workers.OCI.Platforms == nil {
		cfg.Workers.OCI.Platforms = binfmt_misc.SupportedPlatforms()
	}
	if cfg.Workers.Containerd.Platforms == nil {
		cfg.Workers.Containerd.Platforms = binfmt_misc.SupportedPlatforms()
	}

	if system.RunningInUserNS() {
		// if buildkitd is being executed as the mapped-root (not only EUID==0 but also $USER==root)
		// in a user namespace, we need to enable the rootless mode but
		// we don't want to honor $HOME for setting up default paths.
		if u := os.Getenv("USER"); u != "" && u != "root" {
			if orig.Root == "" {
				cfg.Root = appdefaults.UserRoot()
			}
			if len(orig.GRPC.Address) == 0 {
				cfg.GRPC.Address = []string{appdefaults.UserAddress()}
			}
			appdefaults.EnsureUserAddressDir()
		}
	}
}

func applyMainFlags(c *cli.Context, cfg *config.Config, md *toml.MetaData) error {
	if c.IsSet("debug") {
		cfg.Debug = c.Bool("debug")
	}
	if c.IsSet("root") {
		cfg.Root = c.String("root")
	}

	if c.IsSet("addr") || len(cfg.GRPC.Address) == 0 {
		addrs := c.StringSlice("addr")
		if len(addrs) > 1 {
			addrs = addrs[1:] // https://github.com/urfave/cli/issues/160
		}

		cfg.GRPC.Address = make([]string, 0, len(addrs))
		for _, v := range addrs {
			cfg.GRPC.Address = append(cfg.GRPC.Address, v)
		}
	}

	if c.IsSet("allow-insecure-entitlement") {
		//override values from config
		cfg.Entitlements = c.StringSlice("allow-insecure-entitlement")
	}

	if c.IsSet("debugaddr") {
		cfg.GRPC.DebugAddress = c.String("debugaddr")
	}

	if md == nil || !md.IsDefined("grpc", "uid") {
		cfg.GRPC.UID = os.Getuid()
	}

	if md == nil || !md.IsDefined("grpc", "gid") {
		cfg.GRPC.GID = os.Getgid()
	}

	if group := c.String("group"); group != "" {
		gid, err := groupToGid(group)
		if err != nil {
			return err
		}
		cfg.GRPC.GID = gid
	}

	if tlscert := c.String("tlscert"); tlscert != "" {
		cfg.GRPC.TLS.Cert = tlscert
	}
	if tlskey := c.String("tlskey"); tlskey != "" {
		cfg.GRPC.TLS.Key = tlskey
	}
	if tlsca := c.String("tlscacert"); tlsca != "" {
		cfg.GRPC.TLS.CA = tlsca
	}
	return nil
}

// Convert a string containing either a group name or a stringified gid into a numeric id)
func groupToGid(group string) (int, error) {
	if group == "" {
		return os.Getgid(), nil
	}

	var (
		err error
		id  int
	)

	// Try and parse as a number, if the error is ErrSyntax
	// (i.e. its not a number) then we carry on and try it as a
	// name.
	if id, err = strconv.Atoi(group); err == nil {
		return id, nil
	} else if err.(*strconv.NumError).Err != strconv.ErrSyntax {
		return 0, err
	}

	ginfo, err := user.LookupGroup(group)
	if err != nil {
		return 0, err
	}
	group = ginfo.Gid

	if id, err = strconv.Atoi(group); err != nil {
		return 0, err
	}

	return id, nil
}

func getListener(cfg config.GRPCConfig, addr string) (net.Listener, error) {
	addrSlice := strings.SplitN(addr, "://", 2)
	if len(addrSlice) < 2 {
		return nil, errors.Errorf("address %s does not contain proto, you meant unix://%s ?",
			addr, addr)
	}
	proto := addrSlice[0]
	listenAddr := addrSlice[1]
	switch proto {
	case "unix", "npipe":
		return sys.GetLocalListener(listenAddr, cfg.UID, cfg.GID)
	case "tcp":
		return sockets.NewTCPSocket(listenAddr, nil)
	default:
		return nil, errors.Errorf("addr %s not supported", addr)
	}
}

var (
	currentOps   int64
	currentOpsMu sync.Mutex
)

func unaryInterceptor(globalCtx context.Context, stopShutdownTimer, resetShutdownTimer func()) grpc.ServerOption {
	withTrace := otgrpc.OpenTracingServerInterceptor(tracer, otgrpc.LogPayloads())

	return grpc.UnaryInterceptor(func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		currentOpsMu.Lock()
		currentOps++
		currentOpsMu.Unlock()
		stopShutdownTimer()
		defer func() {
			currentOpsMu.Lock()
			currentOps--
			currentOps0 := currentOps == 0
			currentOpsMu.Unlock()
			// if there is no other call
			if currentOps0 {
				resetShutdownTimer()
			}
		}()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		go func() {
			select {
			case <-ctx.Done():
			case <-globalCtx.Done():
				cancel()
			}
		}()

		resp, err = withTrace(ctx, req, info, handler)
		if err != nil {
			logrus.Errorf("%s returned error: %+v", info.FullMethod, err)
		}
		return
	})
}

func streamInterceptor(stopShutdownTimer, resetShutdownTimer func()) grpc.ServerOption {
	withTrace := otgrpc.OpenTracingStreamServerInterceptor(tracer)
	return grpc.StreamInterceptor(func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		currentOpsMu.Lock()
		currentOps++
		currentOpsMu.Unlock()
		stopShutdownTimer()
		defer func() {
			currentOpsMu.Lock()
			currentOps--
			currentOps0 := currentOps == 0
			currentOpsMu.Unlock()
			// if there is no other call
			if currentOps0 {
				resetShutdownTimer()
			}
		}()
		return withTrace(srv, ss, info, handler)
	})
}

func serverCredentials(cfg config.TLSConfig) (grpc.ServerOption, error) {
	certFile := cfg.Cert
	keyFile := cfg.Key
	caFile := cfg.CA
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	err := errors.New("you must specify key and cert file if one is specified")
	if certFile == "" {
		return nil, err
	}
	if keyFile == "" {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.Wrap(err, "could not load server key pair")
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{certificate},
	}
	if caFile != "" {
		certPool := x509.NewCertPool()
		ca, err := ioutil.ReadFile(caFile)
		if err != nil {
			return nil, errors.Wrap(err, "could not read ca certificate")
		}
		// Append the client certificates from the CA
		if ok := certPool.AppendCertsFromPEM(ca); !ok {
			return nil, errors.New("failed to append ca cert")
		}
		tlsConf.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConf.ClientCAs = certPool
	}
	creds := grpc.Creds(credentials.NewTLS(tlsConf))
	return creds, nil
}

func newController(c *cli.Context, cfg *config.Config) (*control.Controller, error) {
	sessionManager, err := session.NewManager()
	if err != nil {
		return nil, err
	}
	wc, err := newWorkerController(c, workerInitializerOpt{
		config: cfg,
	})
	if err != nil {
		return nil, err
	}
	frontends := map[string]frontend.Frontend{}
	frontends["dockerfile.v0"] = forwarder.NewGatewayForwarder(wc, dockerfile.Build)
	frontends["gateway.v0"] = gateway.NewGatewayFrontend(wc)

	cacheStorage, err := bboltcachestorage.NewStore(filepath.Join(cfg.Root, "cache.db"))
	if err != nil {
		return nil, err
	}

	resolverFn := resolverFunc(cfg)

	remoteCacheExporterFuncs := map[string]remotecache.ResolveCacheExporterFunc{
		"registry": registryremotecache.ResolveCacheExporterFunc(sessionManager, resolverFn),
		"local":    localremotecache.ResolveCacheExporterFunc(sessionManager),
		"inline":   inlineremotecache.ResolveCacheExporterFunc(),
	}
	remoteCacheImporterFuncs := map[string]remotecache.ResolveCacheImporterFunc{
		"registry": registryremotecache.ResolveCacheImporterFunc(sessionManager, resolverFn),
		"local":    localremotecache.ResolveCacheImporterFunc(sessionManager),
	}
	return control.NewController(control.Opt{
		SessionManager:            sessionManager,
		WorkerController:          wc,
		Frontends:                 frontends,
		ResolveCacheExporterFuncs: remoteCacheExporterFuncs,
		ResolveCacheImporterFuncs: remoteCacheImporterFuncs,
		CacheKeyStorage:           cacheStorage,
		Entitlements:              cfg.Entitlements,
	})
}

func resolverFunc(cfg *config.Config) resolver.ResolveOptionsFunc {
	m := map[string]resolver.RegistryConf{}
	for k, v := range cfg.Registries {
		m[k] = resolver.RegistryConf{
			Mirrors:   v.Mirrors,
			PlainHTTP: v.PlainHTTP,
		}
	}
	return resolver.NewResolveOptionsFunc(m)
}

func newWorkerController(c *cli.Context, wiOpt workerInitializerOpt) (*worker.Controller, error) {
	wc := &worker.Controller{}
	nWorkers := 0
	for _, wi := range workerInitializers {
		ws, err := wi.fn(c, wiOpt)
		if err != nil {
			return nil, err
		}
		for _, w := range ws {
			p := formatPlatforms(w.Platforms())
			logrus.Infof("found worker %q, labels=%v, platforms=%v", w.ID(), w.Labels(), p)
			binfmt_misc.WarnIfUnsupported(p)
			if err = wc.Add(w); err != nil {
				return nil, err
			}
			nWorkers++
		}
	}
	if nWorkers == 0 {
		return nil, errors.New("no worker found, rebuild the buildkit daemon?")
	}
	defaultWorker, err := wc.GetDefault()
	if err != nil {
		return nil, err
	}
	logrus.Infof("found %d workers, default=%q", nWorkers, defaultWorker.ID())
	logrus.Warn("currently, only the default worker can be used.")
	return wc, nil
}

func attrMap(sl []string) (map[string]string, error) {
	m := map[string]string{}
	for _, v := range sl {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return nil, errors.Errorf("invalid value %s", v)
		}
		m[parts[0]] = parts[1]
	}
	return m, nil
}

func formatPlatforms(p []specs.Platform) []string {
	str := make([]string, 0, len(p))
	for _, pp := range p {
		str = append(str, platforms.Format(platforms.Normalize(pp)))
	}
	return str
}

func parsePlatforms(platformsStr []string) ([]specs.Platform, error) {
	out := make([]specs.Platform, 0, len(platformsStr))
	for _, s := range platformsStr {
		p, err := platforms.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, platforms.Normalize(p))
	}
	return out, nil
}

func getGCPolicy(cfg config.GCConfig, root string) []client.PruneInfo {
	if cfg.GC != nil && !*cfg.GC {
		return nil
	}
	if len(cfg.GCPolicy) == 0 {
		cfg.GCPolicy = config.DefaultGCPolicy(root, cfg.GCKeepStorage)
	}
	out := make([]client.PruneInfo, 0, len(cfg.GCPolicy))
	for _, rule := range cfg.GCPolicy {
		out = append(out, client.PruneInfo{
			Filter:       rule.Filters,
			All:          rule.All,
			KeepBytes:    rule.KeepBytes,
			KeepDuration: time.Duration(rule.KeepDuration) * time.Second,
		})
	}
	return out
}
