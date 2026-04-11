package server_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/server/auth"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestRequestIDAppearsInLogs(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	router, err := server.NewRouter(logger, client, server.RouterOptions{})
	assert.Expect(err).NotTo(HaveOccurred())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Expect(rec.Code).To(Equal(http.StatusOK))
	assert.Expect(logBuf.String()).To(ContainSubstring("request_id"))
}

func TestBasicAuthMiddlewarePropagatesActorToLogs(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	router, err := server.NewRouter(logger, client, server.RouterOptions{
		BasicAuthUsername: "admin",
		BasicAuthPassword: "secret",
	})
	assert.Expect(err).NotTo(HaveOccurred())

	req := httptest.NewRequest(http.MethodGet, "/pipelines/", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Expect(rec.Code).NotTo(Equal(http.StatusUnauthorized))
	logs := logBuf.String()
	assert.Expect(logs).To(ContainSubstring(`"auth_provider":"basic"`))
	assert.Expect(logs).To(ContainSubstring(`"user":"admin"`))
}

func TestExecutePipelineLoggerIncludesRequestID(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	pipeline, err := client.SavePipeline(context.Background(), "request-id-pipeline", "export const pipeline = async () => {};", "native", "")
	assert.Expect(err).NotTo(HaveOccurred())

	router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

	// Trigger the pipeline via the API endpoint so request_id is propagated
	req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

	router.ExecutionService().Wait()

	// Verify the pipeline was triggered and completed
	triggerResp := mustJSONMap(t, rec)
	runID, ok := triggerResp["run_id"].(string)
	assert.Expect(ok).To(BeTrue())
	assert.Expect(runID).NotTo(BeEmpty())
}

func TestExecutePipelineLoggerIncludesActor(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	pipeline, err := client.SavePipeline(context.Background(), "actor-pipeline", "export const pipeline = async () => {};", "native", "")
	assert.Expect(err).NotTo(HaveOccurred())

	router := newStrictSecretRouter(t, client, server.RouterOptions{
		MaxInFlight:       5,
		BasicAuthUsername: "admin",
		BasicAuthPassword: "secret",
	})

	// Trigger with basic auth so actor info is propagated
	req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

	router.ExecutionService().Wait()

	// Verify the pipeline was triggered and completed
	triggerResp := mustJSONMap(t, rec)
	runID, ok := triggerResp["run_id"].(string)
	assert.Expect(ok).To(BeTrue())
	assert.Expect(runID).NotTo(BeEmpty())
}

func TestRequestActorFromContext(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	ctx := auth.WithRequestActor(context.Background(), auth.RequestActor{Provider: "basic", User: "alice"})
	actor, ok := server.RequestActorFromContext(ctx)
	assert.Expect(ok).To(BeTrue())
	assert.Expect(actor.Provider).To(Equal("basic"))
	assert.Expect(actor.User).To(Equal("alice"))
}

func TestOAuthMiddlewarePropagatesActorToLogs(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

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

	assert.Expect(rec.Code).NotTo(Equal(http.StatusUnauthorized))
	logs := logBuf.String()
	assert.Expect(strings.Contains(logs, `"auth_provider":"github"`)).To(BeTrue())
	assert.Expect(strings.Contains(logs, `"user":"alice@example.com"`)).To(BeTrue())
}
