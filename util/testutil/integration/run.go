package integration

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/docker/cli/cli/config"
	"github.com/gofrs/flock"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/contentutil"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

var sandboxLimiter *semaphore.Weighted

func init() {
	sandboxLimiter = semaphore.NewWeighted(int64(runtime.GOMAXPROCS(0)))
}

// Backend is the minimal interface that describes a testing backend.
type Backend interface {
	Address() string
	DockerAddress() string
	ContainerdAddress() string
	DebugAddress() string

	Rootless() bool
	NetNSDetached() bool
	Snapshotter() string
	ExtraEnv() []string
	Supports(feature string) bool
}

type Sandbox interface {
	Backend

	Context() context.Context
	Cmd(...string) *exec.Cmd
	Logs() map[string]*bytes.Buffer
	PrintLogs(*testing.T)
	ClearLogs()
	NewRegistry() (string, error)
	Value(string) any // chosen matrix value
	Name() string
	CDISpecDir() string
}

// BackendConfig is used to configure backends created by a worker.
type BackendConfig struct {
	Logs         map[string]*bytes.Buffer
	DaemonConfig []ConfigUpdater
	CDISpecDir   string
}

type Worker interface {
	New(context.Context, *BackendConfig) (Backend, func() error, error)
	Close() error
	Name() string
	Rootless() bool
	NetNSDetached() bool
}

type ConfigUpdater interface {
	UpdateConfigFile(string) string
}

type Test interface {
	Name() string
	Run(t *testing.T, sb Sandbox)
}

type testFunc struct {
	name string
	run  func(t *testing.T, sb Sandbox)
}

func (f testFunc) Name() string {
	return f.name
}

func (f testFunc) Run(t *testing.T, sb Sandbox) {
	t.Helper()
	f.run(t, sb)
}

func TestFuncs(funcs ...func(t *testing.T, sb Sandbox)) []Test {
	var tests []Test
	names := map[string]struct{}{}
	for _, f := range funcs {
		name := getFunctionName(f)
		if _, ok := names[name]; ok {
			panic("duplicate test: " + name)
		}
		names[name] = struct{}{}
		tests = append(tests, testFunc{name: name, run: f})
	}
	return tests
}

var defaultWorkers []Worker

func Register(w Worker) {
	defaultWorkers = append(defaultWorkers, w)
}

func List() []Worker {
	return defaultWorkers
}

// TestOpt is an option that can be used to configure a set of integration
// tests.
type TestOpt func(*testConf)

func WithMatrix(key string, m map[string]any) TestOpt {
	return func(tc *testConf) {
		if tc.matrix == nil {
			tc.matrix = map[string]map[string]any{}
		}
		tc.matrix[key] = m
	}
}

func WithMirroredImages(m map[string]string) TestOpt {
	return func(tc *testConf) {
		if tc.mirroredImages == nil {
			tc.mirroredImages = map[string]string{}
		}
		maps.Copy(tc.mirroredImages, m)
	}
}

type testConf struct {
	matrix         map[string]map[string]any
	mirroredImages map[string]string
}

func Run(t *testing.T, testCases []Test, opt ...TestOpt) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	if os.Getenv("SKIP_INTEGRATION_TESTS") == "1" {
		t.Skip("skipping integration tests")
	}

	var sliceSplit int
	if filter, ok := lookupTestFilter(); ok {
		parts := strings.Split(filter, "/")
		if len(parts) >= 2 {
			const prefix = "slice="
			if after, ok0 := strings.CutPrefix(parts[1], prefix); ok0 {
				conf := after
				offsetS, totalS, ok := strings.Cut(conf, "-")
				if !ok {
					t.Fatalf("invalid slice=%q", conf)
				}
				offset, err := strconv.Atoi(offsetS)
				require.NoError(t, err)
				total, err := strconv.Atoi(totalS)
				require.NoError(t, err)
				if offset < 1 || total < 1 || offset > total {
					t.Fatalf("invalid slice=%q", conf)
				}
				sliceSplit = total
			}
		}
	}

	var tc testConf
	for _, o := range opt {
		o(&tc)
	}

	getMirror := lazyMirrorRunnerFunc(t, tc.mirroredImages)

	matrix := prepareValueMatrix(tc)

	list := List()
	if os.Getenv("BUILDKIT_WORKER_RANDOM") == "1" && len(list) > 0 {
		rng := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // using math/rand is fine in a test utility
		list = []Worker{list[rng.Intn(len(list))]}
	}
	t.Cleanup(func() {
		for _, br := range list {
			_ = br.Close()
		}
	})

	for _, br := range list {
		for i, tc := range testCases {
			for _, mv := range matrix {
				fn := tc.Name()
				if sliceSplit > 0 {
					pageLimit := int(math.Ceil(float64(len(testCases)) / float64(sliceSplit)))
					sliceName := fmt.Sprintf("slice=%d-%d/", i/pageLimit+1, sliceSplit)
					fn = sliceName + fn
				}
				name := fn + "/worker=" + br.Name() + mv.functionSuffix()
				func(fn, testName string, br Worker, tc Test, mv matrixValue) {
					ok := t.Run(testName, func(t *testing.T) {
						if strings.Contains(fn, "NoRootless") && br.Rootless() {
							// skip sandbox setup
							t.Skip("rootless")
						}
						ctx := appcontext.Context()
						// TODO(profnandaa): to revisit this to allow tests run
						// in parallel on Windows in a stable way. Is flaky currently.
						if !strings.HasSuffix(fn, "NoParallel") && runtime.GOOS != "windows" {
							t.Parallel()
						}
						require.NoError(t, sandboxLimiter.Acquire(context.TODO(), 1))
						defer sandboxLimiter.Release(1)

						ctx, cancel := context.WithCancelCause(ctx)
						defer func() { cancel(errors.WithStack(context.Canceled)) }()

						sb, closer, err := newSandbox(ctx, t, br, getMirror(), mv)
						require.NoError(t, err)
						t.Cleanup(func() {
							if closer != nil {
								_ = closer()
							}
						})
						defer func() {
							if t.Failed() {
								closer()
								closer = nil // don't call again
								sb.PrintLogs(t)
							}
						}()
						tc.Run(t, sb)
					})
					require.True(t, ok)
				}(fn, name, br, tc, mv)
			}
		}
	}
}

func getFunctionName(i any) string {
	fullname := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	dot := strings.LastIndex(fullname, ".") + 1
	return strings.Title(fullname[dot:]) //nolint:staticcheck // ignoring "SA1019: strings.Title is deprecated", as for our use we don't need full unicode support
}

var localImageCache map[string]map[string]struct{}
var localImageCacheMu sync.Mutex

func copyImagesLocal(t *testing.T, host string, images map[string]string) error {
	localImageCacheMu.Lock()
	defer localImageCacheMu.Unlock()
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

		// already exists check
		if _, _, err := docker.NewResolver(docker.ResolverOptions{}).Resolve(context.TODO(), host+"/"+to); err == nil {
			continue
		}

		var desc ocispecs.Descriptor
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
			dockerConfig := config.LoadDefaultConfigFile(os.Stderr)

			desc, provider, err = contentutil.ProviderFromRef(from, contentutil.WithCredentials(
				func(host string) (string, string, error) {
					ac, err := dockerConfig.GetAuthConfig(host)
					if err != nil {
						return "", "", err
					}
					return ac.Username, ac.Password, nil
				}))
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
	return officialImages(names...)
}

func withMirrorConfig(mirror string) ConfigUpdater {
	return mirrorConfig(mirror)
}

type mirrorConfig string

func (mc mirrorConfig) UpdateConfigFile(in string) string {
	return fmt.Sprintf(`%s

[registry."docker.io"]
mirrors=["%s"]
`, in, mc)
}

func WriteConfig(updaters []ConfigUpdater) (string, error) {
	tmpdir, err := os.MkdirTemp("", "bktest_config")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(tmpdir, 0711); err != nil {
		return "", err
	}

	s := ""
	for _, upt := range updaters {
		s = upt.UpdateConfigFile(s)
	}

	if err := os.WriteFile(filepath.Join(tmpdir, buildkitdConfigFile), []byte(s), 0644); err != nil {
		return "", err
	}
	return filepath.Join(tmpdir, buildkitdConfigFile), nil
}

func lazyMirrorRunnerFunc(t *testing.T, images map[string]string) func() string {
	var once sync.Once
	var mirror string
	return func() string {
		once.Do(func() {
			m, err := RunMirror()
			require.NoError(t, err)
			require.NoError(t, m.AddImages(t, images))
			t.Cleanup(func() { _ = m.Close() })
			mirror = m.Host
		})
		return mirror
	}
}

type Mirror struct {
	Host    string
	dir     string
	cleanup func() error
}

func (m *Mirror) lock() (*flock.Flock, error) {
	if m.dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(m.dir, "lock"))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return lock, nil
}

func (m *Mirror) Close() error {
	if m.cleanup != nil {
		return m.cleanup()
	}
	return nil
}

func (m *Mirror) AddImages(t *testing.T, images map[string]string) (err error) {
	lock, err := m.lock()
	if err != nil {
		return err
	}
	defer func() {
		if lock != nil {
			lock.Unlock()
		}
	}()

	if err := copyImagesLocal(t, m.Host, images); err != nil {
		return err
	}
	return nil
}

func RunMirror() (_ *Mirror, err error) {
	mirrorDir := os.Getenv("BUILDKIT_REGISTRY_MIRROR_DIR")

	m := &Mirror{
		dir: mirrorDir,
	}

	lock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			lock.Unlock()
		}
	}()

	host, cleanup, err := NewRegistry(mirrorDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	m.Host = host
	m.cleanup = cleanup

	if lock != nil {
		if err := lock.Unlock(); err != nil {
			return nil, err
		}
	}

	return m, err
}

type matrixValue struct {
	fn     []string
	values map[string]matrixValueChoice
}

func (mv matrixValue) functionSuffix() string {
	if len(mv.fn) == 0 {
		return ""
	}
	slices.Sort(mv.fn)
	sb := &strings.Builder{}
	for _, f := range mv.fn {
		sb.Write([]byte("/" + f + "=" + mv.values[f].name))
	}
	return sb.String()
}

type matrixValueChoice struct {
	name  string
	value any
}

func newMatrixValue(key, name string, v any) matrixValue {
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

func prepareValueMatrix(tc testConf) []matrixValue {
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
				maps.Copy(vv.values, c.values)
				m = append(m, vv)
			}
		}
	}
	if len(m) == 0 {
		m = append(m, matrixValue{})
	}
	return m
}

// Skips tests on platform
func SkipOnPlatform(t *testing.T, goos string) {
	skip := false
	// support for negation
	if after, ok := strings.CutPrefix(goos, "!"); ok {
		goos = after
		skip = runtime.GOOS != goos
	} else {
		skip = runtime.GOOS == goos
	}

	if skip {
		t.Skipf("Skipped on %s", goos)
	}
}

// Selects between two types, returns second
// argument if on Windows or else first argument.
// Typically used for selecting test cases.
func UnixOrWindows[T any](unix, windows T) T {
	if runtime.GOOS == "windows" {
		return windows
	}
	return unix
}

func lookupTestFilter() (string, bool) {
	const prefix = "-test.run="
	for _, arg := range os.Args {
		if after, ok := strings.CutPrefix(arg, prefix); ok {
			return after, true
		}
	}
	return "", false
}
