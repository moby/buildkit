package attestation

import (
	"context"
	"encoding/json"
	"os"

	"github.com/containerd/continuity/fs"
	intotov1 "github.com/in-toto/attestation/go/v1"
	"github.com/moby/buildkit/exporter"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/result"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/structpb"
)

// ReadAll reads the content of an attestation.
func ReadAll(ctx context.Context, s session.Group, att exporter.Attestation) ([]byte, error) {
	var content []byte
	if att.ContentFunc != nil {
		data, err := att.ContentFunc()
		if err != nil {
			return nil, err
		}
		content = data
	} else if att.Ref != nil {
		mount, err := att.Ref.Mount(ctx, true, s)
		if err != nil {
			return nil, err
		}
		lm := snapshot.LocalMounter(mount)
		src, err := lm.Mount()
		if err != nil {
			return nil, err
		}
		defer lm.Unmount()

		p, err := fs.RootPath(src, att.Path)
		if err != nil {
			return nil, err
		}
		content, err = os.ReadFile(p)
		if err != nil {
			return nil, errors.Wrap(err, "cannot read in-toto attestation")
		}
	} else {
		return nil, errors.New("no available content for attestation")
	}
	if len(content) == 0 {
		content = nil
	}
	return content, nil
}

// MakeInTotoStatements iterates over all provided result attestations and
// generates intoto attestation statements.
func MakeInTotoStatements(ctx context.Context, s session.Group, attestations []exporter.Attestation, defaultSubjects []*intotov1.ResourceDescriptor) ([]*intotov1.Statement, error) {
	eg, ctx := errgroup.WithContext(ctx)
	statements := make([]*intotov1.Statement, len(attestations))

	for i, att := range attestations {
		i, att := i, att
		eg.Go(func() error {
			content, err := ReadAll(ctx, s, att)
			if err != nil {
				return err
			}

			switch att.Kind {
			case gatewaypb.AttestationKindInToto:
				stmt, err := makeInTotoStatement(ctx, content, att, defaultSubjects)
				if err != nil {
					return err
				}
				statements[i] = stmt
			case gatewaypb.AttestationKindBundle:
				return errors.New("bundle attestation kind must be un-bundled first")
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return statements, nil
}

func makeInTotoStatement(ctx context.Context, content []byte, attestation exporter.Attestation, defaultSubjects []*intotov1.ResourceDescriptor) (*intotov1.Statement, error) {
	if len(attestation.InToto.Subjects) == 0 {
		attestation.InToto.Subjects = []result.InTotoSubject{{
			Kind: gatewaypb.InTotoSubjectKindSelf,
		}}
	}
	subjects := []*intotov1.ResourceDescriptor{}
	for _, subject := range attestation.InToto.Subjects {
		subjectName := "_"
		if subject.Name != "" {
			subjectName = subject.Name
		}

		switch subject.Kind {
		case gatewaypb.InTotoSubjectKindSelf:
			for _, defaultSubject := range defaultSubjects {
				subjectNames := []string{}
				subjectNames = append(subjectNames, defaultSubject.Name)
				if subjectName != "_" {
					subjectNames = append(subjectNames, subjectName)
				}

				for _, name := range subjectNames {
					subjects = append(subjects, &intotov1.ResourceDescriptor{
						Name:   name,
						Digest: defaultSubject.Digest,
					})
				}
			}
		case gatewaypb.InTotoSubjectKindRaw:
			subjects = append(subjects, &intotov1.ResourceDescriptor{
				Name:   subjectName,
				Digest: result.ToDigestMap(subject.Digest...),
			})
		default:
			return nil, errors.Errorf("unknown attestation subject type %T", subject)
		}
	}

	var pred map[string]interface{}
	err := json.Unmarshal(content, &pred)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal attestation predicate")
	}
	predicate, err := structpb.NewStruct(pred)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert attestation predicate to struct")
	}

	stmt := intotov1.Statement{
		Type:          intotov1.StatementTypeUri,
		Subject:       subjects,
		PredicateType: attestation.InToto.PredicateType,
		Predicate:     predicate,
	}
	return &stmt, nil
}
