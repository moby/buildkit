package helpers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
)

func NewFakeGCSServer(t *testing.T, sb integration.Sandbox) (address, bucket string, cleanup func() error, err error) {
	t.Helper()

	bucket = randomString(10)
	if _, err := exec.LookPath("fake-gcs-server"); err != nil {
		return "", "", nil, errors.Errorf("fake-gcs-server binary not found: %s", err)
	}

	deferF := &integration.MultiCloser{}
	cleanup = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cleanup = nil
		}
	}()

	tmpDir := t.TempDir()

	// Dynamically pick a port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return "", "", nil, err
	}
	addr := listener.Addr().String()
	port := addr[strings.LastIndex(addr, ":")+1:]
	_ = listener.Close()

	serverAddr := fmt.Sprintf("http://localhost:%s/storage/v1", port)

	cmd := exec.Command("fake-gcs-server",
		"-scheme", "http",
		"-port", port,
		"-data", tmpDir,
		"-public-host", "localhost:"+port,
	)

	if err := cmd.Start(); err != nil {
		return "", "", nil, err
	}
	deferF.Append(func() error {
		cmd.Process.Kill()
		return cmd.Wait()
	})

	// Wait for server to be ready
	if err := waitForServer(serverAddr, 10*time.Second); err != nil {
		return "", "", nil, err
	}

	// Create bucket
	if err := createBucket(serverAddr, bucket); err != nil {
		return "", "", nil, err
	}

	return serverAddr, bucket, cleanup, nil
}

func waitForServer(addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeoutCause(context.Background(), timeout, errors.New("timeout waiting for server"))
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return errors.Wrap(context.Cause(ctx), "server did not become ready")
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
			if err != nil {
				continue
			}

			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
				return nil
			}
		}
	}
}

func createBucket(addr, bucket string) error {
	ctx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Second, errors.New("timeout creating bucket"))
	defer cancel()

	reqBody := fmt.Sprintf(`{"name": "%s"}`, bucket)
	url := fmt.Sprintf("%s/b?project=test", addr)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(reqBody))
	if err != nil {
		return errors.Wrap(err, "failed to create HTTP request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Errorf("failed to create bucket: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("failed to create bucket, status: %s, body: %s", resp.Status, string(body))
	}
	return nil
}
