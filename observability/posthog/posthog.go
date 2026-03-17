package posthog

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/observability"
	ph "github.com/posthog/posthog-go"
)

// Config holds configuration for the PostHog observability provider.
type Config struct {
	APIKey   string
	Endpoint string
}

type provider struct {
	client ph.Client
}

// New creates a PostHog observability provider from the given Config.
func New(cfg Config, logger *slog.Logger) (observability.Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("posthog Config must contain an API key")
	}

	config := ph.Config{}

	if cfg.Endpoint != "" {
		config.Endpoint = cfg.Endpoint
	}

	client, err := ph.NewWithConfig(cfg.APIKey, config)
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
