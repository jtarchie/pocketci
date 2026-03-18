package secrets

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a requested secret does not exist.
var ErrNotFound = errors.New("secret not found")

// Manager is the interface for secret storage backends.
// Implementations must encrypt secrets at rest and ensure
// values are inaccessible from the underlying storage mechanism.
type Manager interface {
	// Get retrieves a secret by scope and key.
	// Scope is "global" or "pipeline/{pipelineID}".
	// Returns ErrNotFound if the secret does not exist.
	Get(ctx context.Context, scope string, key string) (string, error)

	// Set stores or updates a secret.
	// If the secret already exists, the old value is overwritten (not retained).
	Set(ctx context.Context, scope string, key string, value string) error

	// Delete removes a secret.
	// Returns ErrNotFound if the secret does not exist.
	Delete(ctx context.Context, scope string, key string) error

	// ListByScope returns all secret keys in the given scope.
	ListByScope(ctx context.Context, scope string) ([]string, error)

	// DeleteByScope removes all secrets in the given scope.
	DeleteByScope(ctx context.Context, scope string) error

	// Close releases any resources held by the manager.
	Close() error
}

// PipelineScope returns the scope string for a pipeline.
func PipelineScope(pipelineID string) string {
	return "pipeline/" + pipelineID
}

// GlobalScope is the scope for secrets shared across all pipelines.
const GlobalScope = "global"

// systemManagedKeys are secret keys reserved for internal use by the system.
// These keys are managed through dedicated API fields (e.g., driver via
// Driver, webhook_secret via WebhookSecret) and must not be set or read
// through user-facing secret mechanisms.
var systemManagedKeys = map[string]struct{}{
	"driver":         {},
	"webhook_secret": {},
}

// IsSystemKey reports whether the given key is reserved for internal system use.
func IsSystemKey(key string) bool {
	_, ok := systemManagedKeys[key]
	return ok
}
