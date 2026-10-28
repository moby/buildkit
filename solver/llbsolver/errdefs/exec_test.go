package errdefs

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestExecErrorBorrowOnce checks that concurrent callers sharing the same
// ExecError can mark it borrowed safely and that only one call reports it.
func TestExecErrorBorrowOnce(t *testing.T) {
	t.Parallel()

	ee := &ExecError{error: errors.New("exec failed")}
	var won atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Go(func() {
			<-start
			if ee.Borrow() {
				won.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(1), won.Load())
	require.False(t, ee.Borrow())
	require.NoError(t, ee.Release())
}
