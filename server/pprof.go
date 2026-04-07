package server

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // intentional: pprof only started when --pprof-addr is explicitly set
)

// StartPprof starts the pprof HTTP server on addr in a background goroutine.
// net/http/pprof registers its handlers on http.DefaultServeMux via init().
// This must only be called when profiling is explicitly requested — never in
// production without a firewall, since pprof exposes heap/goroutine dumps.
func StartPprof(addr string, logger *slog.Logger) {
	logger.Info("pprof.starting", "addr", addr)

	go func() {
		err := http.ListenAndServe(addr, nil)
		if err != nil { //nolint:gosec // addr is operator-supplied
			logger.Error("pprof.server.error", "err", err)
		}
	}()
}
