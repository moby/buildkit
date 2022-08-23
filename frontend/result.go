package frontend

import (
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/attestation"
)

type Attestation interface {
	isFrontendAttestation()
}

type InTotoAttestation struct {
	PredicateType string
	PredicateRef  solver.ResultProxy
	PredicatePath string
	Subjects      []attestation.InTotoSubject
}

func (a *InTotoAttestation) isFrontendAttestation() {}

type Result struct {
	Ref          solver.ResultProxy
	Refs         map[string]solver.ResultProxy
	Metadata     map[string][]byte
	Attestations map[string][]Attestation
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
	for _, as := range r.Attestations {
		for _, a := range as {
			switch a := a.(type) {
			case *InTotoAttestation:
				if err1 := fn(a.PredicateRef); err1 != nil && err == nil {
					err = err1
				}
			}
		}
	}
	return err
}
