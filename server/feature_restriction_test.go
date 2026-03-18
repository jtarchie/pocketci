package server_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "github.com/jtarchie/pocketci/orchestra/native"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestFeatureRestriction(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("rejects webhook_secret when webhooks feature is disabled", func(t *testing.T) {
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

			// Create router with only secrets enabled (no webhooks)
			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedDrivers:  "native",
				AllowedFeatures: "secrets",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			body := map[string]string{
				"content":        "export { pipeline };",
				"driver":     "native",
				"webhook_secret": "my-secret",
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))
			message := mustJSONErrorText(t, rec)
			assert.Expect(message).To(ContainSubstring("webhooks feature is not enabled"))
		})

		t.Run("allows webhook_secret when webhooks feature is enabled", func(t *testing.T) {
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
				AllowedDrivers:  "native",
				AllowedFeatures: "webhooks,secrets",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			body := map[string]string{
				"content":        "export { pipeline };",
				"driver":     "native",
				"webhook_secret": "my-secret",
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
		})

		t.Run("rejects webhook trigger when webhooks feature is disabled", func(t *testing.T) {
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

			// Create router with no webhooks
			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedDrivers:  "native",
				AllowedFeatures: "secrets",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			// Hit the webhook endpoint
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/some-id", bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusForbidden))
			message := mustJSONErrorText(t, rec)
			assert.Expect(message).To(ContainSubstring("webhooks feature is not enabled"))
		})

		t.Run("wildcard enables all features", func(t *testing.T) {
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

			// Create router with wildcard (default)
			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedDrivers:  "native",
				AllowedFeatures: "*",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			// Should allow pipeline with webhook_secret
			body := map[string]string{
				"content":        "export { pipeline };",
				"driver":     "native",
				"webhook_secret": "my-secret",
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
		})

		t.Run("rejects unknown feature name", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			_, err = server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedDrivers:  "native",
				AllowedFeatures: "webhooks,bogus",
			})
			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("unknown feature"))
			assert.Expect(err.Error()).To(ContainSubstring("bogus"))
		})

		t.Run("defaults to all features when empty", func(t *testing.T) {
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

			// Empty string should default to all features
			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedDrivers:  "native",
				AllowedFeatures: "",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			// Should allow pipelines with webhook_secret (all features enabled)
			body := map[string]string{
				"content":        "export { pipeline };",
				"driver":     "native",
				"webhook_secret": "my-secret",
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
		})

		t.Run("pipeline without webhook_secret works even when webhooks disabled", func(t *testing.T) {
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

			// Create router with only secrets (no webhooks)
			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
				AllowedDrivers:  "native",
				AllowedFeatures: "secrets",
				SecretsManager:  secretsMgr,
			})
			assert.Expect(err).NotTo(HaveOccurred())

			// Pipeline without webhook_secret should work fine
			body := map[string]string{
				"content":    "export { pipeline };",
				"driver": "native",
			}
			jsonBody, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})
}
