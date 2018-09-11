package integration

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/moby/buildkit/util/contentutil"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Sandbox interface {
	Address() string
	PrintLogs(*testing.T)
	Cmd(...string) *exec.Cmd
	NewRegistry() (string, error)
	Rootless() bool
}

type Worker interface {
	New(...SandboxOpt) (Sandbox, func() error, error)
	Name() string
}

type SandboxConf struct {
	mirror string
}

type SandboxOpt func(*SandboxConf)

func WithMirror(h string) SandboxOpt {
	return func(c *SandboxConf) {
		c.mirror = h
	}
}

type Test func(*testing.T, Sandbox)

var defaultWorkers []Worker

func register(w Worker) {
	defaultWorkers = append(defaultWorkers, w)
}

func List() []Worker {
	return defaultWorkers
}

func Run(t *testing.T, testCases []Test) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	mirrorDir := os.Getenv("BUILDKIT_REGISTRY_MIRROR_DIR")

	var f *os.File
	if mirrorDir != "" {
		var err error
		f, err = os.Create(filepath.Join(mirrorDir, "lock"))
		require.NoError(t, err)
		// defer f.Close() // this defer runs after subtest, cleanup on exit

		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		require.NoError(t, err)
	}

	mirror, cleanup, err := newRegistry(mirrorDir)
	require.NoError(t, err)
	_ = cleanup
	// defer cleanup() // this defer runs after subtest, cleanup on exit

	err = copyImagesLocal(t, mirror)
	require.NoError(t, err)

	if mirrorDir != "" {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		require.NoError(t, err)
	}

	for _, br := range List() {
		for _, tc := range testCases {
			ok := t.Run(getFunctionName(tc)+"/worker="+br.Name(), func(t *testing.T) {
				sb, close, err := br.New(WithMirror(mirror))
				if err != nil {
					if errors.Cause(err) == ErrorRequirements {
						t.Skip(err.Error())
					}
					require.NoError(t, err)
				}
				defer func() {
					assert.NoError(t, close())
					if t.Failed() {
						sb.PrintLogs(t)
					}
				}()
				tc(t, sb)
			})
			require.True(t, ok)
		}
	}
}

func getFunctionName(i interface{}) string {
	fullname := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	dot := strings.LastIndex(fullname, ".") + 1
	return strings.Title(fullname[dot:])
}

func copyImagesLocal(t *testing.T, host string) error {
	for to, from := range offlineImages() {
		desc, provider, err := contentutil.ProviderFromRef(from)
		if err != nil {
			return err
		}
		ingester, err := contentutil.IngesterFromRef(host + "/" + to)
		if err != nil {
			return err
		}
		if err := contentutil.CopyChain(context.TODO(), ingester, provider, desc); err != nil {
			return err
		}
		t.Logf("copied %s to local mirror %s", from, host+"/"+to)
	}
	return nil
}

func offlineImages() map[string]string {
	arch := runtime.GOARCH
	if arch == "arm64" {
		arch = "arm64v8"
	}
	return map[string]string{
		"library/busybox:latest": "docker.io/" + arch + "/busybox:latest",
		"library/alpine:latest":  "docker.io/" + arch + "/alpine:latest",
	}
}

func configWithMirror(mirror string) (string, error) {
	tmpdir, err := ioutil.TempDir("", "bktest_config")
	if err != nil {
		return "", err
	}
	if err := ioutil.WriteFile(filepath.Join(tmpdir, "buildkitd.toml"), []byte(fmt.Sprintf(`
[registry."docker.io"]
mirrors=["%s"]
`, mirror)), 0600); err != nil {
		return "", err
	}
	return tmpdir, nil
}
