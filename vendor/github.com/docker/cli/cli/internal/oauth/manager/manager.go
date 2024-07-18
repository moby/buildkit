package manager

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/cli/cli/internal/oauth/api"
	"github.com/docker/cli/cli/oauth"

	"github.com/pkg/browser"
)

// OAuthManager is the manager
type OAuthManager struct {
	api         api.OAuthAPI
	audience    string
	tenant      string
	openBrowser func(string) error
}

// OAuthManagerOptions are the options used for New to create a new auth manager.
type OAuthManagerOptions struct {
	Audience    string
	ClientID    string
	Scopes      []string
	Tenant      string
	DeviceName  string
	OpenBrowser func(string) error
}

func New(options OAuthManagerOptions) *OAuthManager {
	scopes := []string{"openid", "offline_access"}
	if len(options.Scopes) > 0 {
		scopes = options.Scopes
	}

	openBrowser := browser.OpenURL
	if options.OpenBrowser != nil {
		openBrowser = options.OpenBrowser
	}

	return &OAuthManager{
		audience: options.Audience,
		api: api.API{
			BaseURL:  "https://" + options.Tenant,
			ClientID: options.ClientID,
			Scopes:   scopes,
		},
		tenant:      options.Tenant,
		openBrowser: openBrowser,
	}
}

// LoginDevice launches the device authentication flow with the tenant, printing
// instructions to the provided writer and attempting to open the browser for
// the user to authenticate.
// Once complete, the retrieved tokens are returned.
func (m *OAuthManager) LoginDevice(ctx context.Context, w io.Writer) (res oauth.TokenResult, err error) {
	state, err := m.api.GetDeviceCode(ctx, m.audience)
	if err != nil {
		return res, fmt.Errorf("login failed: %w", err)
	}

	if state.UserCode == "" {
		return res, errors.New("login failed: no user code returned")
	}

	_, _ = fmt.Fprintln(w, "\nYou will be signed in using a web-based login.")
	_, _ = fmt.Fprintln(w, "To sign in with credentials on the command line, use 'docker login -u <username>'")
	_, _ = fmt.Fprintf(w, "\nYour one-time device confirmation code is: %s\n", state.UserCode)
	_, _ = fmt.Fprint(w, "\nPress ENTER to open the browser.\n")
	_, _ = fmt.Fprintf(w, "Or open the URL manually: %s\n", strings.Split(state.VerificationURI, "?")[0])

	tokenResChan := make(chan api.TokenResponse)
	waitForTokenErrChan := make(chan error)
	go func() {
		tokenRes, err := m.api.WaitForDeviceToken(ctx, state)
		if err != nil {
			waitForTokenErrChan <- err
			return
		}
		tokenResChan <- tokenRes
	}()

	go func() {
		reader := bufio.NewReader(os.Stdin)
		reader.ReadString('\n')
		_ = m.openBrowser(state.VerificationURI)
	}()

	_, _ = fmt.Fprint(w, "\nWaiting for authentication in the browser...\n")
	var tokenRes api.TokenResponse
	select {
	case <-ctx.Done():
		return res, errors.New("login canceled")
	case err := <-waitForTokenErrChan:
		return res, fmt.Errorf("login failed: %w", err)
	case tokenRes = <-tokenResChan:
	}

	claims, err := oauth.GetClaims(tokenRes.AccessToken)
	if err != nil {
		return res, fmt.Errorf("login failed: %w", err)
	}

	res.Tenant = m.tenant
	res.AccessToken = tokenRes.AccessToken
	res.RefreshToken = tokenRes.RefreshToken
	res.Claims = claims

	return res, nil
}

// Logout revokes the provided refresh token with the oauth tenant. However, it
// does not end the user's session with the tenant.
func (m *OAuthManager) Logout(ctx context.Context, refreshToken string) error {
	return m.api.RevokeToken(ctx, refreshToken)
}

// RefreshToken uses the provided token to refresh access with the oauth
// tenant, returning new access and refresh token.
func (m OAuthManager) RefreshToken(ctx context.Context, refreshToken string) (res oauth.TokenResult, err error) {
	refreshRes, err := m.api.Refresh(ctx, refreshToken)
	if err != nil {
		return res, err
	}

	select {
	case <-ctx.Done():
		return res, ctx.Err()
	default:
	}

	claims, err := oauth.GetClaims(refreshRes.AccessToken)
	if err != nil {
		return res, err
	}

	res.Tenant = m.tenant
	res.AccessToken = refreshRes.AccessToken
	res.RefreshToken = refreshRes.RefreshToken
	res.Claims = claims
	return res, nil
}
