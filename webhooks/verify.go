package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HexHMACSHA256 returns hex(hmac-sha256(secret, payload)). The returned string
// is the lowercase hex digest, matching how every supported provider encodes
// its signatures.
func HexHMACSHA256(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)

	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHexHMACSHA256 reports whether signature equals the hex HMAC-SHA256 of
// payload under secret. Comparison is constant-time.
func VerifyHexHMACSHA256(payload, secret []byte, signature string) bool {
	expected := HexHMACSHA256(payload, secret)

	return hmac.Equal([]byte(signature), []byte(expected))
}

// VerifyHexHMACSHA256Prefixed checks that sigHeader has the given prefix and
// that the remainder matches the hex HMAC-SHA256 of payload under secret.
// Returns false if the prefix is missing.
func VerifyHexHMACSHA256Prefixed(payload, secret []byte, sigHeader, prefix string) bool {
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}

	return VerifyHexHMACSHA256(payload, secret, strings.TrimPrefix(sigHeader, prefix))
}
