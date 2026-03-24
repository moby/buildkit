package intoto

import (
	"bytes"
	"encoding/json"
	"maps"

	attestationv1 "github.com/in-toto/attestation/go/v1"
	legacyintoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

var (
	marshalOptions   = protojson.MarshalOptions{}
	unmarshalOptions = protojson.UnmarshalOptions{}
)

// Subject is the subset of the in-toto v1 resource descriptor.
type Subject struct {
	Name   string            `json:"name,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
}

// Statement is a local compatibility wrapper around the in-toto v1 protobuf
// statement. It keeps protojson isolated at the boundary while still accepting
// legacy v0.1 JSON on decode.
type Statement struct {
	Type          string
	Subject       []Subject
	PredicateType string
	Predicate     json.RawMessage
}

func ToResourceDescriptors(subjects []Subject) []*attestationv1.ResourceDescriptor {
	out := make([]*attestationv1.ResourceDescriptor, 0, len(subjects))
	for _, subject := range subjects {
		out = append(out, &attestationv1.ResourceDescriptor{
			Name:   subject.Name,
			Digest: maps.Clone(subject.Digest),
		})
	}
	return out
}

func FromResourceDescriptors(subjects []*attestationv1.ResourceDescriptor) []Subject {
	out := make([]Subject, 0, len(subjects))
	for _, subject := range subjects {
		if subject == nil {
			continue
		}
		out = append(out, Subject{
			Name:   subject.GetName(),
			Digest: maps.Clone(subject.GetDigest()),
		})
	}
	return out
}

func (s Statement) MarshalJSON() ([]byte, error) {
	stmt, err := s.toProtobuf()
	if err != nil {
		return nil, err
	}
	return marshalOptions.Marshal(stmt)
}

func (s *Statement) UnmarshalJSON(data []byte) error {
	var stmt attestationv1.Statement
	if err := unmarshalOptions.Unmarshal(data, &stmt); err != nil {
		return errors.Wrap(err, "cannot decode in-toto statement")
	}
	if !validStatementType(stmt.GetType()) {
		return errors.Errorf("unsupported in-toto statement type %q", stmt.GetType())
	}
	return s.fromProtobuf(&stmt)
}

func (s Statement) toProtobuf() (*attestationv1.Statement, error) {
	predicate, err := toPredicateStruct(s.Predicate)
	if err != nil {
		return nil, err
	}
	return &attestationv1.Statement{
		Type:          attestationv1.StatementTypeUri,
		Subject:       ToResourceDescriptors(s.Subject),
		PredicateType: s.PredicateType,
		Predicate:     predicate,
	}, nil
}

func (s *Statement) fromProtobuf(stmt *attestationv1.Statement) error {
	predicate, err := fromPredicateStruct(stmt.GetPredicate())
	if err != nil {
		return err
	}
	s.Type = stmt.GetType()
	s.Subject = FromResourceDescriptors(stmt.GetSubject())
	s.PredicateType = stmt.GetPredicateType()
	s.Predicate = predicate
	return nil
}

func toPredicateStruct(raw json.RawMessage) (*structpb.Struct, error) {
	raw = normalizePredicate(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	var predicate map[string]any
	if err := json.Unmarshal(raw, &predicate); err != nil {
		return nil, errors.Wrap(err, "cannot decode in-toto predicate as object")
	}
	st, err := structpb.NewStruct(predicate)
	if err != nil {
		return nil, errors.Wrap(err, "cannot convert in-toto predicate to protobuf")
	}
	return st, nil
}

func fromPredicateStruct(predicate *structpb.Struct) (json.RawMessage, error) {
	if predicate == nil {
		return nil, nil
	}
	dt, err := marshalOptions.Marshal(predicate)
	if err != nil {
		return nil, errors.Wrap(err, "cannot encode in-toto predicate")
	}
	return normalizePredicate(dt), nil
}

func normalizePredicate(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	return bytes.Clone(raw)
}

func validStatementType(t string) bool {
	switch t {
	case legacyintoto.StatementInTotoV01, attestationv1.StatementTypeUri:
		return true
	default:
		return false
	}
}
