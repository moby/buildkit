package solver

import (
	"maps"

	digest "github.com/opencontainers/go-digest"
)

func (s *Solver) Inspect() map[digest.Digest]map[*Job]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	actives := make(map[digest.Digest]map[*Job]struct{})
	for dgst, st := range s.actives {
		actives[dgst] = maps.Clone(st.jobs)
	}
	return actives
}
