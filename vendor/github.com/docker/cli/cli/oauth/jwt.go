package oauth

import (
	"errors"
	"strings"

	"github.com/go-jose/go-jose/v3/jwt"
)

// Claims represents standard claims along with some custom ones.
type Claims struct {
	jwt.Claims

	// Domain is the domain claims for the token.
	Domain DomainClaims `json:"https://hub.docker.com"`

	// Deprecated: Email will be removed in favor of DomainClaims.Email in the
	// future.
	Email string `json:"email,omitempty"`

	// Scope is the scopes for the claims as a string that is space delimited.
	Scope string `json:"scope,omitempty"`

	// Deprecated: SessionID should be removed as we shouldn't have sessions.
	// For now, we cannot remove this or tutum-app will break. In the future,
	// we'll assign session IDs and put them on the claims `jti` field.
	SessionID string `json:"session_id,omitempty"`

	// Deprecated: Source will be removed in favor of DomainClaims.Source in the
	// future.
	Source *Source `json:"source,omitempty"`

	// Deprecated: Username will be removed in favor of DomainClaims.Username in
	// the future.
	Username string `json:"username,omitempty"`

	// Deprecated: UserID will be removed as soon as we can.
	UserID string `json:"user_id,omitempty"`
}

// DomainClaims represents a custom claim data set that doesn't change the spec
// payload. This is primarily introduced by Auth0 and is defined by a fully
// specified URL as it's key. e.g. "https://hub.docker.com"
type DomainClaims struct {
	// UUID is the user, machine client, or organization's UUID in our database.
	UUID string `json:"uuid"`

	// Roles is a list of roles as strings that the user has. e.g. "admin"
	//
	// Deprecated: This is no longer used and will be removed in the new
	// version.
	Roles []string `json:"roles"`

	// Email is the user's email address.
	Email string `json:"email"`

	// Username is the user's username.
	Username string `json:"username"`

	// Source is the source of the JWT. This should look like
	// `docker_{type}|{id}`.
	Source string `json:"source"`

	// SessionID is the unique ID of the token.
	SessionID string `json:"session_id"`

	// ClientID is the client_id that generated the token. This is filled if
	// M2M.
	ClientID string `json:"client_id,omitempty"`

	// ClientName is the name of the client that generated the token. This is
	// filled if M2M.
	ClientName string `json:"client_name,omitempty"`
}

// Source represents a source of a JWT.
type Source struct {
	// Type is the type of source. This could be "pat" etc.
	Type string `json:"type"`

	// ID is the identifier to the source type. If "pat" then this will be the
	// ID of the PAT.
	ID string `json:"id"`
}

// ParseSigned parses a JWT and returns the signature object or error. This does
// not verify the validity of the JWT.
func ParseSigned(token string) (*jwt.JSONWebToken, error) {
	return jwt.ParseSigned(token)
}

// GetClaims returns claims from an access token without verification.
func GetClaims(accessToken string) (claims Claims, err error) {
	token, err := ParseSigned(accessToken)
	if err != nil {
		return
	}

	err = token.UnsafeClaimsWithoutVerification(&claims)

	return
}

func ConcatTokens(accessToken, refreshToken string) string {
	return accessToken + ".." + refreshToken
}

func SplitTokens(token string) (accessToken, refreshToken string, err error) {
	parts := strings.Split(token, "..")
	if len(parts) != 2 {
		return "", "", errors.New("failed to parse token")
	}

	return parts[0], parts[1], nil
}
