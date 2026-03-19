package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ShareClaims holds the decoded payload of a validated share token.
type ShareClaims struct {
	RunID string
}

// GenerateShareToken creates a tamper-proof token binding the given runID.
// Format: "<runID>.<hex-HMAC-SHA256(secret, "share:"+runID)>"
// Tokens have no expiry — they are valid as long as the signing secret is unchanged.
func GenerateShareToken(runID, secret string) (string, error) {
	if runID == "" {
		return "", errors.New("runID must not be empty")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte("share:" + runID)); err != nil {
		return "", fmt.Errorf("could not compute HMAC: %w", err)
	}

	sig := hex.EncodeToString(mac.Sum(nil))

	return runID + "." + sig, nil
}

// ValidateShareToken verifies a share token and returns the ShareClaims if valid.
// Returns an error if the token is malformed or the HMAC does not match.
func ValidateShareToken(token, secret string) (*ShareClaims, error) {
	// Find the last dot — runID itself may contain dots (UUIDs use hyphens, but be safe).
	idx := strings.LastIndex(token, ".")
	if idx < 1 {
		return nil, errors.New("malformed share token")
	}

	runID := token[:idx]
	sigHex := token[idx+1:]

	if runID == "" || sigHex == "" {
		return nil, errors.New("malformed share token")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte("share:" + runID)); err != nil {
		return nil, fmt.Errorf("could not compute HMAC: %w", err)
	}

	expected, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, errors.New("malformed share token signature")
	}

	if !hmac.Equal(mac.Sum(nil), expected) {
		return nil, errors.New("invalid share token")
	}

	return &ShareClaims{RunID: runID}, nil
}
