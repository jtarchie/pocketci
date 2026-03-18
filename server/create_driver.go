package server

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/digitalocean"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/orchestra/qemu"
	"github.com/jtarchie/pocketci/orchestra/vz"
)

// createDriver creates a driver instance from a driver name, namespace,
// typed server config (or nil), and logger. When serverCfg is nil an
// empty ServerConfig for the requested driver is used.
func createDriver(driverName, namespace string, serverCfg orchestra.DriverConfig, logger *slog.Logger) (orchestra.Driver, error) {
	switch driverName {
	case "docker":
		cfg, err := asServerConfig[docker.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return docker.New(docker.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	case "native":
		return native.New(native.Config{Namespace: namespace}, logger)

	case "fly":
		cfg, err := asServerConfig[fly.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return fly.New(fly.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	case "hetzner":
		cfg, err := asServerConfig[hetzner.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return hetzner.New(hetzner.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	case "digitalocean":
		cfg, err := asServerConfig[digitalocean.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return digitalocean.New(digitalocean.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	case "k8s":
		cfg, err := asServerConfig[k8s.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return k8s.New(k8s.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	case "qemu":
		cfg, err := asServerConfig[qemu.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return qemu.New(qemu.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	case "vz":
		cfg, err := asServerConfig[vz.ServerConfig](serverCfg)
		if err != nil {
			return nil, err
		}

		return vz.New(vz.Config{ServerConfig: cfg, Namespace: namespace}, logger)

	default:
		return nil, fmt.Errorf("unknown driver %q", driverName)
	}
}

// asServerConfig converts an orchestra.DriverConfig to the concrete type T.
// If serverCfg is nil, the zero value of T is returned.
func asServerConfig[T orchestra.DriverConfig](serverCfg orchestra.DriverConfig) (T, error) {
	var zero T

	if serverCfg == nil {
		return zero, nil
	}

	if typed, ok := serverCfg.(T); ok {
		return typed, nil
	}

	return zero, fmt.Errorf("driver config type mismatch: expected %T, got %T", zero, serverCfg)
}

// unmarshalDriverConfig unmarshals raw JSON into the appropriate typed
// ServerConfig for the given driver name.
func unmarshalDriverConfig(driverName string, raw json.RawMessage) (orchestra.DriverConfig, error) {
	switch driverName {
	case "docker":
		var cfg docker.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid docker config: %w", err)
		}

		return cfg, nil

	case "native":
		return native.ServerConfig{}, nil

	case "fly":
		var cfg fly.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid fly config: %w", err)
		}

		return cfg, nil

	case "hetzner":
		var cfg hetzner.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid hetzner config: %w", err)
		}

		return cfg, nil

	case "digitalocean":
		var cfg digitalocean.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid digitalocean config: %w", err)
		}

		return cfg, nil

	case "k8s":
		var cfg k8s.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid k8s config: %w", err)
		}

		return cfg, nil

	case "qemu":
		var cfg qemu.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid qemu config: %w", err)
		}

		return cfg, nil

	case "vz":
		var cfg vz.ServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("invalid vz config: %w", err)
		}

		return cfg, nil

	default:
		return nil, fmt.Errorf("unknown driver %q", driverName)
	}
}
