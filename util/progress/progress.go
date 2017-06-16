package progress

import (
	"context"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/pkg/errors"
)

type contextKeyT string

var contextKey = contextKeyT("buildkit/util/progress")

func FromContext(ctx context.Context, name string) (ProgressWriter, bool, context.Context) {
	pw, ok := ctx.Value(contextKey).(*progressWriter)
	if !ok {
		return &noOpWriter{}, false, ctx
	}
	pw = newWriter(pw, name)
	ctx = context.WithValue(ctx, contextKey, pw)
	return pw, true, ctx
}

func NewContext(ctx context.Context) (ProgressReader, context.Context, func()) {
	pr, pw, cancel := pipe()
	ctx = context.WithValue(ctx, contextKey, pw)
	return pr, ctx, cancel
}

type ProgressWriter interface {
	Write(interface{}) error
	Done() error // Close
}

type ProgressReader interface {
	Read(context.Context) ([]*Progress, error)
}

type Progress struct {
	ID        string
	Timestamp time.Time
	Sys       interface{}
}

type Status struct {
	// ...progress of an action
	Action    string
	Current   int
	Total     int
	Started   *time.Time
	Completed *time.Time
}

type progressReader struct {
	ctx     context.Context
	cond    *sync.Cond
	mu      sync.Mutex
	writers map[*progressWriter]struct{}
	dirty   map[string]*Progress
}

// type progressState struct {
// 	mu      sync.Mutex
//
// }
//
// type streamHandle struct {
// 	pw    *progressWriter
// 	lastP *Progress
// }
//
// func (sh *streamHandle) next() (*Progress, bool) {
// 	lasti := sh.pw.lastP.Load()
// 	if lasti != nil {
// 		last := lasti.(*Progress)
// 		if last != sh.lastP {
// 			sh.lastP = last
// 			return last, true
// 		}
// 	}
// 	return nil, false
// }
//
// func (pr *progressReader) write(id string, p *Progress) {
//
// }

func (pr *progressReader) Read(ctx context.Context) ([]*Progress, error) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-ctx.Done():
			pr.cond.Broadcast()
		}
	}()
	pr.mu.Lock()
	for {
		select {
		case <-ctx.Done():
			pr.mu.Unlock()
			return nil, ctx.Err()
		default:
		}
		dmap := pr.dirty
		open := len(pr.writers) > 0
		if len(dmap) == 0 {
			if !open {
				pr.mu.Unlock()
				return nil, io.EOF
			}
			pr.cond.Wait()
			continue
		}
		pr.dirty = make(map[string]*Progress)
		pr.mu.Unlock()

		out := make([]*Progress, 0, len(dmap))
		for _, p := range dmap {
			out = append(out, p)
		}

		sort.Slice(out, func(i, j int) bool {
			return out[i].Timestamp.Before(out[j].Timestamp)
		})

		return out, nil
	}
}

func (pr *progressReader) append(pw *progressWriter) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	select {
	case <-pr.ctx.Done():
		return
	default:
		pr.writers[pw] = struct{}{}
	}
}

func pipe() (*progressReader, *progressWriter, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	pr := &progressReader{
		ctx:     ctx,
		writers: make(map[*progressWriter]struct{}),
		dirty:   make(map[string]*Progress),
	}
	pr.cond = sync.NewCond(&pr.mu)
	go func() {
		<-ctx.Done()
		pr.cond.Broadcast()
	}()
	pw := &progressWriter{
		reader: pr,
	}
	return pr, pw, cancel
}

func newWriter(pw *progressWriter, name string) *progressWriter {
	if pw.id != "" {
		if name == "" {
			name = pw.id
		} else {
			name = pw.id + "." + name
		}
	}
	pw = &progressWriter{
		id:     name,
		reader: pw.reader,
	}
	pw.reader.append(pw)
	return pw
}

type progressWriter struct {
	id     string
	done   bool
	reader *progressReader
}

func (pw *progressWriter) Write(s interface{}) error {
	if pw.done {
		return errors.Errorf("writing to closed progresswriter %s", pw.id)
	}
	var p Progress
	p.ID = pw.id
	p.Timestamp = time.Now()
	p.Sys = s
	return pw.write(p)
}

func (pw *progressWriter) write(p Progress) error {
	pw.reader.mu.Lock()
	pw.reader.dirty[pw.id+"."+p.ID] = &p
	pw.reader.mu.Unlock()
	pw.reader.cond.Broadcast()
	return nil
}

func (pw *progressWriter) Done() error {
	pw.reader.mu.Lock()
	delete(pw.reader.writers, pw)
	pw.reader.mu.Unlock()
	pw.reader.cond.Broadcast()
	pw.done = true
	return nil
}

type noOpWriter struct{}

func (pw *noOpWriter) Write(p interface{}) error {
	return nil
}

func (pw *noOpWriter) Done() error {
	return nil
}

// type ProgressRecord struct {
// 	UUID   string
// 	Parent string
// 	Done   bool
// 	*Progress
// }
