package posthog

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/jtarchie/pocketci/observability"
	ph "github.com/posthog/posthog-go"
)

func init() {
	observability.Register("posthog", New)
}

type provider struct {
	client ph.Client
}

// New creates a PostHog observability provider.
// DSN format: "posthog://API_KEY" or "posthog://API_KEY?endpoint=https://us.i.posthog.com"
func New(dsn string, logger *slog.Logger) (observability.Provider, error) {
	uri, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("could not parse posthog DSN: %w", err)
	}

	apiKey := uri.Host
	if apiKey == "" {
		return nil, fmt.Errorf("posthog DSN must contain an API key (e.g., posthog://phc_abc123)")
	}

	config := ph.Config{}

	if endpoint := uri.Query().Get("endpoint"); endpoint != "" {
		config.Endpoint = endpoint
	}

	client, err := ph.NewWithConfig(apiKey, config)
	if err != nil {
		return nil, fmt.Errorf("could not create posthog client: %w", err)
	}

	return &provider{client: client}, nil
}

func (p *provider) Name() string {
	return "posthog"
}

func (p *provider) Event(eventType string, data map[string]any) error {
	props := ph.NewProperties()
	for k, v := range data {
		props.Set(k, v)
	}

	return p.client.Enqueue(ph.Capture{
		DistinctId: "pocketci-server",
		Event:      eventType,
		Properties: props,
	})
}

func (p *provider) SlogHandler(next slog.Handler) slog.Handler {
	return ph.NewSlogCaptureHandler(next, p.client,
		ph.WithMinCaptureLevel(slog.LevelWarn),
		ph.WithDistinctIDFn(func(_ context.Context, _ slog.Record) string {
			return "pocketci-server"
		}),
	)
}

func (p *provider) Close() error {
	return p.client.Close()
}
