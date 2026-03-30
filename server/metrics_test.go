package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func newMetricsTestClient(t *testing.T) storage.Driver {
	t.Helper()

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatalf("could not create temp file: %v", err)
	}
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
	if err != nil {
		t.Fatalf("could not create storage client: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	return client
}

func TestMetricsDashboard(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("GET /metrics/ returns 200 with empty store", func(t *testing.T) {
			t.Parallel()
			assert := NewWithT(t)

			client := newMetricsTestClient(t)
			router := newRouterWithSecrets(t, client, server.RouterOptions{MaxInFlight: 5})

			req := httptest.NewRequest(http.MethodGet, "/metrics/", http.NoBody)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
			body := rec.Body.String()
			assert.Expect(body).To(ContainSubstring("Metrics"))
		})

		t.Run("GET /metrics/ shows pipeline and run counts", func(t *testing.T) {
			t.Parallel()
			assert := NewWithT(t)

			ctx := context.Background()
			client := newMetricsTestClient(t)

			// Create two pipelines
			p1, err := client.SavePipeline(ctx, "pipeline-alpha", "export {};", "native", "js")
			assert.Expect(err).NotTo(HaveOccurred())
			p2, err := client.SavePipeline(ctx, "pipeline-beta", "export {};", "native", "js")
			assert.Expect(err).NotTo(HaveOccurred())

			// 2 success runs for pipeline-alpha
			for range 2 {
				run, runErr := client.SaveRun(ctx, p1.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
				assert.Expect(runErr).NotTo(HaveOccurred())
				assert.Expect(client.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, "")).NotTo(HaveOccurred())
			}

			// 1 failed run for pipeline-beta
			run, err := client.SaveRun(ctx, p2.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(client.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "something went wrong")).NotTo(HaveOccurred())

			router := newRouterWithSecrets(t, client, server.RouterOptions{MaxInFlight: 10})
			req := httptest.NewRequest(http.MethodGet, "/metrics/", http.NoBody)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
			body := rec.Body.String()

			// Pipeline names appear
			assert.Expect(body).To(ContainSubstring("pipeline-alpha"))
			assert.Expect(body).To(ContainSubstring("pipeline-beta"))

			// Status labels appear
			assert.Expect(body).To(ContainSubstring("Success"))
			assert.Expect(body).To(ContainSubstring("Failed"))

			// Recent failures section with error message
			assert.Expect(body).To(ContainSubstring("something went wrong"))
		})

		t.Run("GET /metrics/content returns partial for HTMX", func(t *testing.T) {
			t.Parallel()
			assert := NewWithT(t)

			client := newMetricsTestClient(t)
			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodGet, "/metrics/content", http.NoBody)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
			body := rec.Body.String()
			// Partial should contain the metrics content div but not the full page boilerplate
			assert.Expect(body).To(ContainSubstring("metrics-content"))
			assert.Expect(strings.Contains(body, "<!DOCTYPE html>")).To(BeFalse())
		})

		t.Run("GetRunStats returns correct counts", func(t *testing.T) {
			t.Parallel()
			assert := NewWithT(t)

			ctx := context.Background()
			client := newMetricsTestClient(t)

			p, err := client.SavePipeline(ctx, "stats-pipeline", "export {};", "native", "js")
			assert.Expect(err).NotTo(HaveOccurred())

			statuses := []storage.RunStatus{
				storage.RunStatusSuccess,
				storage.RunStatusSuccess,
				storage.RunStatusFailed,
				storage.RunStatusSkipped,
			}
			for _, status := range statuses {
				run, runErr := client.SaveRun(ctx, p.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
				assert.Expect(runErr).NotTo(HaveOccurred())
				assert.Expect(client.UpdateRunStatus(ctx, run.ID, status, "")).NotTo(HaveOccurred())
			}

			stats, err := client.GetRunStats(ctx)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(stats[storage.RunStatusSuccess]).To(Equal(2))
			assert.Expect(stats[storage.RunStatusFailed]).To(Equal(1))
			assert.Expect(stats[storage.RunStatusSkipped]).To(Equal(1))
			assert.Expect(stats[storage.RunStatusRunning]).To(Equal(0))
		})

		t.Run("GetRecentRunsByStatus respects limit and ordering", func(t *testing.T) {
			t.Parallel()
			assert := NewWithT(t)

			ctx := context.Background()
			client := newMetricsTestClient(t)

			p, err := client.SavePipeline(ctx, "recent-pipeline", "export {};", "native", "js")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create 5 failed runs
			for range 5 {
				run, runErr := client.SaveRun(ctx, p.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
				assert.Expect(runErr).NotTo(HaveOccurred())
				assert.Expect(client.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "err")).NotTo(HaveOccurred())
			}

			// Limit to 3
			runs, err := client.GetRunsByStatus(ctx, storage.RunStatusFailed, 3)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(runs).To(HaveLen(3))
			for _, r := range runs {
				assert.Expect(r.Status).To(Equal(storage.RunStatusFailed))
			}
		})
	})
}
