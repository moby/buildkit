package netpool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPoolReusesReturnedValue(t *testing.T) {
	var next int
	p := New(Opt[int]{
		Name:       "test",
		TargetSize: 1,
		New: func(context.Context) (int, error) {
			next++
			return next, nil
		},
		Release: func(int) error {
			return nil
		},
	})

	v1, err := p.Get(t.Context())
	require.NoError(t, err)
	p.Put(v1)

	v2, err := p.Get(t.Context())
	require.NoError(t, err)
	require.Equal(t, v1, v2)
	require.Equal(t, 1, next)
	require.NoError(t, p.Discard(v2))
}

func TestPoolCloseReleasesAvailableAndReturnedValues(t *testing.T) {
	var next int
	var released []int
	p := New(Opt[int]{
		Name:       "test",
		TargetSize: 1,
		New: func(context.Context) (int, error) {
			next++
			return next, nil
		},
		Release: func(v int) error {
			released = append(released, v)
			return nil
		},
	})

	v, err := p.Get(t.Context())
	require.NoError(t, err)
	require.NoError(t, p.Close())
	p.Put(v)
	require.Equal(t, []int{v}, released)

	_, err = p.Get(t.Context())
	require.Error(t, err)
	require.Equal(t, 1, next)
}
