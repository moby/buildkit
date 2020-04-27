package errdefs

import (
	"fmt"
	"io"
	"strings"

	pb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
)

func WithSource(err error, src *pb.Source) error {
	if err == nil {
		return nil
	}
	return &ErrorSource{Source: Source{Source: src}, error: err}
}

type ErrorSource struct {
	Source
	error
}

func (e *ErrorSource) Unwrap() error {
	return e.error
}

func (e *ErrorSource) ToProto() grpcerrors.TypedErrorProto {
	return &e.Source
}

func Sources(err error) []*Source {
	var out []*Source
	var es *ErrorSource
	if errors.As(err, &es) {
		out = Sources(es.Unwrap())
		out = append(out, &es.Source)
	}
	return out
}

func (s *Source) WrapError(err error) error {
	return &ErrorSource{error: err, Source: *s}
}

func (s *Source) Print(w io.Writer) error {
	ss := s.Source
	if ss == nil {
		return nil
	}
	lines := strings.Split(string(ss.Data), "\n")

	start, end, ok := getStartEndLine(ss.Locations)
	if !ok {
		return nil
	}
	if start > len(lines) || start < 1 {
		return nil
	}
	if end > len(lines) {
		end = len(lines)
	}

	pad := 2
	if end == start {
		pad = 4
	}
	var p int

	prepadStart := start
	for {
		if p >= pad {
			break
		}
		if start > 1 {
			start--
			p++
		}
		if end != len(lines) {
			end++
			p++
		}
		p++
	}

	fmt.Fprintf(w, "%s:%d\n--------------------\n", ss.Filename, prepadStart)
	for i := start; i <= end; i++ {
		pfx := "   "
		if containsLine(ss.Locations, i) {
			pfx = ">>>"
		}
		fmt.Fprintf(w, " %3d | %s %s\n", i, pfx, lines[i-1])
	}
	fmt.Fprintf(w, "--------------------\n")
	return nil
}

func containsLine(rr []*pb.Range, l int) bool {
	for _, r := range rr {
		var s, e int
		if r.Start == nil {
			continue
		}
		s = int(r.Start.Line)
		if r.End != nil {
			e = int(r.End.Line)
		}
		if e < s {
			e = s
		}
		if s <= l && e >= l {
			return true
		}
	}
	return false
}

func getStartEndLine(rr []*pb.Range) (start int, end int, ok bool) {
	for _, r := range rr {
		if r.Start != nil {
			if !ok || start > int(r.Start.Line) {
				start = int(r.Start.Line)
			}
			if end < start {
				end = start
			}
			ok = true
		}
		if r.End != nil {
			if end < int(r.End.Line) {
				end = int(r.End.Line)
			}
		}
	}
	return
}
