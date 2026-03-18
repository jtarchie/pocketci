package k8s

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("k8s", func(namespace string, config map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		return New(Config{
			Namespace:    namespace,
			Kubeconfig:   config["kubeconfig"],
			K8sNamespace: config["namespace"],
		}, logger)
	})
}
