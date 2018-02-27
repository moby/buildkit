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
		testGetSet,
		testResults,
		testLinks,
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

func testGetSet(t *testing.T, st CacheKeyStorage) {
	t.Parallel()
	cki := CacheKeyInfo{
		ID:   "foo",
		Base: digest.FromBytes([]byte("foo")),
	}
	err := st.Set(cki)
	require.NoError(t, err)

	cki2, err := st.Get(cki.ID)
	require.NoError(t, err)
	require.Equal(t, cki, cki2)

	_, err = st.Get("bar")
	require.Error(t, err)
	require.Equal(t, errors.Cause(err), ErrNotFound)
}

func testResults(t *testing.T, st CacheKeyStorage) {
	t.Parallel()
	cki := CacheKeyInfo{
		ID:   "foo",
		Base: digest.FromBytes([]byte("foo")),
	}
	err := st.Set(cki)
	require.NoError(t, err)

	cki2 := CacheKeyInfo{
		ID:   "bar",
		Base: digest.FromBytes([]byte("bar")),
	}
	err = st.Set(cki2)
	require.NoError(t, err)

	err = st.AddResult(cki.ID, CacheResult{
		ID:        "foo0",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddResult(cki.ID, CacheResult{
		ID:        "foo1",
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	err = st.AddResult(cki2.ID, CacheResult{
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
	cki := CacheKeyInfo{
		ID:   "foo",
		Base: digest.FromBytes([]byte("foo")),
	}
	err := st.Set(cki)
	require.NoError(t, err)

	cki2 := CacheKeyInfo{
		ID:   "bar",
		Base: digest.FromBytes([]byte("bar")),
	}
	err = st.Set(cki2)
	require.NoError(t, err)

	l0 := CacheInfoLink{
		Input: 0, Output: 1, Digest: digest.FromBytes([]byte(">target0")),
	}
	err = st.AddLink(cki.ID, l0, "target0")
	require.NoError(t, err)

	err = st.AddLink(cki2.ID, l0, "target0-bar")
	require.NoError(t, err)

	m := map[string]struct{}{}
	err = st.WalkLinks(cki.ID, l0, func(id string) error {
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
	err = st.WalkLinks(cki.ID, l1, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 0)

	err = st.AddLink(cki.ID, l1, "target1")
	require.NoError(t, err)

	m = map[string]struct{}{}
	err = st.WalkLinks(cki.ID, l1, func(id string) error {
		m[id] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, len(m), 1)

	_, ok = m["target1"]
	require.True(t, ok)

	err = st.AddLink(cki.ID, l1, "target1-second")
	require.NoError(t, err)

	m = map[string]struct{}{}
	err = st.WalkLinks(cki.ID, l1, func(id string) error {
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

func getFunctionName(i interface{}) string {
	fullname := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	dot := strings.LastIndex(fullname, ".") + 1
	return strings.Title(fullname[dot:])
}
