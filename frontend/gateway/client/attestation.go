package client

import (
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/result"
)

func AttestationToPB(a *result.Attestation) (*pb.Attestation, error) {
	subjects := make([]*pb.InTotoSubject, len(a.InToto.Subjects))
	for i, subject := range a.InToto.Subjects {
		subjects[i] = &pb.InTotoSubject{
			Kind:   subject.Kind,
			Name:   subject.Name,
			Digest: subject.Digest,
		}
	}

	return &pb.Attestation{
		Kind:                a.Kind,
		Path:                a.Path,
		Ref:                 a.Ref,
		InTotoPredicateType: a.InToto.PredicateType,
		InTotoSubjects:      subjects,
	}, nil
}

func AttestationFromPB(a *pb.Attestation) (*result.Attestation, error) {
	subjects := make([]result.InTotoSubject, len(a.InTotoSubjects))
	for i, subject := range a.InTotoSubjects {
		subjects[i] = result.InTotoSubject{
			Kind:   subject.Kind,
			Name:   subject.Name,
			Digest: subject.Digest,
		}
	}

	return &result.Attestation{
		Kind: a.Kind,
		Path: a.Path,
		Ref:  a.Ref,
		InToto: result.InTotoAttestation{
			PredicateType: a.InTotoPredicateType,
			Subjects:      subjects,
		},
	}, nil
}
