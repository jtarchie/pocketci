package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/onsi/gomega"
)

func setupRouterWithAuth(t *testing.T, username, password string) *Router {
	tempDir := t.TempDir()
	buildFile, err := os.CreateTemp(tempDir, "")
	if err != nil {
		t.Fatalf("could not create temp file: %v", err)
	}
	defer func() { _ = buildFile.Close() }()

	initStorage, found := storage.GetFromDSN("sqlite://" + buildFile.Name())
	if !found {
		t.Fatal("could not get storage driver")
	}

	client, err := initStorage("sqlite://"+buildFile.Name(), "namespace", slog.Default())
	if err != nil {
		t.Fatalf("could not create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	secretsManager, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
	if err != nil {
		t.Fatalf("could not create secrets manager: %v", err)
	}
	t.Cleanup(func() { _ = secretsManager.Close() })

	router, err := NewRouter(slog.Default(), client, RouterOptions{
		BasicAuthUsername: username,
		BasicAuthPassword: password,
		SecretsManager:    secretsManager,
	})
	if err != nil {
		t.Fatalf("could not create router: %v", err)
	}

	return router
}

func TestBasicAuthDisabled(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "", "")

	// Create a pipeline for testing
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))
}

func TestBasicAuthMissingHeader(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))
}

func TestBasicAuthInvalidCredentials(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("testuser", "wrongpass")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))
}

func TestBasicAuthWrongUsername(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("wronguser", "testpass")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))
}

func TestBasicAuthValidCredentials(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("testuser", "testpass")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))
}

func TestBasicAuthProtectsAllAPIEndpoints(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	// Create a pipeline first with auth
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("testuser", "testpass")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	pipelineID := "test-pipeline"

	// Test GET /api/pipelines without auth
	req = httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Test GET /api/pipelines/:id without auth
	req = httptest.NewRequest(http.MethodGet, "/api/pipelines/"+pipelineID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Test DELETE /api/pipelines/:id without auth
	req = httptest.NewRequest(http.MethodDelete, "/api/pipelines/"+pipelineID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Test POST /api/pipelines/:id/trigger without auth
	req = httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipelineID+"/trigger", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Now test with proper auth - should succeed
	req = httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.SetBasicAuth("testuser", "testpass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))
}

func TestBasicAuthProtectsWebUIRoutes(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "admin", "secret123")

	// Test GET /pipelines/ without auth
	req := httptest.NewRequest(http.MethodGet, "/pipelines/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Test GET /pipelines/ with auth
	req = httptest.NewRequest(http.MethodGet, "/pipelines/", nil)
	req.SetBasicAuth("admin", "secret123")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	// Test / redirect without auth
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Test / redirect with auth
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "secret123")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusMovedPermanently))
}

func TestBasicAuthHealthEndpointPublic(t *testing.T) {
	assert := gomega.NewWithT(t)
	// Even with auth enabled, /health should be public
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	req = httptest.NewRequest(http.MethodGet, "/health/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))
}

func TestBasicAuthStaticFilesPublic(t *testing.T) {
	assert := gomega.NewWithT(t)
	// Static files should be public (even if they might return 404)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodGet, "/static/test.js", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	// Might be 404, but should not be 401
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}

func TestBasicAuthDocsPublic(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "testuser", "testpass")

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))

	req = httptest.NewRequest(http.MethodGet, "/docs/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}

func TestBasicAuthWebhooksSignatureOnly(t *testing.T) {
	assert := gomega.NewWithT(t)
	// Webhooks should not require basic auth, only signature validation
	router := setupRouterWithAuth(t, "testuser", "testpass")

	// Create a pipeline with no webhook secret (so no signature validation required)
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/webhook-pipeline", bytes.NewReader([]byte(`{
		"content": "const pipeline = async () => {}; export { pipeline };",
		"driver_dsn": "native://",
		"webhook_secret": ""
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("testuser", "testpass")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	// Webhook should be accessible without basic auth
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook-pipeline", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	// Should not return 401 Unauthorized
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}

func TestBasicAuthRunView(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "user", "pass")

	// Test /runs/:id view without auth
	req := httptest.NewRequest(http.MethodGet, "/runs/test-run-id/tasks", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	// Test /runs/:id view with auth
	req = httptest.NewRequest(http.MethodGet, "/runs/test-run-id/tasks", nil)
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	// Should be 200 (or might error out but not 401)
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}
