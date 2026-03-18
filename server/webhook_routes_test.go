package server_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func computeSignature(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookAPI(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("POST /api/webhooks/:id triggers pipeline and returns 202 on timeout", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Pipeline that doesn't call http.respond() - should timeout to 202
			pipeline, err := client.SavePipeline(context.Background(),
				"webhook-pipeline",
				"export const pipeline = async () => { console.log('running'); };",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:    5,
				WebhookTimeout: 100 * time.Millisecond,
			})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{"test": true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).NotTo(BeEmpty())
			assert.Expect(resp["pipeline_id"]).To(Equal(pipeline.ID))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("webhook returns 404 for non-existent pipeline", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newStrictSecretRouter(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/non-existent", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})

		t.Run("webhook validates X-Webhook-Signature header", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(),
				"secure-pipeline",
				"export const pipeline = async () => {};",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = secretsMgr.Close() }()
			err = secretsMgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "webhook_secret", "my-secret-key")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:    5,
				WebhookTimeout: 100 * time.Millisecond,
				SecretsManager: secretsMgr,
			})

			// Valid signature
			body := `{"event": "push"}`
			sig := computeSignature(body, "my-secret-key")

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(body))
			req.Header.Set("X-Webhook-Signature", sig)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("webhook validates signature query param", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(),
				"secure-pipeline-qp",
				"export const pipeline = async () => {};",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = secretsMgr.Close() }()
			err = secretsMgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "webhook_secret", "query-secret")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:    5,
				WebhookTimeout: 100 * time.Millisecond,
				SecretsManager: secretsMgr,
			})

			body := `{"event": "push"}`
			sig := computeSignature(body, "query-secret")

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID+"?signature="+sig, strings.NewReader(body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("webhook returns 401 on missing signature", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(),
				"secure-pipeline-nosig",
				"export const pipeline = async () => {};",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = secretsMgr.Close() }()
			err = secretsMgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "webhook_secret", "my-secret")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{SecretsManager: secretsMgr})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusUnauthorized))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).To(Equal("webhook signature validation failed"))
		})

		t.Run("webhook returns 401 on invalid signature", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(),
				"secure-pipeline-badsig",
				"export const pipeline = async () => {};",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = secretsMgr.Close() }()
			err = secretsMgr.Set(context.Background(), secrets.PipelineScope(pipeline.ID), "webhook_secret", "correct-secret")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{SecretsManager: secretsMgr})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{}`))
			req.Header.Set("X-Webhook-Signature", "bad-signature-value")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusUnauthorized))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).To(Equal("webhook signature validation failed"))
		})

		t.Run("webhook accepts all requests when no secret configured", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(),
				"open-pipeline",
				"export const pipeline = async () => {};",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:    5,
				WebhookTimeout: 100 * time.Millisecond,
			})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			// Should succeed without any signature
			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("webhook forwards any HTTP method", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(),
				"any-method-pipeline",
				"export const pipeline = async () => {};",
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:    5,
				WebhookTimeout: 100 * time.Millisecond,
			})

			// Test with GET
			req := httptest.NewRequest(http.MethodGet, "/api/webhooks/"+pipeline.ID, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			// Test with PUT
			req = httptest.NewRequest(http.MethodPut, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{}`))
			rec = httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("pipeline can send response via http.respond()", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Pipeline that calls http.respond() immediately
			content := `
					export const pipeline = async () => {
						const req = http.request();
						if (req) {
							http.respond({
								status: 201,
								body: JSON.stringify({ ok: true, method: req.method }),
								headers: { "Content-Type": "application/json", "X-Custom": "value" }
							});
						}
					};
				`

			pipeline, err := client.SavePipeline(context.Background(),
				"respond-pipeline",
				content,
				"native",
				"",
			)
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:    5,
				WebhookTimeout: 5 * time.Second, // Long timeout - pipeline should respond quickly
			})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{"hello": "world"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(201))
			assert.Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			assert.Expect(rec.Header().Get("X-Custom")).To(Equal("value"))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["ok"]).To(BeTrue())
			assert.Expect(resp["method"]).To(Equal("POST"))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})
	})
}
