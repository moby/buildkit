package progress

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"
)

func TestProgress(t *testing.T) {
	s, err := calc(context.TODO(), 4, "calc")
	assert.NoError(t, err)
	assert.Equal(t, 10, s)

	eg, ctx := errgroup.WithContext(context.Background())

	pr, ctx, cancelProgress := NewContext(ctx)
	var trace trace
	eg.Go(func() error {
		return saveProgress(ctx, pr, &trace)
	})
	s, err = calc(ctx, 5, "calc")
	assert.NoError(t, err)
	assert.Equal(t, 15, s)

	cancelProgress()
	err = eg.Wait()
	assert.NoError(t, err)

	assert.Equal(t, 6, len(trace.items))
	assert.Equal(t, trace.items[len(trace.items)-1].Done, true)
}

func TestProgressNested(t *testing.T) {
	eg, ctx := errgroup.WithContext(context.Background())
	pr, ctx, cancelProgress := NewContext(ctx)
	var trace trace
	eg.Go(func() error {
		return saveProgress(ctx, pr, &trace)
	})
	s, err := reduceCalc(ctx, 3)
	assert.NoError(t, err)
	assert.Equal(t, 6, s)

	cancelProgress()

	err = eg.Wait()
	assert.NoError(t, err)

	assert.True(t, len(trace.items) > 9) // usually 14
	assert.True(t, len(trace.items) <= 14)
	streams := 0
	for _, t := range trace.items {
		if t.Done {
			streams++
		}
	}
	assert.Equal(t, streams, 4)
}

func calc(ctx context.Context, total int, name string) (int, error) {
	pw, _, ctx := FromContext(ctx, name)
	defer pw.Done()

	sum := 0
	pw.Write(Progress{Action: "starting", Total: total})
	for i := 1; i <= total; i++ {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		if i == total {
			pw.Write(Progress{Action: "done", Total: total, Current: total, Done: true})
		} else {
			pw.Write(Progress{Action: "calculating", Total: total, Current: i})
		}
		sum += i
	}

	return sum, nil
}

func reduceCalc(ctx context.Context, total int) (int, error) {
	eg, ctx := errgroup.WithContext(ctx)

	pw, _, ctx := FromContext(ctx, "reduce")
	defer pw.Done()

	pw.Write(Progress{Action: "starting"})

	// sync step
	sum, err := calc(ctx, total, "synccalc")
	if err != nil {
		return 0, err
	}
	// parallel steps
	for i := 0; i < 2; i++ {
		func(i int) {
			eg.Go(func() error {
				_, err := calc(ctx, total, fmt.Sprintf("calc-%d", i))
				return err
			})
		}(i)
	}
	if err := eg.Wait(); err != nil {
		return 0, err
	}
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
