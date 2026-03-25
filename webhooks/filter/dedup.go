package filter

import (
	"crypto/sha256"
	"fmt"
)

// DedupKeyHash evaluates a string expression against the webhook environment
// and returns a truncated SHA-256 hash (16 bytes) of the result.
// Returns (nil, nil) when expression is empty or evaluates to an empty string.
func DedupKeyHash(expression string, env WebhookEnv) ([]byte, error) {
	if expression == "" {
		return nil, nil
	}

	key, err := EvaluateString(expression, env)
	if err != nil {
		return nil, fmt.Errorf("dedup_key evaluation: %w", err)
	}

	if key == "" {
		return nil, nil
	}

	h := sha256.Sum256([]byte(key))

	return h[:16], nil
}
