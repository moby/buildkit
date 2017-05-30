package progress

import (
	"context"
	"time"

	"github.com/pkg/errors"
)

func FromContext(ctx context.Context, name string) (*ProgressWriter, bool, context.Context) {
	return nil, false, ctx
}

func NewContext(ctx context.Context) (*ProgressReader, context.Context) {
	return nil, ctx
}

type ProgressWriter interface {
	Write(Progress) error
	Done() error
}

type ProgressReader interface {
	Read(context.Context) (*Progress, error)
}

type Progress struct {
	ID string

	// Progress contains a Message or...
	Message string

	// ...progress of an action
	Action    string
	Current   int64
	Total     int64
	Timestamp time.Time
	Done      bool
}

type progressReader struct{}

func (pr *progressReader) Read(ctx context.Context) (*Progress, error) {
	return nil, errors.Errorf("Read not implemented")
}

type progressWriter struct{}

func (pw *progressWriter) Write(p Progress) error {
	return errors.Errorf("Write not implemented")
}

func (pw *progressWriter) Done() error {
	return errors.Errorf("Done not implemented")
}

// type ProgressRecord struct {
// 	UUID   string
// 	Parent string
// 	Done   bool
// 	*Progress
// }
