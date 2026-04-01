package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthDeadlockIntegration(t *testing.T) {
	integration.Run(t, integration.TestFuncs(testAuthDeadlockRepro))
}

type authChallengeRegistry struct {
	server *httptest.Server
	host   string
	ref    string
	token  string
}

func newAuthChallengeRegistry(t *testing.T) *authChallengeRegistry {
	t.Helper()

	r := &authChallengeRegistry{
		token: "test-token",
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"token":      r.token,
				"expires_in": 60,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		case strings.HasPrefix(req.URL.Path, "/v2/"):
			if req.Header.Get("Authorization") != "Bearer "+r.token {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="deadlock-registry",scope="repository:deadlock/test:pull"`, r.server.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if req.URL.Path == "/v2/" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
			return
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	r.server = httptest.NewServer(handler)
	t.Cleanup(r.server.Close)

	u, err := url.Parse(r.server.URL)
	require.NoError(t, err)
	r.host = u.Host
	r.ref = r.host + "/deadlock/test:latest"
	return r
}

type controlledAuthProvider struct {
	sessionauth.UnimplementedAuthServer

	host        string
	blockLookup bool
	started     chan struct{}
	release     chan struct{}
}

func (p *controlledAuthProvider) Register(server *grpc.Server) {
	sessionauth.RegisterAuthServer(server, p)
}

func (p *controlledAuthProvider) Credentials(_ context.Context, req *sessionauth.CredentialsRequest) (*sessionauth.CredentialsResponse, error) {
	if req.Host != p.host {
		return &sessionauth.CredentialsResponse{}, nil
	}
	return &sessionauth.CredentialsResponse{
		Username: "user",
		Secret:   "secret",
	}, nil
}

func (p *controlledAuthProvider) GetTokenAuthority(_ context.Context, req *sessionauth.GetTokenAuthorityRequest) (*sessionauth.GetTokenAuthorityResponse, error) {
	if req.Host == p.host && p.blockLookup {
		select {
		case <-p.started:
		default:
			close(p.started)
		}
		<-p.release
	}
	return nil, status.Error(codes.Unimplemented, "force credentials fallback")
}

func solveImageWithAuth(ctx context.Context, c *Client, imageRef string, authProvider session.Attachable) error {
	st := llb.Image(imageRef)
	def, err := st.Marshal(ctx)
	if err != nil {
		return err
	}
	_, err = c.Solve(ctx, def, SolveOpt{
		Session: []session.Attachable{authProvider},
	}, nil)
	return err
}

func testAuthDeadlockRepro(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	registry := newAuthChallengeRegistry(t)

	blockedClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer blockedClient.Close()

	fastClient, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer fastClient.Close()

	blockedProvider := &controlledAuthProvider{
		host:        registry.host,
		blockLookup: true,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	defer func() {
		select {
		case <-blockedProvider.release:
		default:
			close(blockedProvider.release)
		}
	}()

	fastProvider := &controlledAuthProvider{
		host:    registry.host,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	firstCtx, cancelFirst := context.WithTimeoutCause(sb.Context(), 30*time.Second, context.DeadlineExceeded)
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- solveImageWithAuth(firstCtx, blockedClient, registry.ref, blockedProvider)
	}()

	select {
	case <-blockedProvider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first solve to enter auth callback")
	}

	secondDone := make(chan error, 1)
	secondStart := time.Now()
	go func() {
		secondDone <- solveImageWithAuth(sb.Context(), fastClient, registry.ref, fastProvider)
	}()

	select {
	case err := <-secondDone:
		require.Error(t, err)
		require.Less(t, time.Since(secondStart), 2*time.Second, "second solve should not be blocked by unrelated hung auth callback")
	case <-time.After(2 * time.Second):
		t.Fatal("second solve blocked behind unrelated hung auth callback")
	}

	close(blockedProvider.release)
	select {
	case err := <-firstDone:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first solve did not finish after releasing auth callback")
	}
}
