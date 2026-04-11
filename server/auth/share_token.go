package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultShareTokenTTL = 30 * 24 * time.Hour // 30 days

// ShareClaims holds the decoded payload of a validated share token.
type ShareClaims struct {
	RunID string
}

// GenerateShareToken creates a tamper-proof token binding the given runID with a 30-day expiry.
// Format: "<runID>.<expiresUnix>.<hex-HMAC-SHA256(secret, "share:"+runID+":"+expiresUnix)>"
func GenerateShareToken(runID, secret string) (string, error) {
	return GenerateShareTokenWithTTL(runID, secret, defaultShareTokenTTL)
}

// GenerateShareTokenWithTTL creates a share token with a configurable TTL.
func GenerateShareTokenWithTTL(runID, secret string, ttl time.Duration) (string, error) {
	if runID == "" {
		return "", errors.New("runID must not be empty")
	}

	expires := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)

	mac := hmac.New(sha256.New, []byte(secret))
	_, macErr := mac.Write([]byte("share:" + runID + ":" + expires))
	if macErr != nil {
		return "", fmt.Errorf("could not compute HMAC: %w", macErr)
	}

	sig := hex.EncodeToString(mac.Sum(nil))

	return runID + "." + expires + "." + sig, nil
}

// ValidateShareToken verifies a share token and returns the ShareClaims if valid.
// Returns an error if the token is malformed, the HMAC does not match, or the token has expired.
func ValidateShareToken(token, secret string) (*ShareClaims, error) {
	// Format: <runID>.<expiresUnix>.<sig>
	// runID may not contain dots (UUIDs use hyphens only), but be defensive and split from the right.
	lastDot := strings.LastIndex(token, ".")
	if lastDot < 1 {
		return nil, errors.New("malformed share token")
	}

	sigHex := token[lastDot+1:]

	rest := token[:lastDot]
	midDot := strings.LastIndex(rest, ".")
	if midDot < 1 {
		return nil, errors.New("malformed share token")
	}

	runID := rest[:midDot]
	expires := rest[midDot+1:]

	if runID == "" || expires == "" || sigHex == "" {
		return nil, errors.New("malformed share token")
	}

	expiresAt, err := strconv.ParseInt(expires, 10, 64)
	if err != nil {
		return nil, errors.New("malformed share token expiry")
	}

	if time.Now().Unix() > expiresAt {
		return nil, errors.New("share token expired")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, macErr := mac.Write([]byte("share:" + runID + ":" + expires))
	if macErr != nil {
		return nil, fmt.Errorf("could not compute HMAC: %w", macErr)
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
