package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc/codes"
)

type SecretStore interface {
	GetSecret(context.Context, string) ([]byte, error)
}

var ErrNotFound = errors.New("not found")

func GetSecret(ctx context.Context, c session.Caller, id string) ([]byte, error) {
	client := NewSecretsClient(c.Conn())
	resp, err := client.GetSecret(ctx, &GetSecretRequest{
		ID: id,
	})
	if err != nil {
		if code := grpcerrors.Code(err); code == codes.Unimplemented || code == codes.NotFound {
			return nil, fmt.Errorf("secret %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	return resp.Data, nil
}
