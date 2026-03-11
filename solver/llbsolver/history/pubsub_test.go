package history

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPubsubSendReceive(t *testing.T) {
	ps := &pubsub[int]{m: map[*channel[int]]struct{}{}}

	sub1 := ps.Subscribe()
	sub2 := ps.Subscribe()

	ps.Send(42)

	v1 := <-sub1.ch
	v2 := <-sub2.ch

	require.Equal(t, 42, v1)
	require.Equal(t, 42, v2)

	sub1.close()

	ps.Send(99)

	v2 = <-sub2.ch
	require.Equal(t, 99, v2)

	// sub1 should not receive after close
	select {
	case <-sub1.ch:
		t.Fatal("received on closed subscriber")
	default:
	}

	sub2.close()
}

func TestPubsubClose(t *testing.T) {
	ps := &pubsub[string]{m: map[*channel[string]]struct{}{}}

	sub := ps.Subscribe()
	ps.Close()

	// done channel should be closed after ps.Close
	select {
	case <-sub.done:
	default:
		t.Fatal("subscriber done channel not closed after pubsub Close")
	}
}

func TestPubsubCloseIdempotent(t *testing.T) {
	ps := &pubsub[int]{m: map[*channel[int]]struct{}{}}

	sub := ps.Subscribe()
	sub.close()
	sub.close() // should not panic
}

func TestPubsubConcurrent(t *testing.T) {
	ps := &pubsub[int]{m: map[*channel[int]]struct{}{}}

	const numSubs = 10
	const numMsgs = 100

	subs := make([]*channel[int], numSubs)
	for i := range subs {
		subs[i] = ps.Subscribe()
	}

	var wg sync.WaitGroup

	// concurrent sends
	wg.Go(func() {
		for i := range numMsgs {
			ps.Send(i)
		}
	})

	// concurrent receives
	received := make([][]int, numSubs)
	for i, sub := range subs {
		wg.Go(func() {
			for range numMsgs {
				v := <-sub.ch
				received[i] = append(received[i], v)
			}
		})
	}

	wg.Wait()

	for i := range numSubs {
		require.Len(t, received[i], numMsgs)
	}

	for _, sub := range subs {
		sub.close()
	}
}
