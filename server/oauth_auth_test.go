package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/onsi/gomega"
)

const testSessionSecret = "test-secret-key-at-least-32-bytes-long"

func setupRouterWithOAuth(t *testing.T, rbacExpression string) *Router {
	return setupRouterWithOAuthLogger(t, rbacExpression, slog.Default())
}

func setupRouterWithOAuthLogger(t *testing.T, rbacExpression string, logger *slog.Logger) *Router {
	t.Helper()

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

	authCfg := &auth.Config{
		GithubClientID:     "test-client-id",
		GithubClientSecret: "test-client-secret",
		SessionSecret:      testSessionSecret,
		CallbackURL:        "http://localhost:8080",
		ServerRBAC:         rbacExpression,
	}

	router, err := NewRouter(logger, client, RouterOptions{
		SecretsManager: secretsManager,
		AuthConfig:     authCfg,
	})
	if err != nil {
		t.Fatalf("could not create router: %v", err)
	}

	return router
}

func generateTestToken(t *testing.T, user *auth.User) string {
	t.Helper()

	token, err := auth.GenerateToken(user, testSessionSecret, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("could not generate token: %v", err)
	}

	return token
}

// --- RequireAuth tests ---

func TestOAuthRequireAuthBrowserRedirect(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	req := httptest.NewRequest(http.MethodGet, "/pipelines/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusFound))
	assert.Expect(rec.Header().Get("Location")).To(gomega.Equal("/auth/login"))
}

func TestOAuthRequireAuthAPIReturnsJSON(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	var body map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(body["error"]).To(gomega.Equal("authentication required"))
}

func TestOAuthRequireAuthValidBearerToken(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	user := &auth.User{
		Email:    "alice@example.com",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}
	token := generateTestToken(t, user)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	// Should pass auth and reach the handler (200, not 401)
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}

func TestOAuthRequestLogsIncludeAuthenticatedUser(t *testing.T) {
	assert := gomega.NewWithT(t)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	router := setupRouterWithOAuthLogger(t, "", logger)

	user := &auth.User{
		Email:    "alice@example.com",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}
	token := generateTestToken(t, user)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
	logs := logBuf.String()
	assert.Expect(strings.Contains(logs, `"auth_provider":"github"`)).To(gomega.BeTrue())
	assert.Expect(strings.Contains(logs, `"user":"alice@example.com"`)).To(gomega.BeTrue())
}

func TestOAuthRequireAuthInvalidBearerToken(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusUnauthorized))

	var body map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(body["error"]).To(gomega.Equal("invalid or expired token"))
}

func TestOAuthRequireAuthNoProvidersBypass(t *testing.T) {
	// When no OAuth providers are configured, auth middleware is a no-op
	assert := gomega.NewWithT(t)

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

	// Config with no providers — HasOAuthProviders() returns false
	authCfg := &auth.Config{
		SessionSecret: testSessionSecret,
	}

	router, err := NewRouter(slog.Default(), client, RouterOptions{
		SecretsManager: secretsManager,
		AuthConfig:     authCfg,
	})
	if err != nil {
		t.Fatalf("could not create router: %v", err)
	}

	// Should reach the handler without auth
	req := httptest.NewRequest(http.MethodGet, "/pipelines/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	// Should not be 401 or 302 redirect to login
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusFound))
}

// --- RequireRBAC tests ---

func TestOAuthRBACBrowserHTMLError(t *testing.T) {
	assert := gomega.NewWithT(t)
	// RBAC denies all users (impossible expression)
	router := setupRouterWithOAuth(t, `Email == "nobody@example.com"`)

	user := &auth.User{
		Email:    "alice@example.com",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}
	token := generateTestToken(t, user)

	req := httptest.NewRequest(http.MethodGet, "/pipelines/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusForbidden))
	// Should render HTML, not JSON
	assert.Expect(rec.Body.String()).To(gomega.ContainSubstring("Access Denied"))
	assert.Expect(rec.Body.String()).To(gomega.ContainSubstring("You do not have permission"))
	assert.Expect(rec.Body.String()).NotTo(gomega.ContainSubstring(`"error"`))
}

func TestOAuthRBACAPIReturnsJSON(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, `Email == "nobody@example.com"`)

	user := &auth.User{
		Email:    "alice@example.com",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}
	token := generateTestToken(t, user)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusForbidden))

	var body map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(body["error"]).To(gomega.Equal("access denied"))
}

func TestOAuthRBACAllowedUser(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, `NickName == "alice"`)

	user := &auth.User{
		Email:    "alice@example.com",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}
	token := generateTestToken(t, user)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	// Should pass RBAC and reach the handler
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusForbidden))
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}

func TestOAuthRBACEmptyExprAllowsAll(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	user := &auth.User{
		Email:    "anyone@example.com",
		NickName: "anyone",
		Provider: "github",
		UserID:   "99999",
	}
	token := generateTestToken(t, user)

	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	// No RBAC restriction — should pass through
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusForbidden))
	assert.Expect(rec.Code).NotTo(gomega.Equal(http.StatusUnauthorized))
}

// --- CLI device flow tests ---

func TestOAuthCLIBeginReturnsCode(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	req := httptest.NewRequest(http.MethodPost, "/auth/cli/begin", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))

	var body map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(body["code"]).NotTo(gomega.BeEmpty())
	assert.Expect(body["login_url"]).To(gomega.ContainSubstring("/auth/cli/approve?code="))
}

func TestOAuthCLIPollGETMethod(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	// First, begin a CLI login to get a code
	beginReq := httptest.NewRequest(http.MethodPost, "/auth/cli/begin", nil)
	beginRec := httptest.NewRecorder()
	router.ServeHTTP(beginRec, beginReq)
	assert.Expect(beginRec.Code).To(gomega.Equal(http.StatusOK))

	var beginBody map[string]string
	err := json.Unmarshal(beginRec.Body.Bytes(), &beginBody)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	code := beginBody["code"]

	// Poll with GET — should return 202 (pending)
	pollReq := httptest.NewRequest(http.MethodGet, "/auth/cli/poll?code="+code, nil)
	pollRec := httptest.NewRecorder()
	router.ServeHTTP(pollRec, pollReq)
	assert.Expect(pollRec.Code).To(gomega.Equal(http.StatusAccepted))

	var pollBody map[string]string
	err = json.Unmarshal(pollRec.Body.Bytes(), &pollBody)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(pollBody["status"]).To(gomega.Equal("pending"))
}

func TestOAuthCLIPollPOSTNotAllowed(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	// POST to poll should fail with 405
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/poll?code=something", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusMethodNotAllowed))
}

func TestOAuthCLIPollUnknownCode(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	req := httptest.NewRequest(http.MethodGet, "/auth/cli/poll?code=nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusNotFound))

	var body map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(body["error"]).To(gomega.Equal("unknown code"))
}

func TestOAuthCLIApproveRendersTemplate(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	// Begin to get a code
	beginReq := httptest.NewRequest(http.MethodPost, "/auth/cli/begin", nil)
	beginRec := httptest.NewRecorder()
	router.ServeHTTP(beginRec, beginReq)

	var beginBody map[string]string
	err := json.Unmarshal(beginRec.Body.Bytes(), &beginBody)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	code := beginBody["code"]

	// Visit the approve page — should render styled HTML
	approveReq := httptest.NewRequest(http.MethodGet, "/auth/cli/approve?code="+code, nil)
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	assert.Expect(approveRec.Code).To(gomega.Equal(http.StatusOK))

	body := approveRec.Body.String()
	// Should have the styled template, not raw inline HTML
	assert.Expect(body).To(gomega.ContainSubstring("bundle.css"))
	assert.Expect(body).To(gomega.ContainSubstring("Approve CLI"))
	assert.Expect(body).To(gomega.ContainSubstring("Sign in with GitHub"))
	assert.Expect(body).To(gomega.ContainSubstring("cli_code="))
}

func TestOAuthCLIApproveInvalidCode(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, "")

	req := httptest.NewRequest(http.MethodGet, "/auth/cli/approve?code=invalid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusBadRequest))
}

// --- Public routes remain accessible ---

func TestOAuthPublicRoutesAccessible(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, `NickName == "admin"`)

	// Health endpoint should be public (no auth required)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))
	assert.Expect(rec.Body.String()).To(gomega.Equal("OK"))
}

func TestOAuthLoginPageAccessible(t *testing.T) {
	assert := gomega.NewWithT(t)
	router := setupRouterWithOAuth(t, `NickName == "admin"`)

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(gomega.Equal(http.StatusOK))
	assert.Expect(rec.Body.String()).To(gomega.ContainSubstring("Login"))
}
