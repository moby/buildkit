package solver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPipe(t *testing.T) {
	runCh := make(chan struct{})
	f := func(ctx context.Context) (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-runCh:
			return "res0", nil
		}
	}

	waitSignal := make(chan struct{}, 10)
	signalled := 0
	signal := func() {
		signalled++
		waitSignal <- struct{}{}
	}

	p, start := newFuncionPipe(f)
	p.SignalWriter = signal
	go start()
	require.Equal(t, false, p.Reader.Reload())

	st := p.Reader.Status()
	require.Equal(t, st.Completed, false)
	require.Equal(t, st.Canceled, false)
	require.Nil(t, st.Value)
	require.Equal(t, signalled, 0)

	close(runCh)
	<-waitSignal

	p.Reader.Reload()
	st = p.Reader.Status()
	require.Equal(t, st.Completed, true)
	require.Equal(t, st.Canceled, false)
	require.NoError(t, st.Err)
	require.Equal(t, st.Value.(string), "res0")
}

func TestPipeCancel(t *testing.T) {
	runCh := make(chan struct{})
	f := func(ctx context.Context) (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-runCh:
			return "res0", nil
		}
	}

	waitSignal := make(chan struct{}, 10)
	signalled := 0
	signal := func() {
		signalled++
		waitSignal <- struct{}{}
	}

	p, start := newFuncionPipe(f)
	p.SignalWriter = signal
	go start()
	p.Reader.Reload()

	st := p.Reader.Status()
	require.Equal(t, st.Completed, false)
	require.Equal(t, st.Canceled, false)
	require.Nil(t, st.Value)
	require.Equal(t, signalled, 0)

	p.Reader.Cancel()
	<-waitSignal

	p.Reader.Reload()
	st = p.Reader.Status()
	require.Equal(t, st.Completed, true)
	require.Equal(t, st.Canceled, true)
	require.Error(t, st.Err)
	require.Equal(t, st.Err, context.Canceled)
}
