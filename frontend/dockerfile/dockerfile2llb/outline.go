package dockerfile2llb

import (
	"sort"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	pb "github.com/moby/buildkit/solver/pb"
)

type outlineCapture struct {
	allArgs  map[string]argInfo
	usedArgs map[string]struct{}
}

type argInfo struct {
	value      string
	definition instructions.KeyValuePairOptional
	deps       map[string]struct{}
	location   []parser.Range
}

func (ai *argInfo) line() int {
	if len(ai.location) == 0 {
		return 0
	}
	return ai.location[0].Start.Line
}

func newOutlineCapture() outlineCapture {
	return outlineCapture{
		allArgs:  map[string]argInfo{},
		usedArgs: map[string]struct{}{},
	}
}

func (o outlineCapture) clone() outlineCapture {
	allArgs := map[string]argInfo{}
	for k, v := range o.allArgs {
		allArgs[k] = v
	}
	usedArgs := map[string]struct{}{}
	for k := range o.usedArgs {
		usedArgs[k] = struct{}{}
	}
	return outlineCapture{
		allArgs:  allArgs,
		usedArgs: usedArgs,
	}
}

func (o outlineCapture) markAllUsed(in map[string]struct{}) {
	for k := range in {
		if a, ok := o.allArgs[k]; ok {
			o.markAllUsed(a.deps)
		}
		o.usedArgs[k] = struct{}{}
	}
}

func (ds *dispatchState) args(visited map[string]struct{}) []argInfo {
	ds.outline.markAllUsed(ds.outline.usedArgs)

	args := make([]argInfo, 0, len(ds.outline.usedArgs))
	for k := range ds.outline.usedArgs {
		if a, ok := ds.outline.allArgs[k]; ok {
			if _, ok := visited[k]; !ok {
				args = append(args, a)
				visited[k] = struct{}{}
			}
		}
	}

	if ds.base != nil {
		args = append(args, ds.base.args(visited)...)
	}
	for d := range ds.deps {
		args = append(args, d.args(visited)...)
	}

	return args
}

func (ds *dispatchState) Outline(dt []byte) outline.Outline {
	args := ds.args(map[string]struct{}{})
	sort.Slice(args, func(i, j int) bool {
		return args[i].line() < args[j].line()
	})

	out := outline.Outline{
		Sources: [][]byte{dt},
	}

	out.Args = make([]outline.Arg, len(args))
	for i, a := range args {
		out.Args[i] = outline.Arg{
			Name:        a.definition.Key,
			Value:       a.value,
			Description: a.definition.Comment,
			Location:    toSourceLocation(a.location),
		}
	}

	return out
}

func toSourceLocation(r []parser.Range) *pb.Location {
	if len(r) == 0 {
		return nil
	}
	arr := make([]*pb.Range, len(r))
	for i, r := range r {
		arr[i] = &pb.Range{
			Start: pb.Position{
				Line:      int32(r.Start.Line),
				Character: int32(r.Start.Character),
			},
			End: pb.Position{
				Line:      int32(r.End.Line),
				Character: int32(r.End.Character),
			},
		}
	}
	return &pb.Location{Ranges: arr}
}
