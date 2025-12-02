package main

import (
	"context"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/exporter/attestation"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/result"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func MakeInTotoStatements(ctx context.Context, attestations []result.Attestation[client.Reference], defaultSubjects []intoto.Subject) ([]intoto.Statement, error) {
	eg, ctx := errgroup.WithContext(ctx)
	statements := make([]intoto.Statement, len(attestations))

	for i, att := range attestations {
		i, att := i, att
		eg.Go(func() error {
			content, err := readAttestation(ctx, att)
			if err != nil {
				return err
			}

			switch att.Kind {
			case gatewaypb.AttestationKind_InToto:
				stmt, err := attestation.MakeInTotoStatement(content, att, defaultSubjects)
				if err != nil {
					return err
				}
				statements[i] = *stmt
			case gatewaypb.AttestationKind_Bundle:
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

func readAttestation(ctx context.Context, att result.Attestation[client.Reference]) ([]byte, error) {
	var content []byte
	var err error
	if att.ContentFunc != nil {
		content, err = att.ContentFunc(ctx)
	}
	if att.Ref != nil {
		content, err = att.Ref.ReadFile(ctx, client.ReadRequest{Filename: att.Path})
	}
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		content = nil
	}
	return content, nil
}
