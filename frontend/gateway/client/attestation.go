package client

import (
	"context"

	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/result"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func AttestationToPB[T any](ctx context.Context, a *result.Attestation[T]) (*pb.Attestation, error) {
	subjects := make([]*pb.InTotoSubject, len(a.InToto.Subjects))
	for i, subject := range a.InToto.Subjects {
		subjects[i] = &pb.InTotoSubject{
			Kind:   subject.Kind,
			Name:   subject.Name,
			Digest: digestSliceToPB(subject.Digest),
		}
	}

	var content []byte
	if a.ContentFunc != nil {
		var err error
		content, err = a.ContentFunc(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get attestation content")
		}
	}

	return &pb.Attestation{
		Kind:                a.Kind,
		Metadata:            a.Metadata,
		Path:                a.Path,
		Content:             content,
		InTotoPredicateType: a.InToto.PredicateType,
		InTotoSubjects:      subjects,
	}, nil
}

func AttestationFromPB[T any](a *pb.Attestation) (*result.Attestation[T], error) {
	if a == nil {
		return nil, errors.Errorf("invalid nil attestation")
	}
	subjects := make([]result.InTotoSubject, len(a.InTotoSubjects))
	for i, subject := range a.InTotoSubjects {
		if subject == nil {
			return nil, errors.Errorf("invalid nil attestation subject")
		}
		subjects[i] = result.InTotoSubject{
			Kind:   subject.Kind,
			Name:   subject.Name,
			Digest: digestSliceFromPB(subject.Digest),
		}
	}

	var contentF func(context.Context) ([]byte, error)
	if a.Content != nil {
		content := a.Content
		contentF = func(ctx context.Context) ([]byte, error) {
			return content, nil
		}
	}

	return &result.Attestation[T]{
		Kind:        a.Kind,
		Metadata:    a.Metadata,
		Path:        a.Path,
		ContentFunc: contentF,
		InToto: result.InTotoAttestation{
			PredicateType: a.InTotoPredicateType,
			Subjects:      subjects,
		},
	}, nil
}

func digestSliceToPB(elems []digest.Digest) []string {
	clone := make([]string, len(elems))
	for i, e := range elems {
		clone[i] = string(e)
	}
	return clone
}

func digestSliceFromPB(elems []string) []digest.Digest {
	clone := make([]digest.Digest, len(elems))
	for i, e := range elems {
		clone[i] = digest.Digest(e)
	}
	return clone
}
