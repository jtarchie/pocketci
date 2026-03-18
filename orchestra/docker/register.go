package docker

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("docker", func(namespace string, config map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		return New(Config{
			ServerConfig: ServerConfig{
				Host: config["host"],
			},
			Namespace: namespace,
		}, logger)
	})
}
