package moby_buildkit_v1_frontend //nolint:revive

import (
	"github.com/moby/buildkit/util/attestation"
	"github.com/pkg/errors"
)

func ToAttestationPB(a attestation.Attestation) (*Attestations_Attestation, error) {
	switch a := a.(type) {
	case *attestation.InTotoAttestation:
		subjects := []*InToto_Subject{}
		for _, subject := range a.Subjects {
			switch s := subject.(type) {
			case *attestation.InTotoSubjectRaw:
				subjects = append(subjects, &InToto_Subject{
					Subject: &InToto_Subject_Raw{
						Raw: &InToto_Subject_RawSubject{
							Name:   s.Name,
							Digest: s.Digest,
						},
					},
				})
			case *attestation.InTotoSubjectSelf:
				subjects = append(subjects, &InToto_Subject{
					Subject: &InToto_Subject_Self{
						Self: &InToto_Subject_SelfSubject{},
					},
				})
			default:
				return nil, errors.Errorf("unknown in toto subject type %T", s)
			}
		}

		intoto := &InToto{
			PredicateType:   a.PredicateType,
			PredicatePath:   a.PredicatePath,
			PredicateRefKey: a.PredicateRefKey,
			Subjects:        subjects,
		}
		return &Attestations_Attestation{
			Attestation: &Attestations_Attestation_Intoto{intoto},
		}, nil
	default:
		return nil, errors.Errorf("unknown attestation type %T", a)
	}
}

func FromAttestationPB(a *Attestations_Attestation) (attestation.Attestation, error) {
	switch a := a.Attestation.(type) {
	case *Attestations_Attestation_Intoto:
		subjects := []attestation.InTotoSubject{}
		for _, pbSubject := range a.Intoto.Subjects {
			switch pbSubject := pbSubject.Subject.(type) {
			case *InToto_Subject_Raw:
				subjects = append(subjects, &attestation.InTotoSubjectRaw{
					Name:   pbSubject.Raw.Name,
					Digest: pbSubject.Raw.Digest,
				})
			case *InToto_Subject_Self:
				subjects = append(subjects, &attestation.InTotoSubjectSelf{})
			default:
				return nil, errors.Errorf("unknown in toto subject type %T", pbSubject)
			}
		}

		return &attestation.InTotoAttestation{
			PredicateType:   a.Intoto.PredicateType,
			PredicatePath:   a.Intoto.PredicatePath,
			PredicateRefKey: a.Intoto.PredicateRefKey,
			Subjects:        subjects,
		}, nil
	default:
		return nil, errors.Errorf("unknown attestation type %T", a)
	}
}
