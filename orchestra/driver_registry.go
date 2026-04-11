package orchestra

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// DriverProvider knows how to create a single type of Driver.
// Implement this interface in each driver package and list all providers
// explicitly at the call site (analogous to WebhookProviders).
type DriverProvider interface {
	// Name returns the driver identifier (e.g. "docker", "native").
	Name() string
	// NewDriver creates a driver for the given namespace using cfg.
	// cfg is the typed ServerConfig for this driver; it may be nil if no config was supplied.
	NewDriver(ctx context.Context, namespace string, cfg DriverConfig, logger *slog.Logger) (Driver, error)
	// UnmarshalConfig deserialises raw JSON into this driver's typed DriverConfig.
	UnmarshalConfig(raw json.RawMessage) (DriverConfig, error)
	// EmptyConfig returns the zero-value DriverConfig for this driver.
	EmptyConfig() DriverConfig
}

// DriverRegistry maps driver names to their DriverProvider.
// Build one with NewDriverRegistry and pass it wherever driver creation is needed.
type DriverRegistry struct {
	byName map[string]DriverProvider
}

// NewDriverRegistry builds a DriverRegistry from an explicit list of providers.
// Panics on duplicate names.
func NewDriverRegistry(providers []DriverProvider) *DriverRegistry {
	m := make(map[string]DriverProvider, len(providers))

	for _, p := range providers {
		name := p.Name()
		if _, exists := m[name]; exists {
			panic(fmt.Sprintf("driver provider %q already registered", name))
		}

		m[name] = p
	}

	return &DriverRegistry{byName: m}
}

// CreateDriver creates a driver instance for the given name, namespace, and config.
// If cfg is nil the provider's EmptyConfig is used.
func (r *DriverRegistry) CreateDriver(ctx context.Context, name, namespace string, cfg DriverConfig, logger *slog.Logger) (Driver, error) {
	p, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", name)
	}

	if cfg == nil {
		cfg = p.EmptyConfig()
	}

	driver, err := p.NewDriver(ctx, namespace, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("create %q driver: %w", name, err)
	}

	return driver, nil
}

// UnmarshalConfig deserialises raw JSON into the typed config for the named driver.
func (r *DriverRegistry) UnmarshalConfig(name string, raw json.RawMessage) (DriverConfig, error) {
	p, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", name)
	}

	cfg, err := p.UnmarshalConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("unmarshal %q driver config: %w", name, err)
	}

	return cfg, nil
}

// Names returns all registered driver names.
func (r *DriverRegistry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}

	return names
}
