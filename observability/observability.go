package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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

// InitFunc is the constructor function for an observability provider.
type InitFunc func(dsn string, logger *slog.Logger) (Provider, error)

var drivers = map[string]InitFunc{}

// Register adds an observability provider by name.
// Called from init() in provider packages.
func Register(name string, init InitFunc) {
	drivers[name] = init
}

// New creates a new Provider from the named backend and DSN.
func New(name string, dsn string, logger *slog.Logger) (Provider, error) {
	init, ok := drivers[name]
	if !ok {
		available := make([]string, 0, len(drivers))
		for k := range drivers {
			available = append(available, k)
		}

		return nil, fmt.Errorf("unknown observability provider %q (available: %v): %w", name, available, errors.ErrUnsupported)
	}

	return init(dsn, logger)
}

// GetFromDSN extracts the provider name from the DSN scheme and creates a Provider.
// The DSN format is "<provider>://<api-key>?<params>",
// e.g. "posthog://phc_abc123?endpoint=https://us.i.posthog.com".
func GetFromDSN(dsn string, logger *slog.Logger) (Provider, error) {
	uri, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("could not parse observability DSN: %w", err)
	}

	return New(uri.Scheme, dsn, logger)
}

// Each iterates over all registered providers.
func Each(f func(string, InitFunc)) {
	for name, init := range drivers {
		f(name, init)
	}
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
		if err := t.primary.Handle(ctx, r); err != nil {
			return err
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
