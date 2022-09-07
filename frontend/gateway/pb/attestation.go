package moby_buildkit_v1_frontend //nolint:revive

import (
	"github.com/moby/buildkit/solver/result"
	"github.com/pkg/errors"
)

var toAttestationKind = map[result.AttestationKind]Attestation_Kind{
	result.InToto: Attestation_InToto,
}
var toSubjectKind = map[result.InTotoSubjectKind]InTotoSubject_Kind{
	result.Raw:  InTotoSubject_Raw,
	result.Self: InTotoSubject_Self,
}
var fromAttestationKind = map[Attestation_Kind]result.AttestationKind{
	Attestation_InToto: result.InToto,
}
var fromSubjectKind = map[InTotoSubject_Kind]result.InTotoSubjectKind{
	InTotoSubject_Raw:  result.Raw,
	InTotoSubject_Self: result.Self,
}

func ToAttestationPB(a *result.Attestation) (*Attestation, error) {
	subjects := make([]*InTotoSubject, len(a.InToto.Subjects))
	for i, subject := range a.InToto.Subjects {
		k, ok := toSubjectKind[subject.Kind]
		if !ok {
			return nil, errors.Errorf("unknown in toto subject kind %q", subject.Kind)
		}
		subjects[i] = &InTotoSubject{
			Kind:      k,
			RawName:   subject.Name,
			RawDigest: subject.Digest,
		}
	}

	k, ok := toAttestationKind[a.Kind]
	if !ok {
		return nil, errors.Errorf("unknown attestation kind %q", a.Kind)
	}
	return &Attestation{
		Kind:                k,
		Path:                a.Path,
		Ref:                 a.Ref,
		InTotoPredicateType: a.InToto.PredicateType,
		InTotoSubjects:      subjects,
	}, nil
}

func FromAttestationPB(a *Attestation) (*result.Attestation, error) {
	subjects := make([]result.InTotoSubject, len(a.InTotoSubjects))
	for i, subject := range a.InTotoSubjects {
		k, ok := fromSubjectKind[subject.Kind]
		if !ok {
			return nil, errors.Errorf("unknown in toto subject kind %q", subject.Kind)
		}
		subjects[i] = result.InTotoSubject{
			Kind:   k,
			Name:   subject.RawName,
			Digest: subject.RawDigest,
		}
	}

	k, ok := fromAttestationKind[a.Kind]
	if !ok {
		return nil, errors.Errorf("unknown attestation kind %q", a.Kind)
	}
	return &result.Attestation{
		Kind: k,
		Path: a.Path,
		Ref:  a.Ref,
		InToto: result.InTotoAttestation{
			PredicateType: a.InTotoPredicateType,
			Subjects:      subjects,
		},
	}, nil
}
