package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/docker/cli/cli/version"
)

type OAuthAPI interface {
	GetDeviceCode(ctx context.Context, audience string) (State, error)
	WaitForDeviceToken(ctx context.Context, state State) (TokenResponse, error)
	Refresh(ctx context.Context, token string) (TokenResponse, error)
	RevokeToken(ctx context.Context, refreshToken string) error
}

// API represents API interactions with Auth0.
type API struct {
	// BaseURL is the base used for each request to Auth0.
	BaseURL string
	// ClientID is the client ID for the application to auth with the tenant.
	ClientID string
	// Scopes are the scopes that are requested during the device auth flow.
	Scopes []string
}

// TokenResponse represents the response of the /oauth/token route.
type TokenResponse struct {
	AccessToken      string  `json:"access_token"`
	IDToken          string  `json:"id_token"`
	RefreshToken     string  `json:"refresh_token"`
	Scope            string  `json:"scope"`
	ExpiresIn        int     `json:"expires_in"`
	TokenType        string  `json:"token_type"`
	Error            *string `json:"error,omitempty"`
	ErrorDescription string  `json:"error_description,omitempty"`
}

var ErrTimeout = errors.New("timed out waiting for device token")

// GetDeviceCode initiates the device-code auth flow with the tenant.
// The state returned contains the device code that the user must use to
// authenticate, as well as the URL to visit, etc.
func (a API) GetDeviceCode(ctx context.Context, audience string) (state State, err error) {
	data := url.Values{
		"client_id": {a.ClientID},
		"audience":  {audience},
		"scope":     {strings.Join(a.Scopes, " ")},
	}

	deviceCodeURL := a.BaseURL + "/oauth/device/code"
	resp, err := postForm(ctx, deviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		if errorDescription, ok := body["error_description"].(string); ok {
			return state, errors.New(errorDescription)
		}
		return state, fmt.Errorf("failed to get device code: %w", err)
	}

	err = json.NewDecoder(resp.Body).Decode(&state)

	return
}

// WaitForDeviceToken polls the tenant to get access/refresh tokens for the user.
// This should be called after GetDeviceCode, and will block until the user has
// authenticated or we have reached the time limit for authenticating (based on
// the response from GetDeviceCode).
func (a API) WaitForDeviceToken(ctx context.Context, state State) (TokenResponse, error) {
	ticker := time.NewTicker(state.IntervalDuration())
	timeout := time.After(time.Duration(state.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return TokenResponse{}, ctx.Err()
		case <-ticker.C:
			res, err := a.getDeviceToken(ctx, state)
			if err != nil {
				return res, err
			}

			if res.Error != nil {
				if *res.Error == "authorization_pending" {
					continue
				}

				return res, errors.New(res.ErrorDescription)
			}

			return res, nil
		case <-timeout:
			ticker.Stop()
			return TokenResponse{}, ErrTimeout
		}
	}
}

// getToken calls the token endpoint of Auth0 and returns the response.
func (a API) getDeviceToken(ctx context.Context, state State) (res TokenResponse, err error) {
	data := url.Values{
		"client_id":   {a.ClientID},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {state.DeviceCode},
	}
	oauthTokenURL := a.BaseURL + "/oauth/token"

	resp, err := postForm(ctx, oauthTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return res, fmt.Errorf("failed to get code: %w", err)
	}

	err = json.NewDecoder(resp.Body).Decode(&res)
	_ = resp.Body.Close()

	return
}

// Refresh fetches new tokens using the refresh token.
func (a API) Refresh(ctx context.Context, token string) (res TokenResponse, err error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {a.ClientID},
		"refresh_token": {token},
	}

	refreshURL := a.BaseURL + "/oauth/token"
	resp, err := postForm(ctx, refreshURL, strings.NewReader(data.Encode()))
	if err != nil {
		return
	}
	defer resp.Body.Close()

	err = json.NewDecoder(resp.Body).Decode(&res)
	return
}

// RevokeToken revokes a refresh token with the tenant so that it can no longer
// be used to get new tokens.
func (a API) RevokeToken(ctx context.Context, refreshToken string) error {
	data := url.Values{
		"client_id": {a.ClientID},
		"token":     {refreshToken},
	}

	revokeURL := a.BaseURL + "/oauth/revoke"
	resp, err := postForm(ctx, revokeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("failed to revoke token")
	}
	return nil
}

func postForm(ctx context.Context, reqURL string, data io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, data)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cliVersion := strings.ReplaceAll(version.Version, ".", "_")
	req.Header.Set("User-Agent", fmt.Sprintf("docker-cli:%s:%s-%s", cliVersion, runtime.GOOS, runtime.GOARCH))

	return http.DefaultClient.Do(req)
}
