package progress

import (
	"context"
	"io"
	"sync"
)

type MultiReader struct {
	mu          sync.Mutex
	main        ProgressReader
	initialized bool
	done        chan struct{}
	writers     map[*progressWriter]func()
}

func NewMultiReader(pr ProgressReader) *MultiReader {
	mr := &MultiReader{
		main:    pr,
		writers: make(map[*progressWriter]func()),
		done:    make(chan struct{}),
	}
	return mr
}

func (mr *MultiReader) Reader(ctx context.Context) ProgressReader {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	pr, ctx, closeWriter := NewContext(ctx)
	pw, _, _ := FromContext(ctx, "")

	w := pw.(*progressWriter)
	mr.writers[w] = closeWriter

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
			if err == io.EOF {
				mr.mu.Lock()
				for w := range mr.writers {
					w.Done()
				}
				mr.mu.Unlock()
				return nil
			}
			return err
		}
		mr.mu.Lock()
		for _, p := range p {
			for w, c := range mr.writers {
				if p == nil {
					c()
				} else {
					w.write(*p)
				}
			}
		}
		mr.mu.Unlock()
	}
}
