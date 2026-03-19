package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestWebShareController(t *testing.T) {
	t.Parallel()

	t.Run("GET /share/<invalid-token>/tasks returns 404", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
		assert.Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodGet, "/share/not-a-valid-token.abc/tasks", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	t.Run("GET /share/<valid-token>/tasks returns 200 with task tree", func(t *testing.T) {
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

		pipeline, err := client.SavePipeline(context.Background(), "share-test-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{"status": "success"})
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{SecretsManager: mgr})
		assert.Expect(err).NotTo(HaveOccurred())

		// First create a share token via the API.
		apiReq := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/share", nil)
		apiRec := httptest.NewRecorder()
		router.ServeHTTP(apiRec, apiReq)
		assert.Expect(apiRec.Code).To(Equal(http.StatusOK))

		apiResp := mustJSONMap(t, apiRec)
		sharePath, ok := apiResp["share_path"].(string)
		assert.Expect(ok).To(BeTrue())
		assert.Expect(sharePath).To(ContainSubstring("/share/"))
		assert.Expect(sharePath).To(HaveSuffix("/tasks"))

		// Access the share URL.
		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		assert.Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"))

		doc := mustHTMLDocument(t, rec)
		assert.Expect(hasSelectorWithText(doc, "h1", "Tasks")).To(BeTrue())
		assert.Expect(doc.Find(`details[data-task-id*="0-build"]`).Length()).To(BeNumerically(">", 0))
	})

	t.Run("shared view has no Stop button", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, _ := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		// Stop button must not appear in the shared view.
		assert.Expect(doc.Find("button#run-stop-button").Length()).To(Equal(0))
	})

	t.Run("shared view has no Run JSON or Tasks JSON links", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, runID := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		assert.Expect(selectorHasAttrValue(doc, "a", "href", "/api/runs/"+runID+"/status")).To(BeFalse())
		assert.Expect(selectorHasAttrValue(doc, "a", "href", "/api/runs/"+runID+"/tasks")).To(BeFalse())
	})

	t.Run("shared view has no per-task JSON links", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, runID := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		assert.Expect(selectorHasAttrContaining(doc, "a", "href", "/api/runs/"+runID+"/tasks?path=")).To(BeFalse())
	})

	t.Run("shared view has no HTMX polling", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, _ := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		body := rec.Body.String()
		assert.Expect(body).NotTo(ContainSubstring("hx-trigger=\"every 3s\""))
		assert.Expect(body).NotTo(ContainSubstring("tasks-partial"))
	})

	t.Run("shared view has no lazy-load terminal attributes", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, _ := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		body := rec.Body.String()
		assert.Expect(body).NotTo(ContainSubstring("hx-trigger=\"revealed once\""))
	})

	t.Run("shared view redacts pipeline secrets from terminal output", func(t *testing.T) {
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

		pipeline, err := client.SavePipeline(context.Background(), "secret-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		const secretValue = "my-super-secret-api-key-12345"

		// Store a pipeline secret.
		err = mgr.Set(context.Background(), "pipeline/"+pipeline.ID, "api_key", secretValue)
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		// Task output contains the secret value.
		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-deploy", map[string]any{
			"status": "success",
			"stdout": "Deploying with key: " + secretValue,
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
		assert.Expect(body).NotTo(ContainSubstring(secretValue))
		assert.Expect(body).To(ContainSubstring("[REDACTED]"))
	})

	t.Run("shared view has no Share button", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, _ := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		assert.Expect(doc.Find("#share-run-button").Length()).To(Equal(0))
	})

	t.Run("shared view has data-readonly on tasks-container", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		router, sharePath, _ := newShareTestSetup(t)

		req := httptest.NewRequest(http.MethodGet, sharePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		val, exists := doc.Find("#tasks-container").Attr("data-readonly")
		assert.Expect(exists).To(BeTrue())
		assert.Expect(val).To(Equal("true"))
	})

	t.Run("shared view redacts global secrets from terminal output", func(t *testing.T) {
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

		pipeline, err := client.SavePipeline(context.Background(), "global-secret-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		const globalSecretValue = "global-token-xyz-9876"

		err = mgr.Set(context.Background(), "global", "token", globalSecretValue)
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-deploy", map[string]any{
			"status": "success",
			"stdout": "Using token: " + globalSecretValue,
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
		assert.Expect(body).NotTo(ContainSubstring(globalSecretValue))
		assert.Expect(body).To(ContainSubstring("[REDACTED]"))
	})

	t.Run("normal view has no data-readonly on tasks-container", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "normal-view-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
		assert.Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		_, exists := doc.Find("#tasks-container").Attr("data-readonly")
		assert.Expect(exists).To(BeFalse())
	})

	t.Run("normal view still shows Share button in ... menu", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "share-button-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
		assert.Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		doc := mustHTMLDocument(t, rec)
		assert.Expect(doc.Find("#share-run-button").Length()).To(BeNumerically(">", 0))
	})
}

func TestAPIShareController(t *testing.T) {
	t.Parallel()

	t.Run("POST /api/runs/:run_id/share returns share_path", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "api-share-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
		assert.Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/share", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusOK))
		resp := mustJSONMap(t, rec)
		sharePath, ok := resp["share_path"].(string)
		assert.Expect(ok).To(BeTrue())
		assert.Expect(sharePath).To(HavePrefix("/share/"))
		assert.Expect(sharePath).To(HaveSuffix("/tasks"))
	})

	t.Run("POST /api/runs/nonexistent/share returns 404", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
		assert.Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodPost, "/api/runs/does-not-exist/share", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	t.Run("token in share_path validates back to the correct run ID", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		mgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "roundtrip-test"}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = mgr.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "roundtrip-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{SecretsManager: mgr})
		assert.Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/share", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Expect(rec.Code).To(Equal(http.StatusOK))

		resp := mustJSONMap(t, rec)
		sharePath := resp["share_path"].(string)

		// Extract the token from the path: /share/<token>/tasks
		// sharePath looks like "/share/abc123.def456/tasks"
		const prefix = "/share/"
		const suffix = "/tasks"
		assert.Expect(sharePath).To(HavePrefix(prefix))
		assert.Expect(sharePath).To(HaveSuffix(suffix))

		token := sharePath[len(prefix) : len(sharePath)-len(suffix)]
		assert.Expect(token).NotTo(BeEmpty())

		// The share page for this token must serve the run.
		shareReq := httptest.NewRequest(http.MethodGet, sharePath, nil)
		shareRec := httptest.NewRecorder()
		router.ServeHTTP(shareRec, shareReq)
		assert.Expect(shareRec.Code).To(Equal(http.StatusOK))

		// And tampering with the token must return 404.
		tamperedPath := prefix + token + "TAMPERED" + suffix
		tamperedReq := httptest.NewRequest(http.MethodGet, tamperedPath, nil)
		tamperedRec := httptest.NewRecorder()
		router.ServeHTTP(tamperedRec, tamperedReq)
		assert.Expect(tamperedRec.Code).To(Equal(http.StatusNotFound))
	})

	t.Run("share token round-trip using auth package directly", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		token, err := auth.GenerateShareToken("my-run-id", "test-secret")
		assert.Expect(err).NotTo(HaveOccurred())

		claims, err := auth.ValidateShareToken(token, "test-secret")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(claims.RunID).To(Equal("my-run-id"))
	})
}

// newShareTestSetup creates a router with a pipeline, run, and task, then
// obtains a valid share path via POST /api/runs/:run_id/share.
// Returns (router, sharePath, runID).
func newShareTestSetup(t *testing.T) (*server.Router, string, string) {
	t.Helper()
	assert := NewGomegaWithT(t)

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = buildFile.Close() })

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = client.Close() })

	mgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-setup"}, slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = mgr.Close() })

	pipeline, err := client.SavePipeline(context.Background(), "setup-pipeline-"+t.Name(), "export const pipeline = async () => {};", "docker", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := client.SaveRun(context.Background(), pipeline.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-step", map[string]any{
		"status": "success",
		"stdout": "hello from task",
	})
	assert.Expect(err).NotTo(HaveOccurred())

	// Mark run as completed so the share view renders a non-active run.
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

	return router, sharePath, run.ID
}
