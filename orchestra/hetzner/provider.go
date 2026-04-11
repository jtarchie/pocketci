package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// Provider implements orchestra.DriverProvider for the Hetzner Cloud driver.
type Provider struct{}

// NewProvider returns a new Hetzner Cloud DriverProvider.
func NewProvider() *Provider { return &Provider{} }

// Name implements orchestra.DriverProvider.
func (p *Provider) Name() string { return "hetzner" }

// NewDriver implements orchestra.DriverProvider.
func (p *Provider) NewDriver(ctx context.Context, namespace string, cfg orchestra.DriverConfig, logger *slog.Logger) (orchestra.Driver, error) {
	var sc ServerConfig
	if cfg != nil {
		var ok bool
		sc, ok = cfg.(ServerConfig)
		if !ok {
			sc = ServerConfig{}
		}
	}

	return New(ctx, Config{ServerConfig: sc, Namespace: namespace}, logger)
}

// UnmarshalConfig implements orchestra.DriverProvider.
func (p *Provider) UnmarshalConfig(raw json.RawMessage) (orchestra.DriverConfig, error) {
	var cfg ServerConfig
	err := json.Unmarshal(raw, &cfg)
	if err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return cfg, nil
}

// EmptyConfig implements orchestra.DriverProvider.
func (p *Provider) EmptyConfig() orchestra.DriverConfig { return ServerConfig{} }
