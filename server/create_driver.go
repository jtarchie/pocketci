package server

import (
	"context"
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
func createDriver(ctx context.Context, driverName, namespace string, serverCfg orchestra.DriverConfig, logger *slog.Logger) (orchestra.Driver, error) {
	switch driverName {
	case "docker":
		return createTypedDriver[docker.ServerConfig](ctx, serverCfg, func(cfg docker.ServerConfig) (orchestra.Driver, error) {
			return docker.New(ctx, docker.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	case "native":
		driver, err := native.New(ctx, native.Config{Namespace: namespace}, logger)
		if err != nil {
			return nil, fmt.Errorf("create native driver: %w", err)
		}

		return driver, nil
	case "fly":
		return createTypedDriver[fly.ServerConfig](ctx, serverCfg, func(cfg fly.ServerConfig) (orchestra.Driver, error) {
			return fly.New(ctx, fly.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	case "hetzner":
		return createTypedDriver[hetzner.ServerConfig](ctx, serverCfg, func(cfg hetzner.ServerConfig) (orchestra.Driver, error) {
			return hetzner.New(ctx, hetzner.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	case "digitalocean":
		return createTypedDriver[digitalocean.ServerConfig](ctx, serverCfg, func(cfg digitalocean.ServerConfig) (orchestra.Driver, error) {
			return digitalocean.New(ctx, digitalocean.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	case "k8s":
		return createTypedDriver[k8s.ServerConfig](ctx, serverCfg, func(cfg k8s.ServerConfig) (orchestra.Driver, error) {
			return k8s.New(ctx, k8s.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	case "qemu":
		return createTypedDriver[qemu.ServerConfig](ctx, serverCfg, func(cfg qemu.ServerConfig) (orchestra.Driver, error) {
			return qemu.New(ctx, qemu.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	case "vz":
		return createTypedDriver[vz.ServerConfig](ctx, serverCfg, func(cfg vz.ServerConfig) (orchestra.Driver, error) {
			return vz.New(ctx, vz.Config{ServerConfig: cfg, Namespace: namespace}, logger)
		})
	default:
		return nil, fmt.Errorf("unknown driver %q", driverName)
	}
}

// createTypedDriver converts a generic DriverConfig to a concrete type T and
// passes it to the provided constructor function.
func createTypedDriver[T orchestra.DriverConfig](_ context.Context, serverCfg orchestra.DriverConfig, newFn func(T) (orchestra.Driver, error)) (orchestra.Driver, error) {
	cfg, err := asServerConfig[T](serverCfg)
	if err != nil {
		return nil, err
	}

	return newFn(cfg)
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
		return unmarshalTypedConfig[docker.ServerConfig](raw, "docker")
	case "native":
		return native.ServerConfig{}, nil
	case "fly":
		return unmarshalTypedConfig[fly.ServerConfig](raw, "fly")
	case "hetzner":
		return unmarshalTypedConfig[hetzner.ServerConfig](raw, "hetzner")
	case "digitalocean":
		return unmarshalTypedConfig[digitalocean.ServerConfig](raw, "digitalocean")
	case "k8s":
		return unmarshalTypedConfig[k8s.ServerConfig](raw, "k8s")
	case "qemu":
		return unmarshalTypedConfig[qemu.ServerConfig](raw, "qemu")
	case "vz":
		return unmarshalTypedConfig[vz.ServerConfig](raw, "vz")
	default:
		return nil, fmt.Errorf("unknown driver %q", driverName)
	}
}

// unmarshalTypedConfig unmarshals raw JSON into the concrete config type T.
func unmarshalTypedConfig[T orchestra.DriverConfig](raw json.RawMessage, name string) (orchestra.DriverConfig, error) {
	var cfg T
	err := json.Unmarshal(raw, &cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid %s config: %w", name, err)
	}

	return cfg, nil
}
