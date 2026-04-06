package honeybadger

import (
	"errors"
	"fmt"
	"log/slog"

	hb "github.com/honeybadger-io/honeybadger-go"
	hbslog "github.com/honeybadger-io/honeybadger-go/slog"
	"github.com/jtarchie/pocketci/observability"
)

// Config holds configuration for the Honeybadger observability provider.
type Config struct {
	APIKey string
	Env    string
}

type provider struct {
	client *hb.Client
}

// New creates a Honeybadger observability provider from the given Config.
func New(cfg Config, logger *slog.Logger) (observability.Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("honeybadger Config must contain an API key")
	}

	config := hb.Configuration{
		APIKey: cfg.APIKey,
	}

	if cfg.Env != "" {
		config.Env = cfg.Env
	}

	client := hb.New(config)

	return &provider{client: client}, nil
}

func (p *provider) Name() string {
	return "honeybadger"
}

func (p *provider) Event(eventType string, data map[string]any) error {
	err := p.client.Event(eventType, data)
	if err != nil {
		return fmt.Errorf("event: %w", err)
	}

	return nil
}

func (p *provider) SlogHandler(next slog.Handler) slog.Handler {
	hbHandler := hbslog.New(p.client)

	return observability.NewTeeHandler(next, hbHandler)
}

func (p *provider) Close() error {
	p.client.Flush()

	return nil
}
