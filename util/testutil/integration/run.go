package integration

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/util/contentutil"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
	Value(string) interface{} // chosen matrix value
}

type Worker interface {
	New(...SandboxOpt) (Sandbox, func() error, error)
	Name() string
}

type SandboxConf struct {
	mirror string
	mv     matrixValue
}

type SandboxOpt func(*SandboxConf)

func WithMirror(h string) SandboxOpt {
	return func(c *SandboxConf) {
		c.mirror = h
	}
}

func withMatrixValues(mv matrixValue) SandboxOpt {
	return func(c *SandboxConf) {
		c.mv = mv
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

type TestOpt func(*TestConf)

func WithMatrix(key string, m map[string]interface{}) TestOpt {
	return func(tc *TestConf) {
		if tc.matrix == nil {
			tc.matrix = map[string]map[string]interface{}{}
		}
		tc.matrix[key] = m
	}
}

func WithMirroredImages(m map[string]string) TestOpt {
	return func(tc *TestConf) {
		if tc.mirroredImages == nil {
			tc.mirroredImages = map[string]string{}
		}
		for k, v := range m {
			tc.mirroredImages[k] = v
		}
	}
}

type TestConf struct {
	matrix         map[string]map[string]interface{}
	mirroredImages map[string]string
}

func Run(t *testing.T, testCases []Test, opt ...TestOpt) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	var tc TestConf
	for _, o := range opt {
		o(&tc)
	}

	mirror, cleanup, err := runMirror(t, tc.mirroredImages)
	require.NoError(t, err)

	var mu sync.Mutex
	var count int
	cleanOnComplete := func() func() {
		count++
		return func() {
			mu.Lock()
			count--
			if count == 0 {
				cleanup()
			}
			mu.Unlock()
		}
	}
	defer cleanOnComplete()()

	matrix := prepareValueMatrix(tc)

	list := List()
	if os.Getenv("BUILDKIT_WORKER_RANDOM") == "1" && len(list) > 0 {
		rand.Seed(time.Now().UnixNano())
		list = []Worker{list[rand.Intn(len(list))]}
	}

	for _, br := range list {
		for _, tc := range testCases {
			for _, mv := range matrix {
				fn := getFunctionName(tc)
				name := fn + "/worker=" + br.Name() + mv.functionSuffix()
				func(fn, testName string, br Worker, tc Test, mv matrixValue) {
					ok := t.Run(testName, func(t *testing.T) {
						defer cleanOnComplete()()
						if !strings.HasSuffix(fn, "NoParallel") {
							t.Parallel()
						}
						sb, close, err := br.New(WithMirror(mirror), withMatrixValues(mv))
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
				}(fn, name, br, tc, mv)
			}
		}
	}
}

func getFunctionName(i interface{}) string {
	fullname := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	dot := strings.LastIndex(fullname, ".") + 1
	return strings.Title(fullname[dot:])
}

var localImageCache map[string]map[string]struct{}

func copyImagesLocal(t *testing.T, host string, images map[string]string) error {
	for to, from := range images {
		if localImageCache == nil {
			localImageCache = map[string]map[string]struct{}{}
		}
		if _, ok := localImageCache[host]; !ok {
			localImageCache[host] = map[string]struct{}{}
		}
		if _, ok := localImageCache[host][to]; ok {
			continue
		}
		localImageCache[host][to] = struct{}{}

		var desc ocispec.Descriptor
		var provider content.Provider
		var err error
		if strings.HasPrefix(from, "local:") {
			var closer func()
			desc, provider, closer, err = providerFromBinary(strings.TrimPrefix(from, "local:"))
			if err != nil {
				return err
			}
			if closer != nil {
				defer closer()
			}
		} else {
			desc, provider, err = contentutil.ProviderFromRef(from)
			if err != nil {
				return err
			}
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

func OfficialImages(names ...string) map[string]string {
	ns := runtime.GOARCH
	if ns == "arm64" {
		ns = "arm64v8"
	} else if ns != "amd64" && ns != "armhf" {
		ns = "library"
	}
	m := map[string]string{}
	for _, name := range names {
		m["library/"+name] = "docker.io/" + ns + "/" + name
	}
	return m
}

func configWithMirror(mirror string) (string, error) {
	tmpdir, err := ioutil.TempDir("", "bktest_config")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(tmpdir, 0711); err != nil {
		return "", err
	}
	if err := ioutil.WriteFile(filepath.Join(tmpdir, "buildkitd.toml"), []byte(fmt.Sprintf(`
[registry."docker.io"]
mirrors=["%s"]
`, mirror)), 0644); err != nil {
		return "", err
	}
	return tmpdir, nil
}

func runMirror(t *testing.T, mirroredImages map[string]string) (host string, _ func() error, err error) {
	mirrorDir := os.Getenv("BUILDKIT_REGISTRY_MIRROR_DIR")

	var f *os.File
	if mirrorDir != "" {
		f, err = os.Create(filepath.Join(mirrorDir, "lock"))
		if err != nil {
			return "", nil, err
		}
		defer func() {
			if err != nil {
				f.Close()
			}
		}()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			return "", nil, err
		}
	}

	mirror, cleanup, err := newRegistry(mirrorDir)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	if err := copyImagesLocal(t, mirror, mirroredImages); err != nil {
		return "", nil, err
	}

	if mirrorDir != "" {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			return "", nil, err
		}
	}

	return mirror, cleanup, err
}

type matrixValue struct {
	fn     []string
	values map[string]matrixValueChoice
}

func (mv matrixValue) functionSuffix() string {
	if len(mv.fn) == 0 {
		return ""
	}
	sort.Strings(mv.fn)
	sb := &strings.Builder{}
	for _, f := range mv.fn {
		sb.Write([]byte("/" + f + "=" + mv.values[f].name))
	}
	return sb.String()
}

type matrixValueChoice struct {
	name  string
	value interface{}
}

func newMatrixValue(key, name string, v interface{}) matrixValue {
	return matrixValue{
		fn: []string{key},
		values: map[string]matrixValueChoice{
			key: {
				name:  name,
				value: v,
			},
		},
	}
}

func prepareValueMatrix(tc TestConf) []matrixValue {
	m := []matrixValue{}
	for featureName, values := range tc.matrix {
		current := m
		m = []matrixValue{}
		for featureValue, v := range values {
			if len(current) == 0 {
				m = append(m, newMatrixValue(featureName, featureValue, v))
			}
			for _, c := range current {
				vv := newMatrixValue(featureName, featureValue, v)
				vv.fn = append(vv.fn, c.fn...)
				for k, v := range c.values {
					vv.values[k] = v
				}
				m = append(m, vv)
			}
		}
	}
	if len(m) == 0 {
		m = append(m, matrixValue{})
	}
	return m
}
