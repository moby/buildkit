package result

import (
	digest "github.com/opencontainers/go-digest"
)

const (
	AttestationKindInToto = iota
)

const (
	InTotoSubjectKindRaw = iota
	InTotoSubjectKindSelf
)

type Attestation struct {
	Kind int

	Ref                 string
	Path                string
	InTotoPredicateType string
	InTotoSubjects      []InTotoSubject
}

type InTotoSubject struct {
	Kind int

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
