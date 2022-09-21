package result

import (
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	digest "github.com/opencontainers/go-digest"
)

type Attestation struct {
	Kind pb.AttestationKind

	Ref  string
	Path string

	InToto InTotoAttestation
}

type InTotoAttestation struct {
	PredicateType string
	Subjects      []InTotoSubject
}

type InTotoSubject struct {
	Kind pb.InTotoSubjectKind

	Name   string
	Digest []digest.Digest
}

func DigestMap(ds ...digest.Digest) map[string]string {
	m := map[string]string{}
	for _, d := range ds {
		m[d.Algorithm().String()] = d.Encoded()
	}
	return m
}
