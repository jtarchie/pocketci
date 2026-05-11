package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Audience values identify the surface a JWT was minted for. Binding tokens
// to an audience prevents a token minted for one surface (e.g. read-only
// MCP) from authenticating against another (e.g. the write-capable
// /api/*). See PCI-SEC-MCP-001.
const (
	AudienceAPI = "api" // CLI device-flow tokens; authenticate /api/* routes.
	AudienceMCP = "mcp" // OAuth-issued tokens for the MCP server.
)

// tokenClaims extends jwt.RegisteredClaims with user-specific fields.
type tokenClaims struct {
	jwt.RegisteredClaims
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	NickName string   `json:"nick_name"`
	Provider string   `json:"provider"`
	Scopes   []string `json:"scopes,omitempty"`
}

// GenerateToken creates a signed JWT for the given user.
// If scopes is non-nil, the token includes scope claims (e.g., ["ci:read"]).
// audience binds the token to a single surface (AudienceAPI / AudienceMCP);
// pass an empty string only for legacy callers that pre-date audience
// binding.
func GenerateToken(user *User, secret string, ttl time.Duration, scopes []string, audience string) (string, error) {
	now := time.Now()
	claims := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.UserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "pocketci",
		},
		Email:    user.Email,
		Name:     user.Name,
		NickName: user.NickName,
		Provider: user.Provider,
		Scopes:   scopes,
	}

	if audience != "" {
		claims.Audience = jwt.ClaimStrings{audience}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("could not sign token: %w", err)
	}

	return signed, nil
}

// ValidateToken verifies a JWT and returns the user.
// Returns an error if the signature is invalid or the token has expired.
//
// expectedAudience pins the surface this token is allowed to authenticate
// against. If allowMissingAudience is true, a token without any aud claim
// is also accepted (used for legacy CLI tokens minted before audience
// binding shipped). Tokens whose aud is set but does not match
// expectedAudience are always rejected.
//
// Pass expectedAudience="" to skip audience validation entirely; that path
// is for tests and code that pre-dates this fix only.
func ValidateToken(tokenString, secret, expectedAudience string, allowMissingAudience bool) (*User, error) {
	claims, err := validateTokenClaims(tokenString, secret, expectedAudience, allowMissingAudience)
	if err != nil {
		return nil, err
	}

	return &User{
		Email:    claims.Email,
		Name:     claims.Name,
		NickName: claims.NickName,
		Provider: claims.Provider,
		UserID:   claims.Subject,
		Scopes:   claims.Scopes,
	}, nil
}

// validateTokenClaims parses and validates a JWT, returning the full claims.
func validateTokenClaims(tokenString, secret, expectedAudience string, allowMissingAudience bool) (*tokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &tokenClaims{}, func(_ *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, errors.New("token expired")
		}

		return nil, errors.New("invalid token")
	}

	claims, ok := token.Claims.(*tokenClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}

	if expectedAudience != "" {
		if len(claims.Audience) == 0 {
			if !allowMissingAudience {
				return nil, errors.New("invalid token: missing audience")
			}
		} else if !slices.Contains(claims.Audience, expectedAudience) {
			return nil, errors.New("invalid token: wrong audience")
		}
	}

	return claims, nil
}

// TokenValidator returns a validator suitable for /api/*. It requires the
// token to carry aud=api OR (for backward compatibility with legacy CLI
// tokens minted before audience binding shipped) no audience claim at all.
// Tokens whose audience is anything else -- crucially, MCP tokens with
// aud=mcp -- are rejected.
//
// Once every active deployment has rolled forward, the lenient branch
// should be removed; tracked under the PCI-SEC-AUTH/MCP follow-ups.
func TokenValidator(secret string) func(string) (*User, error) {
	return func(token string) (*User, error) {
		return ValidateToken(token, secret, AudienceAPI, true)
	}
}

// generateRandomCode creates a cryptographically random hex string for CLI device flow.
func generateRandomCode() (string, error) {
	b := make([]byte, 16)
	_, randErr := rand.Read(b)
	if randErr != nil {
		return "", fmt.Errorf("rand read: %w", randErr)
	}

	return hex.EncodeToString(b), nil
}
