package server_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/onsi/gomega"
)

func setupRouterWithAuth(t *testing.T, username, password string) *server.Router {
	tempDir := t.TempDir()
	buildFile, err := os.CreateTemp(tempDir, "")
	if err != nil {
		t.Fatalf("could not create temp file: %v", err)
	}
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
	if err != nil {
		t.Fatalf("could not create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	secretsManager, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
	if err != nil {
		t.Fatalf("could not create secrets manager: %v", err)
	}
	t.Cleanup(func() { _ = secretsManager.Close() })

	router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
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
		"driver": "native",
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
		"driver": "native",
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
		"driver": "native",
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
		"driver": "native",
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
		"driver": "native",
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
		"driver": "native",
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
		"driver": "native",
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

// setupRouterWithAuthAndStore returns both the router and the storage driver,
// so tests can manipulate pipeline state directly (e.g., setting RBAC expressions).
func setupRouterWithAuthAndStore(t *testing.T, username, password string) (*server.Router, storage.Driver) {
	t.Helper()

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatalf("could not create temp file: %v", err)
	}
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
	if err != nil {
		t.Fatalf("could not create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	secretsManager, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
	if err != nil {
		t.Fatalf("could not create secrets manager: %v", err)
	}
	t.Cleanup(func() { _ = secretsManager.Close() })

	router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
		BasicAuthUsername: username,
		BasicAuthPassword: password,
		SecretsManager:    secretsManager,
	})
	if err != nil {
		t.Fatalf("could not create router: %v", err)
	}

	return router, client
}

func TestBasicAuthDeniedWhenPipelineHasRBAC(t *testing.T) {
	assert := gomega.NewWithT(t)
	router, client := setupRouterWithAuthAndStore(t, "user", "pass")

	// Create a pipeline via the API (basic auth, no RBAC).
	body, _ := json.Marshal(map[string]string{
		"content": `const pipeline = async () => {}; export { pipeline };`,
		"driver":  "native",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/rbac-test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	// Set RBAC expression directly in storage (simulating prior OAuth setup).
	pipeline, err := client.GetPipelineByName(t.Context(), "rbac-test")
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	err = client.UpdatePipelineRBACExpression(t.Context(), pipeline.ID, `Email == "admin@example.com"`)
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	// GET pipeline — should be denied (basic auth can't satisfy RBAC).
	req = httptest.NewRequest(http.MethodGet, "/api/pipelines/"+pipeline.ID, nil)
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusForbidden))

	// PUT update — should be denied.
	body, _ = json.Marshal(map[string]string{
		"content": `const pipeline = async () => { console.log("hacked"); }; export { pipeline };`,
		"driver":  "native",
	})
	req = httptest.NewRequest(http.MethodPut, "/api/pipelines/rbac-test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusForbidden))

	// DELETE — should be denied.
	req = httptest.NewRequest(http.MethodDelete, "/api/pipelines/"+pipeline.ID, nil)
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusForbidden))
}

func TestBasicAuthAllowedWhenPipelineHasNoRBAC(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "user", "pass")

	// Create pipeline without RBAC.
	body, _ := json.Marshal(map[string]string{
		"content": `const pipeline = async () => {}; export { pipeline };`,
		"driver":  "native",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/open-pipeline", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	var resp map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	pipelineID, _ := resp["id"].(string)

	// GET pipeline — should succeed (no RBAC).
	req = httptest.NewRequest(http.MethodGet, "/api/pipelines/"+pipelineID, nil)
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	// DELETE pipeline — should succeed.
	req = httptest.NewRequest(http.MethodDelete, "/api/pipelines/"+pipelineID, nil)
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusNoContent))
}

func TestBasicAuthRejectsSettingRBACExpression(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithAuth(t, "user", "pass")

	// Attempt to create pipeline with RBAC expression via basic auth — should be rejected.
	body, _ := json.Marshal(map[string]any{
		"content":         `const pipeline = async () => {}; export { pipeline };`,
		"driver":          "native",
		"rbac_expression": `Email == "admin@example.com"`,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/rbac-attempt", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// The pipeline is created (content saved), but the RBAC expression is rejected.
	// upsertPostSave returns a 400 for the RBAC expression.
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusBadRequest))

	var errResp map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &errResp)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(errResp["error"]).To(gomega.ContainSubstring("RBAC expressions require OAuth"))
}
