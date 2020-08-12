package resolver

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/session"
	sessionauth "github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/resolver/auth"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type authHandlerNS struct {
	counter int64 // needs to be 64bit aligned for 32bit systems

	mu       sync.Mutex
	handlers map[string]*authHandler
	hosts    map[string][]docker.RegistryHost
	sm       *session.Manager
	g        flightcontrol.Group
}

func newAuthHandlerNS(sm *session.Manager) *authHandlerNS {
	return &authHandlerNS{
		handlers: map[string]*authHandler{},
		hosts:    map[string][]docker.RegistryHost{},
		sm:       sm,
	}
}

func (a *authHandlerNS) get(host string, sm *session.Manager, g session.Group) *authHandler {
	if g == nil {
		return nil
	}

	iter := g.SessionIterator()
	if iter == nil {
		return nil
	}

	for {
		id := iter.NextSession()
		if id == "" {
			break
		}
		h, ok := a.handlers[path.Join(host, id)]
		if ok {
			h.lastUsed = time.Now()
			return h
		}
	}

	// link another handler
	for k, h := range a.handlers {
		parts := strings.SplitN(k, "/", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == host {
			session, username, password, err := sessionauth.CredentialsFunc(sm, g)(host)
			if err == nil {
				if username == h.common.Username && password == h.common.Secret {
					a.handlers[path.Join(host, session)] = h
					h.lastUsed = time.Now()
					return h
				}
			}
		}
	}

	return nil
}

func (a *authHandlerNS) set(host, session string, h *authHandler) {
	a.handlers[path.Join(host, session)] = h
}

func (a *authHandlerNS) delete(h *authHandler) {
	for k, v := range a.handlers {
		if v == h {
			delete(a.handlers, k)
		}
	}
}

type dockerAuthorizer struct {
	client *http.Client

	sm       *session.Manager
	session  session.Group
	handlers *authHandlerNS
}

func newDockerAuthorizer(client *http.Client, handlers *authHandlerNS, sm *session.Manager, group session.Group) *dockerAuthorizer {
	return &dockerAuthorizer{
		client:   client,
		handlers: handlers,
		sm:       sm,
		session:  group,
	}
}

// Authorize handles auth request.
func (a *dockerAuthorizer) Authorize(ctx context.Context, req *http.Request) error {
	a.handlers.mu.Lock()
	defer a.handlers.mu.Unlock()

	// skip if there is no auth handler
	ah := a.handlers.get(req.URL.Host, a.sm, a.session)
	if ah == nil {
		return nil
	}

	auth, err := ah.authorize(ctx, a.session)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", auth)
	return nil
}

func (a *dockerAuthorizer) getCredentials(host string) (sessionID, username, secret string, err error) {
	return sessionauth.CredentialsFunc(a.sm, a.session)(host)
}

func (a *dockerAuthorizer) AddResponses(ctx context.Context, responses []*http.Response) error {
	a.handlers.mu.Lock()
	defer a.handlers.mu.Unlock()

	last := responses[len(responses)-1]
	host := last.Request.URL.Host

	handler := a.handlers.get(host, a.sm, a.session)

	for _, c := range auth.ParseAuthHeader(last.Header) {
		if c.Scheme == auth.BearerAuth {
			if err := invalidAuthorization(c, responses); err != nil {
				a.handlers.delete(handler)

				oldScope := ""
				if handler != nil {
					oldScope = strings.Join(handler.common.Scopes, " ")
				}
				handler = nil

				// this hacky way seems to be best method to detect that error is fatal and should not be retried with a new token
				if c.Parameters["error"] == "insufficient_scope" && c.Parameters["scope"] == oldScope {
					return err
				}
			}

			// reuse existing handler
			//
			// assume that one registry will return the common
			// challenge information, including realm and service.
			// and the resource scope is only different part
			// which can be provided by each request.
			if handler != nil {
				return nil
			}

			session, username, secret, err := a.getCredentials(host)
			if err != nil {
				return err
			}

			common, err := auth.GenerateTokenOptions(ctx, host, username, secret, c)
			if err != nil {
				return err
			}

			a.handlers.set(host, session, newAuthHandler(a.client, c.Scheme, common))

			return nil
		} else if c.Scheme == auth.BasicAuth {
			session, username, secret, err := a.getCredentials(host)
			if err != nil {
				return err
			}

			if username != "" && secret != "" {
				common := auth.TokenOptions{
					Username: username,
					Secret:   secret,
				}

				a.handlers.set(host, session, newAuthHandler(a.client, c.Scheme, common))

				return nil
			}
		}
	}
	return errors.Wrap(errdefs.ErrNotImplemented, "failed to find supported auth scheme")
}

// authResult is used to control limit rate.
type authResult struct {
	sync.WaitGroup
	token   string
	err     error
	expires time.Time
}

// authHandler is used to handle auth request per registry server.
type authHandler struct {
	sync.Mutex

	client *http.Client

	// only support basic and bearer schemes
	scheme auth.AuthenticationScheme

	// common contains common challenge answer
	common auth.TokenOptions

	// scopedTokens caches token indexed by scopes, which used in
	// bearer auth case
	scopedTokens map[string]*authResult

	lastUsed time.Time
}

func newAuthHandler(client *http.Client, scheme auth.AuthenticationScheme, opts auth.TokenOptions) *authHandler {
	return &authHandler{
		client:       client,
		scheme:       scheme,
		common:       opts,
		scopedTokens: map[string]*authResult{},
		lastUsed:     time.Now(),
	}
}

func (ah *authHandler) authorize(ctx context.Context, g session.Group) (string, error) {
	switch ah.scheme {
	case auth.BasicAuth:
		return ah.doBasicAuth(ctx)
	case auth.BearerAuth:
		return ah.doBearerAuth(ctx)
	default:
		return "", errors.Wrapf(errdefs.ErrNotImplemented, "failed to find supported auth scheme: %s", string(ah.scheme))
	}
}

func (ah *authHandler) doBasicAuth(ctx context.Context) (string, error) {
	username, secret := ah.common.Username, ah.common.Secret

	if username == "" || secret == "" {
		return "", fmt.Errorf("failed to handle basic auth because missing username or secret")
	}

	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + secret))
	return fmt.Sprintf("Basic %s", auth), nil
}

func (ah *authHandler) doBearerAuth(ctx context.Context) (token string, err error) {
	// copy common tokenOptions
	to := ah.common

	to.Scopes = docker.GetTokenScopes(ctx, to.Scopes)

	// Docs: https://docs.docker.com/registry/spec/auth/scope
	scoped := strings.Join(to.Scopes, " ")

	ah.Lock()
	if r, exist := ah.scopedTokens[scoped]; exist {
		ah.Unlock()
		r.Wait()
		if r.expires.IsZero() || r.expires.After(time.Now()) {
			return r.token, r.err
		}
		ah.Lock()
	}

	// only one fetch token job
	r := new(authResult)
	r.Add(1)
	ah.scopedTokens[scoped] = r
	ah.Unlock()

	var issuedAt time.Time
	var expires int
	defer func() {
		token = fmt.Sprintf("Bearer %s", token)
		r.token, r.err = token, err
		if err == nil {
			if issuedAt.IsZero() {
				issuedAt = time.Now()
			}
			if exp := issuedAt.Add(time.Duration(float64(expires)*0.9) * time.Second); time.Now().Before(exp) {
				r.expires = exp
			}
		}
		r.Done()
	}()

	// fetch token for the resource scope
	if to.Secret != "" {
		defer func() {
			err = errors.Wrap(err, "failed to fetch oauth token")
		}()
		// try GET first because Docker Hub does not support POST
		// switch once support has landed
		resp, err := auth.FetchToken(ctx, ah.client, nil, to)
		if err != nil {
			var errStatus auth.ErrUnexpectedStatus
			if errors.As(err, &errStatus) {
				// retry with POST request
				// As of September 2017, GCR is known to return 404.
				// As of February 2018, JFrog Artifactory is known to return 401.
				if (errStatus.StatusCode == 405 && to.Username != "") || errStatus.StatusCode == 404 || errStatus.StatusCode == 401 {
					resp, err := auth.FetchTokenWithOAuth(ctx, ah.client, nil, "containerd-client", to)
					if err != nil {
						return "", err
					}
					issuedAt, expires = resp.IssuedAt, resp.ExpiresIn
					return resp.AccessToken, nil
				}
				log.G(ctx).WithFields(logrus.Fields{
					"status": errStatus.Status,
					"body":   string(errStatus.Body),
				}).Debugf("token request failed")
			}
			return "", err
		}
		issuedAt, expires = resp.IssuedAt, resp.ExpiresIn
		return resp.Token, nil
	}
	// do request anonymously
	resp, err := auth.FetchToken(ctx, ah.client, nil, to)
	if err != nil {
		return "", errors.Wrap(err, "failed to fetch anonymous token")
	}
	issuedAt, expires = resp.IssuedAt, resp.ExpiresIn

	return resp.Token, nil
}

func invalidAuthorization(c auth.Challenge, responses []*http.Response) error {
	errStr := c.Parameters["error"]
	if errStr == "" {
		return nil
	}

	n := len(responses)
	if n == 1 || (n > 1 && !sameRequest(responses[n-2].Request, responses[n-1].Request)) {
		return nil
	}

	return errors.Wrapf(docker.ErrInvalidAuthorization, "server message: %s", errStr)
}

func sameRequest(r1, r2 *http.Request) bool {
	if r1.Method != r2.Method {
		return false
	}
	if *r1.URL != *r2.URL {
		return false
	}
	return true
}
