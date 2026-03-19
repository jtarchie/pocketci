package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jtarchie/pocketci/secrets"
)

const shareSigningKeyName = "share_link_signing_key"

var (
	fallbackShareSecret     string
	fallbackShareSecretOnce sync.Once
)

// resolveShareSigningSecret returns the HMAC signing secret for share tokens.
// It persists the secret in the secrets manager under the global scope so that
// share links survive server restarts. If no secrets manager is configured, a
// random secret is generated once per process lifetime (links break on restart).
func resolveShareSigningSecret(ctx context.Context, mgr secrets.Manager, logger *slog.Logger) (string, error) {
	if mgr == nil {
		fallbackShareSecretOnce.Do(func() {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				panic(fmt.Sprintf("could not generate share signing secret: %v", err))
			}

			fallbackShareSecret = hex.EncodeToString(b)
		})

		logger.Warn("share.signing.secret.ephemeral",
			slog.String("reason", "no secrets manager configured — share links will be invalidated on server restart"),
		)

		return fallbackShareSecret, nil
	}

	existing, err := mgr.Get(ctx, secrets.GlobalScope, shareSigningKeyName)
	if err == nil {
		return existing, nil
	}

	if !errors.Is(err, secrets.ErrNotFound) {
		return "", fmt.Errorf("could not read share signing key: %w", err)
	}

	// Generate and persist a new key.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate share signing key: %w", err)
	}

	key := hex.EncodeToString(b)

	if err := mgr.Set(ctx, secrets.GlobalScope, shareSigningKeyName, key); err != nil {
		return "", fmt.Errorf("could not persist share signing key: %w", err)
	}

	return key, nil
}

// collectSecretValues retrieves all decrypted secret values for the global scope
// and the given pipeline scope. System-managed keys are excluded.
func collectSecretValues(ctx context.Context, mgr secrets.Manager, pipelineID string) []string {
	if mgr == nil {
		return nil
	}

	var values []string

	for _, scope := range []string{secrets.GlobalScope, secrets.PipelineScope(pipelineID)} {
		keys, err := mgr.ListByScope(ctx, scope)
		if err != nil {
			continue
		}

		for _, key := range keys {
			if secrets.IsSystemKey(key) {
				continue
			}

			val, err := mgr.Get(ctx, scope, key)
			if err != nil {
				continue
			}

			values = append(values, val)
		}
	}

	return values
}
