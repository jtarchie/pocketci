package native

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("native", func(namespace string, _ map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		return New(Config{
			Namespace: namespace,
		}, logger)
	})
}
