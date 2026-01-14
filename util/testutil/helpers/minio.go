package helpers

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
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
)

type MinioOpts struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
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
	l, err := listener.Listen(context.TODO(), "tcp", "localhost:0")
	if err != nil {
		return "", "", nil, err
	}

	addr := l.Addr().String()
	if err = l.Close(); err != nil {
		return "", "", nil, err
	}
	address = "http://" + addr

	// start server
	cmd := exec.CommandContext(context.TODO(), minioBin, "server", "--json", "--address", addr, t.TempDir())
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

	// create alias config
	alias := randomString(10)
	cmd = exec.CommandContext(context.TODO(), mcBin, "alias", "set", alias, address, opts.AccessKeyID, opts.SecretAccessKey)
	if err := integration.RunCmd(cmd, sb.Logs()); err != nil {
		return "", "", nil, err
	}
	deferF.Append(func() error {
		return exec.CommandContext(context.TODO(), mcBin, "alias", "rm", alias).Run()
	})

	// create bucket
	cmd = exec.CommandContext(context.TODO(), mcBin, "mb", "--region", opts.Region, fmt.Sprintf("%s/%s", alias, bucket)) // #nosec G204
	if err := integration.RunCmd(cmd, sb.Logs()); err != nil {
		return "", "", nil, err
	}

	// trace
	cmd = exec.CommandContext(context.TODO(), mcBin, "admin", "trace", "--json", alias)
	traceStop, err := integration.StartCmd(cmd, sb.Logs())
	if err != nil {
		return "", "", nil, err
	}
	deferF.Append(traceStop)

	return
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
