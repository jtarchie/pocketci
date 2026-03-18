package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func newRouterWithSecrets(t *testing.T, client storage.Driver, opts server.RouterOptions) *server.Router {
	t.Helper()

	if opts.SecretsManager == nil {
		secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
		if err != nil {
			t.Fatalf("could not create secrets manager: %v", err)
		}

		t.Cleanup(func() { _ = secretsMgr.Close() })
		opts.SecretsManager = secretsMgr
	}

	router, err := server.NewRouter(slog.Default(), client, opts)
	if err != nil {
		t.Fatalf("could not create router: %v", err)
	}

	return router
}

func TestPipelineAPI(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("PUT /api/pipelines/:name creates a pipeline", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			body := map[string]string{
				"content": "export { pipeline };",
				"driver":  "docker",
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["id"]).NotTo(BeNil())
			assert.Expect(resp["name"]).To(Equal("test-pipeline"))
			assert.Expect(resp["content"]).To(Equal("export { pipeline };"))
			_, hasDriver := resp["driver"]
			assert.Expect(hasDriver).To(BeFalse())
		})

		t.Run("PUT /api/pipelines/:name returns 400 for missing content", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			body := map[string]string{}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))
		})

		t.Run("GET /api/pipelines lists all pipelines", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			_, err = client.SavePipeline(context.Background(), "pipeline-1", "content1", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var result map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &result)
			assert.Expect(err).NotTo(HaveOccurred())
			items, ok := result["items"].([]any)
			assert.Expect(ok).To(BeTrue())
			assert.Expect(items).To(HaveLen(1))
			item, ok := items[0].(map[string]any)
			assert.Expect(ok).To(BeTrue())
			_, hasDriver := item["driver"]
			assert.Expect(hasDriver).To(BeFalse())
		})

		t.Run("GET /api/pipelines/:id retrieves a pipeline", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			saved, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodGet, "/api/pipelines/"+saved.ID, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["id"]).To(Equal(saved.ID))
			assert.Expect(resp["name"]).To(Equal("my-pipeline"))
			_, hasDriver := resp["driver"]
			assert.Expect(hasDriver).To(BeFalse())
		})

		t.Run("GET /api/pipelines/:id returns 404 for non-existent", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodGet, "/api/pipelines/non-existent", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})

		t.Run("DELETE /api/pipelines/:id deletes a pipeline", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			saved, err := client.SavePipeline(context.Background(), "to-delete", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodDelete, "/api/pipelines/"+saved.ID, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNoContent))

			// Verify it's deleted
			_, err = client.GetPipeline(context.Background(), saved.ID)
			assert.Expect(err).To(Equal(storage.ErrNotFound))
		})

		t.Run("DELETE /api/pipelines/:id returns 404 for non-existent", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodDelete, "/api/pipelines/non-existent", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})

		t.Run("PUT /api/pipelines/:name update with webhook_secret and secrets succeeds", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = secretsMgr.Close() }()

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedFeatures: "webhooks,secrets",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(), "with-webhook", "content-v1", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			err = secretsMgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "webhook_secret", "existing-webhook-secret")
			assert.Expect(err).NotTo(HaveOccurred())

			body := map[string]any{
				"content":        "content-v2",
				"driver":         "native",
				"webhook_secret": "new-webhook-secret",
				"secrets": map[string]string{
					"GITHUB_TOKEN": "token-value",
				},
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/with-webhook", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			updated, err := client.GetPipelineByName(context.Background(), "with-webhook")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(updated.Content).To(Equal("content-v2"))
		})

		t.Run("PUT /api/pipelines/:name missing existing secret key does not persist content update", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = secretsMgr.Close() }()

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedFeatures: "secrets",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(), "atomic-update", "content-v1", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			err = secretsMgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "REQUIRED_KEY", "initial")
			assert.Expect(err).NotTo(HaveOccurred())

			body := map[string]any{
				"content": "content-v2",
				"driver":  "native",
				"secrets": map[string]string{
					"OTHER_KEY": "other",
				},
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/atomic-update", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))

			reloaded, err := client.GetPipelineByName(context.Background(), "atomic-update")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(reloaded.Content).To(Equal("content-v1"))
		})

		t.Run("PUT /api/pipelines/:name rejects system key driver in user secrets", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			body := map[string]any{
				"content": "export { pipeline };",
				"driver":  "docker",
				"secrets": map[string]string{
					"driver": "docker://attacker.com",
				},
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).To(ContainSubstring("reserved for system use"))
		})

		t.Run("PUT /api/pipelines/:name rejects system key webhook_secret in user secrets", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newRouterWithSecrets(t, client, server.RouterOptions{})

			body := map[string]any{
				"content": "export { pipeline };",
				"driver":  "docker",
				"secrets": map[string]string{
					"webhook_secret": "malicious",
				},
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).To(ContainSubstring("reserved for system use"))
		})
	})
}
