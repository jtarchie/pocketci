package native

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// Provider implements orchestra.DriverProvider for the Native driver.
type Provider struct{}

// NewProvider returns a new Native DriverProvider.
func NewProvider() *Provider { return &Provider{} }

// Name implements orchestra.DriverProvider.
func (p *Provider) Name() string { return "native" }

// NewDriver implements orchestra.DriverProvider.
func (p *Provider) NewDriver(ctx context.Context, namespace string, _ orchestra.DriverConfig, logger *slog.Logger) (orchestra.Driver, error) {
	return New(ctx, Config{Namespace: namespace}, logger)
}

// UnmarshalConfig implements orchestra.DriverProvider.
func (p *Provider) UnmarshalConfig(_ json.RawMessage) (orchestra.DriverConfig, error) {
	return ServerConfig{}, nil
}

// EmptyConfig implements orchestra.DriverProvider.
func (p *Provider) EmptyConfig() orchestra.DriverConfig { return ServerConfig{} }
