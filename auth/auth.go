package auth

import (
	"context"
	"net/http"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	errMissingMetadata = status.Errorf(codes.InvalidArgument, "missing metadata")
	errInvalidToken    = status.Errorf(codes.Unauthenticated, "invalid okteto token, run `okteto login` and try again")
)

// Service is the service buildkit can use to authenticate
type Service struct {
	endpoint string
	client   *http.Client
}

// NewService returns an AuthService configured to use endpoint
func NewService(endpoint string) *Service {
	return &Service{
		endpoint: endpoint,
		client:   &http.Client{},
	}
}

// EnsureValidToken validates that the context includes authentication metadata
// and that it's valild
func (s *Service) EnsureValidToken(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, errMissingMetadata
	}

	if !s.valid(md["authorization"]) {
		return ctx, errInvalidToken
	}

	return ctx, nil
}

func (s *Service) valid(authorization []string) bool {
	if len(authorization) < 1 {
		logrus.Error("request didn't contain an authorization header")
		return false
	}

	req, err := http.NewRequest("POST", s.endpoint, nil)
	if err != nil {
		logrus.Error("couldn't create request: %s", err)
		return false
	}

	req.Header.Add("Authorization", authorization[0])
	resp, err := s.client.Do(req)
	if err != nil {
		logrus.Error("authentication request failed: %s", err)
		return false
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		logrus.Error("%s is a bad token: %s | %d", authorization[0], resp.StatusCode)
		return false
	}

	return true
}
