package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
func GenerateToken(user *User, secret string, ttl time.Duration, scopes []string) (string, error) {
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

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("could not sign token: %w", err)
	}

	return signed, nil
}

// ValidateToken verifies a JWT and returns the user.
// Returns an error if the signature is invalid or the token has expired.
func ValidateToken(tokenString, secret string) (*User, error) {
	claims, err := validateTokenClaims(tokenString, secret)
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
func validateTokenClaims(tokenString, secret string) (*tokenClaims, error) {
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

	return claims, nil
}

// TokenValidator returns a function that validates tokens using the given secret.
// Suitable for passing to RequireAuth as the tokenValidator parameter.
func TokenValidator(secret string) func(string) (*User, error) {
	return func(token string) (*User, error) {
		return ValidateToken(token, secret)
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
