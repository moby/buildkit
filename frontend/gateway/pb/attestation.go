package moby_buildkit_v1_frontend //nolint:revive

import (
	"github.com/moby/buildkit/solver/result"
	"github.com/pkg/errors"
)

var toAttestationKind = map[int]Attestation_Kind{
	result.AttestationKindInToto: Attestation_InToto,
}
var toSubjectKind = map[int]InTotoSubject_Kind{
	result.InTotoSubjectKindRaw:  InTotoSubject_Raw,
	result.InTotoSubjectKindSelf: InTotoSubject_Self,
}
var fromAttestationKind = map[Attestation_Kind]int{
	Attestation_InToto: result.AttestationKindInToto,
}
var fromSubjectKind = map[InTotoSubject_Kind]int{
	InTotoSubject_Raw:  result.InTotoSubjectKindRaw,
	InTotoSubject_Self: result.InTotoSubjectKindSelf,
}

func ToAttestationPB(a *result.Attestation) (*Attestation, error) {
	subjects := make([]*InTotoSubject, len(a.InTotoSubjects))
	for i, subject := range a.InTotoSubjects {
		k, ok := toSubjectKind[subject.Kind]
		if !ok {
			return nil, errors.New("unknown in toto subject kind")
		}
		subjects[i] = &InTotoSubject{
			Kind:      k,
			RawName:   subject.Name,
			RawDigest: subject.Digest,
		}
	}

	k, ok := toAttestationKind[a.Kind]
	if !ok {
		return nil, errors.New("unknown attestation kind")
	}
	return &Attestation{
		Kind:                k,
		Path:                a.Path,
		Ref:                 a.Ref,
		InTotoPredicateType: a.InTotoPredicateType,
		InTotoSubjects:      subjects,
	}, nil
}

func FromAttestationPB(a *Attestation) (*result.Attestation, error) {
	subjects := make([]result.InTotoSubject, len(a.InTotoSubjects))
	for i, subject := range a.InTotoSubjects {
		k, ok := fromSubjectKind[subject.Kind]
		if !ok {
			return nil, errors.New("unknown in toto subject kind")
		}
		subjects[i] = result.InTotoSubject{
			Kind:   k,
			Name:   subject.RawName,
			Digest: subject.RawDigest,
		}
	}

	k, ok := fromAttestationKind[a.Kind]
	if !ok {
		return nil, errors.New("unknown attestation kind")
	}
	return &result.Attestation{
		Kind:                k,
		Path:                a.Path,
		Ref:                 a.Ref,
		InTotoPredicateType: a.InTotoPredicateType,
		InTotoSubjects:      subjects,
	}, nil
}
