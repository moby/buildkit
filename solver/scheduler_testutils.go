package solver

import (
	"maps"

	digest "github.com/opencontainers/go-digest"
)

func (jl *Solver) Inspect() map[digest.Digest]map[*Job]struct{} {
	jl.mu.RLock()
	defer jl.mu.RUnlock()

	actives := make(map[digest.Digest]map[*Job]struct{})
	for dgst, st := range jl.actives {
		st.mu.RLock()
		actives[dgst] = maps.Clone(st.jobs)
		st.mu.RUnlock()
	}
	return actives
}
