package helpers

import (
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
		return "", "", nil, errors.Errorf("fake-gcs-server binary not found: %w", err)
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
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// #nosec G107 -- addr is local and for integration tests
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.Errorf("server did not become ready within %s", timeout)
}

func createBucket(addr, bucket string) error {
	reqBody := fmt.Sprintf(`{"name": "%s"}`, bucket)
	url := fmt.Sprintf("%s/b?project=test", addr)
	// #nosec G107 -- addr is local and for integration tests
	resp, err := http.Post(url, "application/json", strings.NewReader(reqBody))
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
