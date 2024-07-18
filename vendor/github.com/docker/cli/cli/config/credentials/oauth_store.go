package credentials

import (
	"context"
	"errors"
	"time"

	"github.com/docker/cli/cli/config/types"
	"github.com/docker/cli/cli/oauth"
	"github.com/docker/docker/registry"
)

// oauthStore wraps an existing store that transparently handles oauth
// flows, managing authentication/token refresh and piggybacking off an
// existing store for storage/retrieval.
type oauthStore struct {
	backingStore Store
	manager      oauth.Manager
}

// NewOAuthStore creates a new oauthStore backed by the provided store.
func NewOAuthStore(backingStore Store, manager oauth.Manager) Store {
	return &oauthStore{
		backingStore: backingStore,
		manager:      manager,
	}
}

const minimumTokenLifetime = 50 * time.Minute

// Get retrieves the credentials from the backing store, refreshing the
// access token if the stored credentials are valid for less than minimumTokenLifetime.
// If the credentials being retrieved are not for the official registry, they are
// returned as is. If the credentials retrieved do not parse as a token, they are
// also returned as is.
func (o *oauthStore) Get(serverAddress string) (types.AuthConfig, error) {
	if serverAddress != registry.IndexServer {
		return o.backingStore.Get(serverAddress)
	}

	auth, err := o.backingStore.Get(serverAddress)
	if err != nil {
		// If an error happens here, it's not due to the backing store not
		// containing credentials, but rather an actual issue with the backing
		// store itself. This should be propagated up.
		return types.AuthConfig{}, err
	}

	tokenRes, err := o.parseToken(auth.Password)
	// if the password is not a token, return the auth config as is
	if err != nil {
		//nolint:nilerr
		return auth, nil
	}

	// if the access token is valid for less than minimumTokenLifetime, refresh it
	if tokenRes.RefreshToken != "" && tokenRes.Claims.Expiry.Time().Before(time.Now().Add(minimumTokenLifetime)) {
		// todo(laurazard): should use a context with a timeout here?
		refreshRes, err := o.manager.RefreshToken(context.TODO(), tokenRes.RefreshToken)
		if err != nil {
			return types.AuthConfig{}, err
		}
		tokenRes = refreshRes
	}

	err = o.storeInBackingStore(tokenRes)
	if err != nil {
		return types.AuthConfig{}, err
	}

	return types.AuthConfig{
		Username:      tokenRes.Claims.Domain.Username,
		Password:      tokenRes.AccessToken,
		Email:         tokenRes.Claims.Domain.Email,
		ServerAddress: registry.IndexServer,
	}, nil
}

// GetAll returns a map of all credentials in the backing store. If the backing
// store contains credentials for the official registry, these are refreshed/processed
// according to the same rules as Get.
func (o *oauthStore) GetAll() (map[string]types.AuthConfig, error) {
	allAuths, err := o.backingStore.GetAll()
	if err != nil {
		return nil, err
	}

	if _, ok := allAuths[registry.IndexServer]; !ok {
		return allAuths, nil
	}

	auth, err := o.Get(registry.IndexServer)
	if err != nil {
		return nil, err
	}
	allAuths[registry.IndexServer] = auth
	return allAuths, err
}

// Erase removes the credentials from the backing store, logging out of the
// tenant if running
func (o *oauthStore) Erase(serverAddress string) error {
	if serverAddress == registry.IndexServer {
		auth, err := o.backingStore.Get(registry.IndexServer)
		if err != nil {
			return err
		}
		if tokenRes, err := o.parseToken(auth.Password); err == nil {
			// todo(laurazard): should use a context with a timeout here?
			_ = o.manager.Logout(context.TODO(), tokenRes.RefreshToken)
		}
	}
	return o.backingStore.Erase(serverAddress)
}

// Store stores the provided credentials in the backing store, without any
// additional processing.
func (o *oauthStore) Store(auth types.AuthConfig) error {
	return o.backingStore.Store(auth)
}

func (o *oauthStore) parseToken(password string) (oauth.TokenResult, error) {
	accessToken, refreshToken, err := oauth.SplitTokens(password)
	if err != nil {
		return oauth.TokenResult{}, errors.New("failed to parse token")
	}
	claims, err := oauth.GetClaims(accessToken)
	if err != nil {
		return oauth.TokenResult{}, err
	}
	return oauth.TokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Claims:       claims,
	}, nil
}

func (o *oauthStore) storeInBackingStore(tokenRes oauth.TokenResult) error {
	auth := types.AuthConfig{
		Username:      tokenRes.Claims.Domain.Username,
		Password:      oauth.ConcatTokens(tokenRes.AccessToken, tokenRes.RefreshToken),
		Email:         tokenRes.Claims.Domain.Email,
		ServerAddress: registry.IndexServer,
	}
	return o.backingStore.Store(auth)
}

func (o *oauthStore) GetFilename() string {
	if fileStore, ok := o.backingStore.(*fileStore); ok {
		return fileStore.GetFilename()
	}
	return ""
}

func (o *oauthStore) IsFileStore() bool {
	if fileStore, ok := o.backingStore.(*fileStore); ok {
		return fileStore.IsFileStore()
	}
	return false
}
