//go:build !darwin

package vz

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// ServerConfig holds server-level configuration for the VZ driver.
type ServerConfig struct {
	Memory   string `json:"memory,omitempty"`
	CPUs     string `json:"cpus,omitempty"`
	CacheDir string `json:"cache_dir,omitempty"`
	Image    string `json:"image,omitempty"`
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "vz" }

// Config holds configuration for the VZ (Apple Virtualization) driver.
// On non-darwin platforms, the driver is not available.
type Config struct {
	ServerConfig
	Namespace string
}

// New returns an error on non-darwin platforms since Apple Virtualization
// framework is only available on macOS.
func New(_ context.Context, _ Config, _ *slog.Logger) (orchestra.Driver, error) {
	return nil, fmt.Errorf("vz driver is only available on macOS (darwin)")
}
