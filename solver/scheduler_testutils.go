package solver

import (
	"maps"

	digest "github.com/opencontainers/go-digest"
)

// Inspect is an UNSAFE METHOD MEANT ONLY FOR UNIT TESTS.
//
// This is not something that is meant to be part of the public API.
// It exists to provide an escape hatch for a unit test to inspect the
// job state.
//
// DO NOT USE OUTSIDE OF UNIT TESTS.
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
