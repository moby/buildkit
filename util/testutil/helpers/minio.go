package helpers

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
)

const (
	minioBin = "minio"
	mcBin    = "mc"
	mcAlias  = "buildkit"
)

type MinioOpts struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	RequestVerifier func(*http.Request) error
}

func NewMinioServer(t *testing.T, sb integration.Sandbox, opts MinioOpts) (address string, bucket string, cl func() error, err error) {
	t.Helper()
	bucket = randomString(10)

	if _, err := exec.LookPath(minioBin); err != nil {
		return "", "", nil, errors.Wrapf(err, "failed to lookup %s binary", minioBin)
	}
	if _, err := exec.LookPath(mcBin); err != nil {
		return "", "", nil, errors.Wrapf(err, "failed to lookup %s binary", mcBin)
	}

	deferF := &integration.MultiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	listener := net.ListenConfig{}
	l, err := listener.Listen(t.Context(), "tcp", "localhost:0")
	if err != nil {
		return "", "", nil, err
	}

	addr := l.Addr().String()
	if err = l.Close(); err != nil {
		return "", "", nil, err
	}
	address = "http://" + addr

	// start server
	cmd := exec.CommandContext(t.Context(), minioBin, "server", "--json", "--address", addr, t.TempDir())
	cmd.Env = append(os.Environ(), []string{
		"MINIO_ROOT_USER=" + opts.AccessKeyID,
		"MINIO_ROOT_PASSWORD=" + opts.SecretAccessKey,
	}...)
	minioStop, err := integration.StartCmd(cmd, sb.Logs())
	if err != nil {
		return "", "", nil, err
	}
	if err = waitMinio(sb.Context(), address, 15*time.Second); err != nil {
		minioStop()
		return "", "", nil, errors.Wrapf(err, "minio did not start up: %s", integration.FormatLogs(sb.Logs()))
	}
	deferF.Append(minioStop)

	// mc keeps its aliases in a single configuration folder that defaults to
	// $HOME/.mc. Servers started in parallel would then race each other while
	// rewriting that file and observe missing aliases, so give every server its
	// own folder. It is passed through the environment rather than as a flag so
	// that no mc invocation can miss it: mc treats "<alias>/<bucket>" of an
	// unknown alias as a local path and silently succeeds on the filesystem.
	mcEnv := append(os.Environ(), "MC_CONFIG_DIR="+t.TempDir())
	mcCmd := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), mcBin, args...)
		cmd.Env = mcEnv
		return cmd
	}

	// create alias config
	if err := integration.RunCmd(mcCmd("alias", "set", mcAlias, address, opts.AccessKeyID, opts.SecretAccessKey), sb.Logs()); err != nil {
		return "", "", nil, err
	}

	// create bucket
	if err := integration.RunCmd(mcCmd("mb", "--region", opts.Region, fmt.Sprintf("%s/%s", mcAlias, bucket)), sb.Logs()); err != nil {
		return "", "", nil, err
	}

	// trace
	traceStop, err := integration.StartCmd(mcCmd("admin", "trace", "--json", mcAlias), sb.Logs())
	if err != nil {
		return "", "", nil, err
	}
	deferF.Append(traceStop)

	if opts.RequestVerifier != nil {
		proxyAddr, proxyStop, err := newMinioProxy(address, opts.RequestVerifier)
		if err != nil {
			return "", "", nil, err
		}
		deferF.Append(proxyStop)
		address = proxyAddr
	}

	return
}

func newMinioProxy(target string, verify func(*http.Request) error) (string, func() error, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := verify(r); err != nil {
			http.Error(w, err.Error(), http.StatusNotImplemented)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	return server.URL, func() error {
		server.Close()
		return nil
	}, nil
}

func waitMinio(ctx context.Context, address string, d time.Duration) error {
	step := 1 * time.Second
	i := 0
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/minio/health/live", address), nil)
		if err != nil {
			return errors.Wrapf(err, "failed to create request")
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			break
		}
		i++
		if time.Duration(i)*step > d {
			return errors.Errorf("failed dialing: %s", address)
		}
		time.Sleep(step)
	}
	return nil
}

func randomString(n int) string {
	chars := "abcdefghijklmnopqrstuvwxyz"
	var b = make([]byte, n)
	_, _ = rand.Read(b)
	for k, v := range b {
		b[k] = chars[v%byte(len(chars))]
	}
	return string(b)
}
