package server_test

import (
	"context"
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

func TestRedactSecretValuesViaShareView(t *testing.T) {
	t.Parallel()

	t.Run("replaces known secret in shared view", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		mgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test"}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = mgr.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "secret-test-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Store a pipeline-scoped secret whose value appears in task output
		err = mgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "api_key", "my-super-secret-token")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())

		// Task output contains the secret value
		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-deploy", map[string]any{
			"status": "success",
			"stdout": "deploying with key my-super-secret-token done",
		})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{SecretsManager: mgr})
		assert.Expect(err).NotTo(HaveOccurred())

		// Create a share link
		apiReq := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/share", nil)
		apiRec := httptest.NewRecorder()
		router.ServeHTTP(apiRec, apiReq)
		assert.Expect(apiRec.Code).To(Equal(http.StatusOK))

		apiResp := mustJSONMap(t, apiRec)
		sharePath := apiResp["share_path"].(string)

		// Access the share view
		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Expect(rec.Code).To(Equal(http.StatusOK))

		body := rec.Body.String()
		assert.Expect(body).NotTo(ContainSubstring("my-super-secret-token"))
		assert.Expect(body).To(ContainSubstring("***REDACTED***"))
	})

	t.Run("replaces multiple secrets in shared view", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		mgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test"}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = mgr.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "multi-secret-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Store multiple secrets
		err = mgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "token_a", "token-A-value")
		assert.Expect(err).NotTo(HaveOccurred())
		err = mgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "token_b", "token-B-value")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-step", map[string]any{
			"status": "success",
			"stdout": "using token-A-value and token-B-value here",
		})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{SecretsManager: mgr})
		assert.Expect(err).NotTo(HaveOccurred())

		apiReq := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/share", nil)
		apiRec := httptest.NewRecorder()
		router.ServeHTTP(apiRec, apiReq)
		assert.Expect(apiRec.Code).To(Equal(http.StatusOK))

		apiResp := mustJSONMap(t, apiRec)
		sharePath := apiResp["share_path"].(string)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Expect(rec.Code).To(Equal(http.StatusOK))

		body := rec.Body.String()
		assert.Expect(body).NotTo(ContainSubstring("token-A-value"))
		assert.Expect(body).NotTo(ContainSubstring("token-B-value"))
		assert.Expect(body).To(ContainSubstring("***REDACTED***"))
	})

	t.Run("handles no secrets gracefully", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		mgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test"}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = mgr.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "no-secret-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-step", map[string]any{
			"status": "success",
			"stdout": "no secrets here",
		})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{SecretsManager: mgr})
		assert.Expect(err).NotTo(HaveOccurred())

		apiReq := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/share", nil)
		apiRec := httptest.NewRecorder()
		router.ServeHTTP(apiRec, apiReq)
		assert.Expect(apiRec.Code).To(Equal(http.StatusOK))

		apiResp := mustJSONMap(t, apiRec)
		sharePath := apiResp["share_path"].(string)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Expect(rec.Code).To(Equal(http.StatusOK))

		body := rec.Body.String()
		assert.Expect(body).To(ContainSubstring("no secrets here"))
		assert.Expect(body).NotTo(ContainSubstring("***REDACTED***"))
	})
}
