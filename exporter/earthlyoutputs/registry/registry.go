package registry

import (
	"context"
	"net/http"
	"strings"

	"github.com/docker/distribution/configuration"
	"github.com/docker/distribution/registry/handlers"
	"github.com/docker/distribution/registry/listener"
	_ "github.com/moby/buildkit/exporter/earthlyoutputs/registry/eodriver" // register the driver
)

// Serve creates a registry service and starts listening for connections on listenAddr.
// Cancelling the context shuts down the server.
func Serve(ctx context.Context, listenAddr string) chan error {
	serveErr := make(chan error, 1)
	config, err := configuration.Parse(strings.NewReader(`
version: 0.1
storage:
  eodriver:
    maxthreads: 100`))
	if err != nil {
		serveErr <- err
		return serveErr
	}
	ln, err := listener.NewListener("tcp", listenAddr)
	if err != nil {
		serveErr <- err
		return serveErr
	}
	app := handlers.NewApp(ctx, config)
	server := &http.Server{
		Handler: app,
	}
	ctx2, cancel := context.WithCancel(ctx)
	go func() {
		serveErr <- server.Serve(ln)
		cancel()
	}()
	go func() {
		<-ctx2.Done()
		server.Close() // this does not drain
	}()
	return serveErr
}
