package progress

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"
)

func TestProgress(t *testing.T) {
	s, err := calc(context.TODO(), 4)
	assert.NoError(t, err)
	assert.Equal(t, 10, s)

	eg, ctx := errgroup.WithContext(context.Background())

	pr, ctx, cancelProgress := NewContext(ctx)
	var trace trace
	eg.Go(func() error {
		return saveProgress(ctx, pr, &trace)
	})
	s, err = calc(ctx, 5)
	assert.NoError(t, err)
	assert.Equal(t, 15, s)

	cancelProgress()
	err = eg.Wait()
	assert.NoError(t, err)

	assert.Equal(t, 6, len(trace.items))
	assert.Equal(t, trace.items[len(trace.items)-1].Done, true)
}

func calc(ctx context.Context, total int) (int, error) {
	pw, _, ctx := FromContext(ctx, "calc")
	defer pw.Done()

	sum := 0
	pw.Write(Progress{Action: "starting", Total: total})
	for i := 1; i <= total; i++ {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		pw.Write(Progress{Action: "calculating", Total: total, Current: i})
		sum += i
	}
	pw.Write(Progress{Action: "done", Total: total, Current: total, Done: true})

	return sum, nil
}

type trace struct {
	items []Progress
}

func saveProgress(ctx context.Context, pr ProgressReader, t *trace) error {
	for {
		p, err := pr.Read(ctx)
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		t.items = append(t.items, *p)
	}
	return nil
}
