package progress

import (
	"context"
	"sync"
)

type MultiReader struct {
	mu          sync.Mutex
	main        ProgressReader
	initialized bool
	done        chan struct{}
	writers     map[*progressWriter]struct{}
}

func NewMultiReader(pr ProgressReader) *MultiReader {
	mr := &MultiReader{
		main: pr,
		done: make(chan struct{}),
	}
	return mr
}

func (mr *MultiReader) Reader(ctx context.Context) ProgressReader {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	pr, ctx, _ := NewContext(ctx)
	pw, _, _ := FromContext(ctx, "")

	w := pw.(*progressWriter)
	mr.writers[w] = struct{}{}

	go func() {
		select {
		case <-ctx.Done():
		case <-mr.done:
		}
		mr.mu.Lock()
		defer mr.mu.Unlock()
		delete(mr.writers, w)
	}()

	if !mr.initialized {
		go mr.handle()
		mr.initialized = true
	}

	return pr
}

func (mr *MultiReader) handle() error {
	for {
		p, err := mr.main.Read(context.TODO())
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		mr.mu.Lock()
		for w := range mr.writers {
			w.write(*p)
		}
		mr.mu.Unlock()
	}
}
