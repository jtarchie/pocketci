package server_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/observability"
	"github.com/jtarchie/pocketci/server"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestPrometheusMetricsEndpointMountedWhenHandlerSet(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	dbFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = dbFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: dbFile.Name()}, "namespace", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	reg := prometheus.NewRegistry()
	metrics := observability.NewPromMetrics(reg)

	// Seed at least one metric so the gather has something to render.
	metrics.CounterAdd("pocketci_test_total", 1, nil)

	router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
		Metrics:        metrics,
		MetricsHandler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	})
	assert.Expect(err).NotTo(HaveOccurred())

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Expect(rec.Code).To(Equal(http.StatusOK))
	assert.Expect(rec.Body.String()).To(ContainSubstring("pocketci_test_total"))
}

func TestPrometheusMetricsEndpointAbsentWhenHandlerNil(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	dbFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = dbFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: dbFile.Name()}, "namespace", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
	assert.Expect(err).NotTo(HaveOccurred())

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
}
