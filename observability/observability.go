package observability

import (
	"context"
	"fmt"
	"log/slog"
)

// Provider is the interface for observability backends.
// Implementations send logs and events to external services
// like PostHog or Honeybadger.
type Provider interface {
	// Name returns a stable identifier for the provider, e.g. "posthog".
	Name() string

	// Event sends a structured event to the observability backend.
	Event(eventType string, data map[string]any) error

	// SlogHandler wraps a base slog.Handler to forward log records
	// to the observability backend in addition to normal logging.
	SlogHandler(next slog.Handler) slog.Handler

	// Close flushes pending data and releases resources.
	Close() error
}

// TeeHandler is a slog.Handler that fans out log records to two handlers.
// This is useful when a provider's slog handler doesn't wrap a base handler
// (e.g., Honeybadger's slog handler is standalone).
type TeeHandler struct {
	primary   slog.Handler
	secondary slog.Handler
}

// NewTeeHandler creates a handler that forwards records to both handlers.
func NewTeeHandler(primary, secondary slog.Handler) *TeeHandler {
	return &TeeHandler{primary: primary, secondary: secondary}
}

func (t *TeeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.primary.Enabled(ctx, level) || t.secondary.Enabled(ctx, level)
}

func (t *TeeHandler) Handle(ctx context.Context, r slog.Record) error {
	if t.primary.Enabled(ctx, r.Level) {
		err := t.primary.Handle(ctx, r)
		if err != nil {
			return fmt.Errorf("handle: %w", err)
		}
	}

	if t.secondary.Enabled(ctx, r.Level) {
		_ = t.secondary.Handle(ctx, r)
	}

	return nil
}

func (t *TeeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TeeHandler{
		primary:   t.primary.WithAttrs(attrs),
		secondary: t.secondary.WithAttrs(attrs),
	}
}

func (t *TeeHandler) WithGroup(name string) slog.Handler {
	return &TeeHandler{
		primary:   t.primary.WithGroup(name),
		secondary: t.secondary.WithGroup(name),
	}
}
