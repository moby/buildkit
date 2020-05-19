package llb

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
)

type SourceMap struct {
	State      *State
	Definition *Definition
	Filename   string
	Data       []byte
}

func NewSourceMap(st *State, filename string, dt []byte) *SourceMap {
	return &SourceMap{
		State:    st,
		Filename: filename,
		Data:     dt,
	}
}

func (s *SourceMap) Location(r []*pb.Range) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		if s == nil {
			return
		}
		c.Source = &SourceLocation{
			SourceMap: s,
			Location:  r,
		}
	})
}

type SourceLocation struct {
	SourceMap *SourceMap
	Location  []*pb.Range
}

type sourceMapCollector struct {
	locations []map[digest.Digest][]*pb.Range
	maps      []*SourceMap
	index     map[*SourceMap]int
}

func newSourceMapCollector() *sourceMapCollector {
	return &sourceMapCollector{
		index: map[*SourceMap]int{},
	}
}

func (smc *sourceMapCollector) Add(dgst digest.Digest, l *SourceLocation) {
	idx, ok := smc.index[l.SourceMap]
	if !ok {
		idx = len(smc.maps)
		smc.maps = append(smc.maps, l.SourceMap)
		smc.locations = append(smc.locations, map[digest.Digest][]*pb.Range{})
	}
	smc.locations[idx][dgst] = l.Location
}

func (smc *sourceMapCollector) Marshal(ctx context.Context, co ...ConstraintsOpt) ([]*pb.Source, error) {
	out := make([]*pb.Source, 0, len(smc.maps))
	for i, m := range smc.maps {
		def := m.Definition
		if def == nil && m.State != nil {
			var err error
			def, err = m.State.Marshal(ctx, co...)
			if err != nil {
				return nil, err
			}
			m.Definition = def
		}
		s := &pb.Source{
			Info: &pb.SourceInfo{
				Data:     m.Data,
				Filename: m.Filename,
			},
			Locations: map[string]*pb.Location{},
		}
		if def != nil {
			s.Info.Definition = def.ToPB()
		}
		for dgst, loc := range smc.locations[i] {
			s.Locations[dgst.String()] = &pb.Location{
				Locations: loc,
			}
		}
		out = append(out, s)
	}
	return out, nil
}
