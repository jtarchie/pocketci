package fly

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("fly", func(namespace string, config map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		return New(Config{
			Namespace: namespace,
			Token:     config["token"],
			App:       config["app"],
			Region:    config["region"],
			Org:       config["org"],
			Size:      config["size"],
		}, logger)
	})
}
