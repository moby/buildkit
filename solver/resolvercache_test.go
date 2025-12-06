package solver

import (
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolverCache_SerialAccess(t *testing.T) {
	rc := newResolverCache()

	// First lock should succeed immediately
	values, release, err := rc.Lock("key1")
	require.NoError(t, err)
	assert.Empty(t, values)

	// Add a value and release
	err = release("val1")
	require.NoError(t, err)

	// Next lock should see accumulated value
	values, release, err = rc.Lock("key1")
	require.NoError(t, err)
	assert.Equal(t, []any{"val1"}, values)

	// Add another value
	_ = release("val2")

	// Lock again should return both values
	values, release, err = rc.Lock("key1")
	require.NoError(t, err)
	assert.Equal(t, []any{"val1", "val2"}, values)
	_ = release(nil)
}

func TestResolverCache_ConcurrentWaiters(t *testing.T) {
	rc := newResolverCache()

	var wg sync.WaitGroup
	results := make(chan []any, 2)

	// First goroutine acquires lock
	values, release, err := rc.Lock("shared")
	require.NoError(t, err)
	assert.Empty(t, values)

	// Two goroutines that will wait
	for range 2 {
		wg.Go(func() {
			v, _, err := rc.Lock("shared")
			results <- v
			assert.NoError(t, err)
		})
	}

	select {
	case <-results:
		t.Fatal("expected goroutines to be waiting, but got result")
	case <-time.After(100 * time.Millisecond):
	}

	// Release with a value
	err = release("done")
	require.NoError(t, err)

	// Wait for both goroutines
	wg.Wait()
	close(results)

	for v := range results {
		assert.Equal(t, []any{"done"}, v)
	}
}

func TestResolverCache_MultipleIndependentKeys(t *testing.T) {
	rc := newResolverCache()

	v1, r1, err := rc.Lock("a")
	require.NoError(t, err)
	v2, r2, err := rc.Lock("b")
	require.NoError(t, err)

	assert.Empty(t, v1)
	assert.Empty(t, v2)

	require.NoError(t, r1("x"))
	require.NoError(t, r2("y"))

	v1, _, err = rc.Lock("a")
	require.NoError(t, err)
	assert.Equal(t, []any{"x"}, v1)

	v2, _, err = rc.Lock("b")
	require.NoError(t, err)
	assert.Equal(t, []any{"y"}, v2)
}

func TestResolverCache_ReleaseNilDoesNotAdd(t *testing.T) {
	rc := newResolverCache()

	v, r, err := rc.Lock("niltest")
	require.NoError(t, err)
	assert.Empty(t, v)

	require.NoError(t, r(nil))

	v, _, err = rc.Lock("niltest")
	require.NoError(t, err)
	assert.Empty(t, v)
}

func TestResolverCache_SequentialLocks(t *testing.T) {
	rc := newResolverCache()

	for i := range 3 {
		v, r, err := rc.Lock("seq")
		require.NoError(t, err)
		assert.Len(t, v, i)
		require.NoError(t, r(i))
	}

	v, _, err := rc.Lock("seq")
	require.NoError(t, err)
	assert.ElementsMatch(t, []any{0, 1, 2}, v)
}

// mockResolverCache implements ResolverCache for testing.
type mockResolverCache struct {
	lockFn    func(key any) ([]any, func(any) error, error)
	lockCalls int
	mu        sync.Mutex
}

func (m *mockResolverCache) Lock(key any) ([]any, func(any) error, error) {
	m.mu.Lock()
	m.lockCalls++
	m.mu.Unlock()
	if m.lockFn != nil {
		return m.lockFn(key)
	}
	return nil, func(any) error { return nil }, nil
}

func TestCombinedResolverCache_BasicMerge(t *testing.T) {
	rc1 := &mockResolverCache{
		lockFn: func(key any) ([]any, func(any) error, error) {
			return []any{"a1", "a2"}, func(v any) error {
				if v != nil {
					assert.Equal(t, "merged", v)
				}
				return nil
			}, nil
		},
	}
	rc2 := &mockResolverCache{
		lockFn: func(key any) ([]any, func(any) error, error) {
			return []any{"b1"}, func(v any) error {
				if v != nil {
					assert.Equal(t, "merged", v)
				}
				return nil
			}, nil
		},
	}

	combined := combinedResolverCache([]ResolverCache{rc1, rc2})
	values, release, err := combined.Lock("key")

	require.NoError(t, err)
	assert.ElementsMatch(t, []any{"a1", "a2", "b1"}, values)

	err = release("merged")
	require.NoError(t, err)

	assert.Equal(t, 1, rc1.lockCalls)
	assert.Equal(t, 1, rc2.lockCalls)
}

func TestCombinedResolverCache_EmptyInput(t *testing.T) {
	combined := combinedResolverCache(nil)
	values, release, err := combined.Lock("any")
	require.NoError(t, err)
	assert.Nil(t, values)
	require.NoError(t, release("whatever"))
}

func TestCombinedResolverCache_ErrorHandlingAndRollback(t *testing.T) {
	var released []string
	var mu sync.Mutex

	rc1 := &mockResolverCache{
		lockFn: func(key any) ([]any, func(any) error, error) {
			return []any{"x"}, func(v any) error {
				mu.Lock()
				released = append(released, "rc1")
				mu.Unlock()
				return nil
			}, nil
		},
	}

	rc2 := &mockResolverCache{
		lockFn: func(key any) ([]any, func(any) error, error) {
			return nil, nil, errors.New("rc2 failed")
		},
	}

	combined := combinedResolverCache([]ResolverCache{rc1, rc2})
	values, release, err := combined.Lock("key")

	assert.Nil(t, values)
	assert.Nil(t, release)
	require.EqualError(t, err, "rc2 failed")

	mu.Lock()
	assert.Contains(t, released, "rc1", "should rollback acquired locks")
	mu.Unlock()
}

func TestCombinedResolverCache_ParallelReleaseErrorPropagation(t *testing.T) {
	var count int
	rc1 := &mockResolverCache{
		lockFn: func(key any) ([]any, func(any) error, error) {
			return []any{"v1"}, func(v any) error {
				count++
				return errors.New("rc1 release failed")
			}, nil
		},
	}
	rc2 := &mockResolverCache{
		lockFn: func(key any) ([]any, func(any) error, error) {
			return []any{"v2"}, func(v any) error {
				count++
				return nil
			}, nil
		},
	}

	combined := combinedResolverCache([]ResolverCache{rc1, rc2})
	values, release, err := combined.Lock("k")
	require.NoError(t, err)
	assert.ElementsMatch(t, []any{"v1", "v2"}, values)

	e := release("data")
	require.EqualError(t, e, "rc1 release failed")
	assert.Equal(t, 2, count, "both releases must be called")
}
