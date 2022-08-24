package frontend

import (
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/attestation"
)

type Result struct {
	Ref          solver.ResultProxy
	Refs         map[string]solver.ResultProxy
	Metadata     map[string][]byte
	Attestations map[string][]attestation.Attestation
}

func (r *Result) EachRef(fn func(solver.ResultProxy) error) (err error) {
	if r.Ref != nil {
		err = fn(r.Ref)
	}
	for _, r := range r.Refs {
		if r != nil {
			if err1 := fn(r); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	return err
}
