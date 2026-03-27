package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/server"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestSeedJobPassed(t *testing.T) {
	t.Parallel()

	t.Run("POST /api/pipelines/:id/jobs/:name/seed-passed creates synthetic success record", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

		req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/jobs/build-image/seed-passed", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))

		var resp map[string]any
		err = json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp["pipeline_id"]).To(Equal(pipeline.ID))
		assert.Expect(resp["job"]).To(Equal("build-image"))
		assert.Expect(resp["run_id"]).NotTo(BeEmpty())
		assert.Expect(resp["message"]).To(ContainSubstring("seeded"))

		// Verify the seeded record satisfies GetMostRecentJobStatus
		status, err := client.GetMostRecentJobStatus(context.Background(), pipeline.ID, "build-image")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status).To(Equal("success"))

		router.WaitForExecutions()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("POST /api/pipelines/:id/jobs/:name/seed-passed returns 404 for non-existent pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

		req := httptest.NewRequest(http.MethodPost, "/api/pipelines/nonexistent/jobs/build/seed-passed", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusNotFound))

		router.WaitForExecutions()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
