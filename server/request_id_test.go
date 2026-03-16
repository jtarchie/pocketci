package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	. "github.com/onsi/gomega"
)

func TestRequestIDFromContext(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	ctx := requestContextWithRequestID(context.Background(), "req-123")
	rid, ok := RequestIDFromContext(ctx)
	assert.Expect(ok).To(BeTrue())
	assert.Expect(rid).To(Equal("req-123"))

	_, ok = RequestIDFromContext(context.Background())
	assert.Expect(ok).To(BeFalse())
}

func TestLoggerWithRequestID(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	LoggerWithRequestID(logger, requestContextWithRequestID(context.Background(), "req-xyz")).Info("test-message")

	line := strings.TrimSpace(buf.String())
	assert.Expect(line).NotTo(BeEmpty())

	var payload map[string]any
	err := json.Unmarshal([]byte(line), &payload)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["request_id"]).To(Equal("req-xyz"))
}

func TestSlogMiddlewarePropagatesRequestIDToContext(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	e := echo.New()
	e.Use(middleware.RequestIDWithConfig(middleware.RequestIDConfig{
		RequestIDHandler: func(c *echo.Context, id string) {
			req := c.Request()
			req = req.WithContext(requestContextWithRequestID(req.Context(), id))
			c.SetRequest(req)
		},
	}))
	e.Use(newSlogMiddleware(slog.New(slog.NewTextHandler(io.Discard, nil))))

	e.GET("/test", func(ctx *echo.Context) error {
		rid, ok := RequestIDFromContext(ctx.Request().Context())
		if !ok {
			return ctx.String(http.StatusInternalServerError, "missing")
		}
		return ctx.String(http.StatusOK, rid)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Expect(rec.Code).To(Equal(http.StatusOK))
	assert.Expect(strings.TrimSpace(rec.Body.String())).NotTo(BeEmpty())
}

func TestExecutePipelineLoggerIncludesRequestID(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = buildFile.Close() }()

	initStorage, found := storage.GetFromDSN("sqlite://" + buildFile.Name())
	assert.Expect(found).To(BeTrue())

	store, err := initStorage("sqlite://"+buildFile.Name(), "namespace", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	pipeline, err := store.SavePipeline(context.Background(), "request-id-pipeline", "export const pipeline = async () => {};", "native://", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(context.Background(), pipeline.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	var buf bytes.Buffer
	svc := NewExecutionService(store, slog.New(slog.NewJSONHandler(&buf, nil)), 1, nil)
	svc.wg.Add(1)
	svc.inFlight.Add(1)

	svc.executePipeline(pipeline, run, execOptions{requestID: "req-exec-1"})

	assert.Expect(buf.String()).To(ContainSubstring("\"request_id\":\"req-exec-1\""))
}
