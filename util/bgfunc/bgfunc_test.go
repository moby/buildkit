package bgfunc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestBgFuncSimple(t *testing.T) {
	t.Parallel()
	var res string
	var mu sync.Mutex
	calls1 := 0
	f, err := New(context.TODO(), func(ctx context.Context, signal func()) error {
		calls1++
		mu.Lock()
		res = "ok"
		signal()
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)

	fn := func() (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if res != "" {
			return res, nil
		}
		return nil, nil
	}
	c1 := f.NewCaller()
	v, err := c1.Call(context.TODO(), fn)
	require.NoError(t, err)

	c2 := f.NewCaller()
	v2, err := c2.Call(context.TODO(), fn)

	require.NoError(t, err)
	require.Equal(t, v.(string), "ok")
	require.Equal(t, v2.(string), "ok")

	require.Equal(t, calls1, 1)

}

func TestSignal(t *testing.T) {
	t.Parallel()
	var res []string
	var mu sync.Mutex
	next := make(chan struct{})
	f, err := New(context.TODO(), func(ctx context.Context, signal func()) error {
		mu.Lock()
		res = append(res, "ok1")
		signal()
		mu.Unlock()
		<-next
		mu.Lock()
		res = append(res, "ok2")
		signal()
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)

	i := 0
	fn := func() (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if i < len(res) {
			v := res[i]
			i++
			return v, nil
		}
		return nil, nil
	}
	c1 := f.NewCaller()
	v, err := c1.Call(context.TODO(), fn)

	require.NoError(t, err)
	require.Equal(t, v.(string), "ok1")

	resCh := make(chan interface{})
	go func() {
		v, err = c1.Call(context.TODO(), fn)
		require.NoError(t, err)
		resCh <- v
	}()

	select {
	case <-resCh:
		require.Fail(t, "unexpected result")
	case <-time.After(50 * time.Millisecond):
		close(next)
	}

	select {
	case v := <-resCh:
		require.Equal(t, v.(string), "ok2")
	case <-time.After(100 * time.Millisecond):
		require.Fail(t, "timeout")
	}

	v, err = c1.Call(context.TODO(), fn)
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	var res []string
	var mu sync.Mutex
	next := make(chan struct{})
	returned := 0
	f, err := New(context.TODO(), func(ctx context.Context, signal func()) error {
		defer func() {
			returned++
		}()
		mu.Lock()
		if len(res) == 0 {
			res = append(res, "ok1")
		}
		signal()
		mu.Unlock()
		select {
		case <-next:
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		res = append(res, "ok2")
		signal()
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)

	i := 0
	fn1 := func() (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if i < len(res) {
			v := res[i]
			i++
			return v, nil
		}
		return nil, nil
	}

	i2 := 0
	fn2 := func() (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if i2 < len(res) {
			v := res[i2]
			i2++
			return v, nil
		}
		return nil, nil
	}

	c1 := f.NewCaller()
	v, err := c1.Call(context.TODO(), fn1)

	require.NoError(t, err)
	require.Equal(t, v.(string), "ok1")

	c2 := f.NewCaller()
	v2, err := c2.Call(context.TODO(), fn2)

	require.NoError(t, err)
	require.Equal(t, v2.(string), "ok1")

	c3 := f.NewCaller()

	firstErr := make(chan error)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := c1.Call(ctx, fn1)
		firstErr <- err
	}()

	secondErr := make(chan error)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := c2.Call(ctx, fn2)
		secondErr <- err
	}()

	select {
	case err := <-firstErr:
		require.Equal(t, err.Error(), context.DeadlineExceeded.Error())
		c3.Cancel()
	case <-secondErr:
		require.Fail(t, "invalid error")
	case <-time.After(100 * time.Millisecond):
		require.Fail(t, "timeout")
	}

	require.Equal(t, returned, 0)

	select {
	case err := <-secondErr:
		require.Equal(t, err.Error(), context.Canceled.Error())
	case <-time.After(100 * time.Millisecond):
		require.Fail(t, "timeout")
	}

	require.Equal(t, 1, returned)

	close(next)

	v, err = c2.Call(context.TODO(), fn2)
	require.NoError(t, err)
	require.Equal(t, v.(string), "ok2")

	v, err = c1.Call(context.TODO(), fn1)
	require.NoError(t, err)
	require.Equal(t, v.(string), "ok2")

	v, err = c2.Call(context.TODO(), fn2)
	require.NoError(t, err)
	require.Equal(t, v, nil)
}

func TestError(t *testing.T) {
	t.Parallel()
	// function returns an error in the middle of processing
	var res string
	var mu sync.Mutex
	next := make(chan struct{})
	returned := 0
	f, err := New(context.TODO(), func(ctx context.Context, signal func()) error {
		defer func() {
			returned++
		}()
		mu.Lock()
		res = "ok1"
		signal()
		mu.Unlock()
		select {
		case <-next:
		case <-ctx.Done():
			return ctx.Err()
		}
		return errors.Errorf("invalid")
	})

	fn1 := func() (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if res != "" {
			defer func() {
				res = ""
			}()
			return res, nil
		}
		return nil, nil
	}

	c1 := f.NewCaller()
	v, err := c1.Call(context.TODO(), fn1)
	require.NoError(t, err)

	require.NoError(t, err)
	require.Equal(t, v.(string), "ok1")

	close(next)

	_, err = c1.Call(context.TODO(), fn1)
	require.Error(t, err)
	require.Equal(t, err.Error(), "invalid")
}
