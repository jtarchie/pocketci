package digitalocean

import (
	"log/slog"
	"strconv"

	"github.com/jtarchie/pocketci/orchestra"
)

func init() {
	orchestra.RegisterDriver("digitalocean", func(namespace string, config map[string]string, logger *slog.Logger) (orchestra.Driver, error) {
		maxWorkers, _ := strconv.Atoi(config["max_workers"])
		reuseWorker, _ := strconv.ParseBool(config["reuse_worker"])

		return New(Config{
			Namespace:   namespace,
			Token:       config["token"],
			Image:       config["image"],
			Size:        config["size"],
			Region:      config["region"],
			MaxWorkers:  maxWorkers,
			ReuseWorker: reuseWorker,
		}, logger)
	})
}
