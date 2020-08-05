package auth

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc/codes"
)

func CredentialsFunc(sm *session.Manager, g session.Group) func(string) (session, username, secret string, err error) {
	return func(host string) (string, string, string, error) {
		var sessionID, user, secret string
		err := sm.Any(context.TODO(), g, func(ctx context.Context, id string, c session.Caller) error {
			client := NewAuthClient(c.Conn())

			resp, err := client.Credentials(ctx, &CredentialsRequest{
				Host: host,
			})
			if err != nil {
				if grpcerrors.Code(err) == codes.Unimplemented {
					return nil
				}
				return err
			}
			sessionID = id
			user = resp.Username
			secret = resp.Secret
			return nil
		})
		if err != nil {
			return "", "", "", err
		}
		return sessionID, user, secret, nil
	}
}
