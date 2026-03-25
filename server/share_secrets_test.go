package server_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

// capturingHandler is a slog.Handler that captures all log records.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()

	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }
func (h *capturingHandler) warnMessages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var msgs []string

	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			msgs = append(msgs, r.Message)
		}
	}

	return msgs
}

// erroringSecretsManager is a secrets.Manager that always errors on ListByScope.
type erroringSecretsManager struct{}

func (e *erroringSecretsManager) ListByScope(_ context.Context, _ string) ([]string, error) {
	return nil, errors.New("backend unavailable")
}

func (e *erroringSecretsManager) Get(_ context.Context, _, _ string) (string, error) {
	return "", errors.New("backend unavailable")
}

func (e *erroringSecretsManager) Set(_ context.Context, _, _, _ string) error {
	return errors.New("backend unavailable")
}

func (e *erroringSecretsManager) Delete(_ context.Context, _, _ string) error {
	return errors.New("backend unavailable")
}

func (e *erroringSecretsManager) DeleteByScope(_ context.Context, _ string) error {
	return errors.New("backend unavailable")
}

func (e *erroringSecretsManager) Close() error { return nil }

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

	t.Run("logs warn when secrets backend errors during collection", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		handler := &capturingHandler{}
		logger := slog.New(handler)

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "erroring-secrets-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-step", map[string]any{
			"status": "success",
			"stdout": "some output",
		})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Use a secrets manager that always errors, to trigger warn logging.
		router, err := server.NewRouter(logger, client, server.RouterOptions{SecretsManager: &erroringSecretsManager{}})
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

		// The share view should still render (gracefully degraded).
		// And the logger should have captured Warn messages about the errors.
		warnMsgs := handler.warnMessages()
		assert.Expect(warnMsgs).To(ContainElement(ContainSubstring("secrets.collect")))
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
