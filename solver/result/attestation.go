package result

import (
	digest "github.com/opencontainers/go-digest"
)

type AttestationKind string
type InTotoSubjectKind string

const (
	InToto AttestationKind = "in-toto"
)

const (
	Self InTotoSubjectKind = "self"
	Raw  InTotoSubjectKind = "raw"
)

type Attestation struct {
	Kind AttestationKind

	Ref  string
	Path string

	InToto InTotoAttestation
}

type InTotoAttestation struct {
	PredicateType string
	Subjects      []InTotoSubject
}

type InTotoSubject struct {
	Kind InTotoSubjectKind

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
