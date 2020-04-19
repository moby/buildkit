package parser

import "github.com/pkg/errors"

type ErrorLocation struct {
	Ranges []Range
	error
}

func (e *ErrorLocation) Unwrap() error {
	return e.error
}

type Range struct {
	Start Position
	End   Position
}

type Position struct {
	Line      int
	Character int
}

func withLocation(err error, startLine, endLine int) error {
	if err == nil {
		return nil
	}
	return errors.WithStack(&ErrorLocation{
		error:  err,
		Ranges: toRanges(startLine, endLine),
	})
}

func toRanges(start, end int) (r []Range) {
	if end <= start {
		end = start
	}
	for i := start; i <= end; i++ {
		r = append(r, Range{Start: Position{Line: i}, End: Position{Line: i}})
	}
	return
}
