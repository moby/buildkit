package solver

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func RunCacheStorageTests(t *testing.T, st func() (CacheKeyStorage, func())) {
	for _, tc := range []func(*testing.T, CacheKeyStorage){
		testResults,
		testLinks,
		testResultReleaseSingleLevel,
		testResultReleaseMultiLevel,
	} {
		runStorageTest(t, tc, st)
	}
}

func runStorageTest(t *testing.T, fn func(t *testing.T, st CacheKeyStorage), st func() (CacheKeyStorage, func())) {
	require.True(t, t.Run(getFunctionName(fn), func(t *testing.T) {
		s, cleanup := st()
		defer cleanup()
		fn(t, s)
	}))
}

func testResults(t *testing.T, st CacheKeyStorage) {
	t.Parallel()
	err := st.AddResult("foo", CacheResult{
		ID:        "foo0",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddResult("foo", CacheResult{
		ID:        "foo1",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddResult("bar", CacheResult{
		ID:        "bar0",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	m := map[string]CacheResult{}
	err = st.WalkResults("foo", func(r CacheResult) error {
		m[r.ID] = r
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, len(m), 2)
	f0, ok := m["foo0"]
	require.True(t, ok)
	f1, ok := m["foo1"]
	require.True(t, ok)
	require.True(t, f0.CreatedAt.Before(f1.CreatedAt))

	m = map[string]CacheResult{}
	err = st.WalkResults("bar", func(r CacheResult) error {
		m[r.ID] = r
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, len(m), 1)
	_, ok = m["bar0"]
	require.True(t, ok)

	// empty result
	err = st.WalkResults("baz", func(r CacheResult) error {
		require.Fail(t, "unreachable")
		return nil
	})
	require.NoError(t, err)

	res, err := st.Load("foo", "foo1")
	require.NoError(t, err)

	require.Equal(t, res.ID, "foo1")

	_, err = st.Load("foo1", "foo1")
	require.Error(t, err)
	require.Equal(t, errors.Cause(err), ErrNotFound)

	_, err = st.Load("foo", "foo2")
	require.Error(t, err)
	require.Equal(t, errors.Cause(err), ErrNotFound)
}

func testLinks(t *testing.T, st CacheKeyStorage) {
	t.Parallel()

	l0 := CacheInfoLink{
		Input: 0, Output: 1, Digest: digest.FromBytes([]byte(">target0")),
	}
	err := st.AddLink("foo", l0, "target0")
	require.NoError(t, err)

	err = st.AddLink("bar", l0, "target0-second")
	require.NoError(t, err)

	m := map[string]struct{}{}
	err = st.WalkLinks("foo", l0, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, len(m), 1)
	_, ok := m["target0"]
	require.True(t, ok)

	l1 := CacheInfoLink{
		Input: 0, Output: 1, Digest: digest.FromBytes([]byte(">target1")),
	}
	m = map[string]struct{}{}
	err = st.WalkLinks("foo", l1, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 0)

	err = st.AddLink("foo", l1, "target1")
	require.NoError(t, err)

	m = map[string]struct{}{}
	err = st.WalkLinks("foo", l1, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 1)

	_, ok = m["target1"]
	require.True(t, ok)

	err = st.AddLink("foo", l1, "target1-second")
	require.NoError(t, err)

	m = map[string]struct{}{}
	err = st.WalkLinks("foo", l1, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 2)
	_, ok = m["target1"]
	require.True(t, ok)
	_, ok = m["target1-second"]
	require.True(t, ok)
}

func testResultReleaseSingleLevel(t *testing.T, st CacheKeyStorage) {
	t.Parallel()

	err := st.AddResult("foo", CacheResult{
		ID:        "foo0",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddResult("foo", CacheResult{
		ID:        "foo1",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.Release("foo0")
	require.NoError(t, err)

	m := map[string]struct{}{}
	st.WalkResults("foo", func(res CacheResult) error {
		m[res.ID] = struct{}{}
		return nil
	})

	require.Equal(t, len(m), 1)
	_, ok := m["foo1"]
	require.True(t, ok)

	err = st.Release("foo1")
	require.NoError(t, err)

	m = map[string]struct{}{}
	st.WalkResults("foo", func(res CacheResult) error {
		m[res.ID] = struct{}{}
		return nil
	})

	require.Equal(t, len(m), 0)
}

func testResultReleaseMultiLevel(t *testing.T, st CacheKeyStorage) {
	t.Parallel()

	err := st.AddResult("foo", CacheResult{
		ID:        "foo-result",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddResult("sub0", CacheResult{
		ID:        "sub0-result",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	l0 := CacheInfoLink{
		Input: 0, Output: 1, Digest: digest.FromBytes([]byte("to-sub0")),
	}
	err = st.AddLink("foo", l0, "sub0")
	require.NoError(t, err)

	err = st.AddResult("sub1", CacheResult{
		ID:        "sub1-result",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddLink("foo", l0, "sub1")
	require.NoError(t, err)

	// delete one sub doesn't delete parent

	err = st.Release("sub0-result")
	require.NoError(t, err)

	m := map[string]struct{}{}
	err = st.WalkResults("foo", func(res CacheResult) error {
		m[res.ID] = struct{}{}
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, len(m), 1)
	_, ok := m["foo-result"]
	require.True(t, ok)

	require.False(t, st.Exists("sub0"))

	m = map[string]struct{}{}
	err = st.WalkLinks("foo", l0, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 1)

	_, ok = m["sub1"]
	require.True(t, ok)

	// release foo removes the result but doesn't break the chain

	err = st.Release("foo-result")
	require.NoError(t, err)

	require.True(t, st.Exists("foo"))

	m = map[string]struct{}{}
	err = st.WalkResults("foo", func(res CacheResult) error {
		m[res.ID] = struct{}{}
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, len(m), 0)

	m = map[string]struct{}{}
	err = st.WalkLinks("foo", l0, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 1)

	// release sub1 now releases foo as well

	err = st.Release("sub1-result")
	require.NoError(t, err)

	require.False(t, st.Exists("sub1"))
	require.False(t, st.Exists("foo"))
}

func getFunctionName(i interface{}) string {
	fullname := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	dot := strings.LastIndex(fullname, ".") + 1
	return strings.Title(fullname[dot:])
}
