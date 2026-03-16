package honeybadger

import (
	"fmt"
	"log/slog"
	"net/url"

	hb "github.com/honeybadger-io/honeybadger-go"
	hbslog "github.com/honeybadger-io/honeybadger-go/slog"
	"github.com/jtarchie/pocketci/observability"
)

func init() {
	observability.Register("honeybadger", New)
}

type provider struct {
	client *hb.Client
}

// New creates a Honeybadger observability provider.
// DSN format: "honeybadger://API_KEY" or "honeybadger://API_KEY?env=production"
func New(dsn string, logger *slog.Logger) (observability.Provider, error) {
	uri, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("could not parse honeybadger DSN: %w", err)
	}

	apiKey := uri.Host
	if apiKey == "" {
		return nil, fmt.Errorf("honeybadger DSN must contain an API key (e.g., honeybadger://hbp_abc123)")
	}

	config := hb.Configuration{
		APIKey: apiKey,
	}

	if env := uri.Query().Get("env"); env != "" {
		config.Env = env
	}

	client := hb.New(config)

	return &provider{client: client}, nil
}

func (p *provider) Name() string {
	return "honeybadger"
}

func (p *provider) Event(eventType string, data map[string]any) error {
	return p.client.Event(eventType, data)
}

func (p *provider) SlogHandler(next slog.Handler) slog.Handler {
	hbHandler := hbslog.New(p.client)

	return observability.NewTeeHandler(next, hbHandler)
}

func (p *provider) Close() error {
	p.client.Flush()

	return nil
}
