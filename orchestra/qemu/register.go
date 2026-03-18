package qemu

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("qemu", func(namespace string, config map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		return New(Config{
			Namespace: namespace,
			Memory:    config["memory"],
			CPUs:      config["cpus"],
			Accel:     config["accel"],
			Binary:    config["binary"],
			CacheDir:  config["cache_dir"],
			Image:     config["image"],
		}, logger)
	})
}
