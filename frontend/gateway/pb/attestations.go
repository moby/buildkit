package moby_buildkit_v1_frontend //nolint:revive

import (
	"github.com/moby/buildkit/util/attestation"
	"github.com/pkg/errors"
)

func ToInTotoPB(predicateType string, predicateRef *Ref, predicatePath string, subjects ...attestation.InTotoSubject) (*Attestations_Attestation_Intoto, error) {
	pbSubjects := []*InToto_Subject{}
	for _, subject := range subjects {
		switch s := subject.(type) {
		case *attestation.InTotoSubjectRaw:
			pbSubjects = append(pbSubjects, &InToto_Subject{
				Subject: &InToto_Subject_Raw{
					Raw: &InToto_Subject_RawSubject{
						Name:   s.Name,
						Digest: s.Digest,
					},
				},
			})
		case *attestation.InTotoSubjectSelf:
			pbSubjects = append(pbSubjects, &InToto_Subject{
				Subject: &InToto_Subject_Self{
					Self: &InToto_Subject_SelfSubject{},
				},
			})
		default:
			return nil, errors.Errorf("unknown in toto subject type %T", s)
		}
	}

	intoto := &InToto{
		PredicateType: predicateType,
		PredicatePath: predicatePath,
		PredicateRef:  predicateRef,
		Subjects:      pbSubjects,
	}

	return &Attestations_Attestation_Intoto{
		Intoto: intoto,
	}, nil
}

func FromInTotoPB(att *Attestations_Attestation_Intoto) (string, *Ref, string, []attestation.InTotoSubject, error) {
	subjects := []attestation.InTotoSubject{}
	for _, pbSubject := range att.Intoto.Subjects {
		switch pbSubject := pbSubject.Subject.(type) {
		case *InToto_Subject_Raw:
			subjects = append(subjects, &attestation.InTotoSubjectRaw{
				Name:   pbSubject.Raw.Name,
				Digest: pbSubject.Raw.Digest,
			})
		case *InToto_Subject_Self:
			subjects = append(subjects, &attestation.InTotoSubjectSelf{})
		default:
			return "", nil, "", nil, errors.Errorf("unknown in toto subject type %T", pbSubject)
		}
	}
	return att.Intoto.PredicateType, att.Intoto.PredicateRef, att.Intoto.PredicatePath, subjects, nil
}
