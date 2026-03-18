//go:build darwin

package vz

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("vz", func(namespace string, config map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		return New(Config{
			Namespace: namespace,
			Memory:    config["memory"],
			CPUs:      config["cpus"],
			CacheDir:  config["cache_dir"],
			Image:     config["image"],
		}, logger)
	})
}
