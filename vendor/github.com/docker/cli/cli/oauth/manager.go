package oauth

import (
	"context"
	"io"
)

// TokenResult is a result from the auth manager.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	RequireAuth  bool
	Tenant       string
	Claims       Claims
}

type Manager interface {
	LoginDevice(ctx context.Context, out io.Writer) (TokenResult, error)
	Logout(ctx context.Context, refreshToken string) error
	RefreshToken(ctx context.Context, refreshToken string) (TokenResult, error)
}
