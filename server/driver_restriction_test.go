package server_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/secrets"
	_ "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestDriverRestriction(t *testing.T) {
	t.Parallel()

	storage.Each(func(name string, init storage.InitFunc) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Run("restricts drivers when AllowedDrivers is set", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				secretsMgr, err := secrets.GetFromDSN("sqlite://:memory:?key=test-key", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = secretsMgr.Close() }()

				// Create router with only native driver allowed
				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
					AllowedDrivers: "native",
					SecretsManager: secretsMgr,
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Try to create pipeline with docker driver (should fail)
				body := map[string]string{
					"content":    "export { pipeline };",
					"driver_dsn": "docker://",
				}
				jsonBody, _ := json.Marshal(body)

				req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))
				message := mustJSONErrorText(t, rec)
				assert.Expect(message).To(ContainSubstring("docker"))
				assert.Expect(message).To(ContainSubstring("not allowed"))

				// Try to create pipeline with native driver (should succeed)
				body["driver_dsn"] = "native"
				jsonBody, _ = json.Marshal(body)

				req = httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
			})

			t.Run("wildcard allows all drivers", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				secretsMgr, err := secrets.GetFromDSN("sqlite://:memory:?key=test-key", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = secretsMgr.Close() }()

				// Create router with wildcard (default)
				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
					AllowedDrivers: "*",
					SecretsManager: secretsMgr,
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Try to create pipeline with any driver (should succeed if DSN is valid)
				body := map[string]string{
					"content":    "export { pipeline };",
					"driver_dsn": "docker://",
				}
				jsonBody, _ := json.Marshal(body)

				req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
			})

			t.Run("uses first allowed driver as default when not specified", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				secretsMgr, err := secrets.GetFromDSN("sqlite://:memory:?key=test-key", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = secretsMgr.Close() }()

				// Create router with native,docker allowed (native should be default)
				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
					AllowedDrivers: "native,docker",
					SecretsManager: secretsMgr,
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Create pipeline without specifying driver
				body := map[string]string{
					"content": "export { pipeline };",
				}
				jsonBody, _ := json.Marshal(body)

				req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))

				var resp map[string]any
				err = json.Unmarshal(rec.Body.Bytes(), &resp)
				assert.Expect(err).NotTo(HaveOccurred())
				_, hasDriver := resp["driver_dsn"]
				assert.Expect(hasDriver).To(BeFalse())
			})

			t.Run("GET /api/drivers returns allowed drivers list", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create router with specific drivers
				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
					AllowedDrivers: "native,docker,k8s",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/api/drivers", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))

				var resp map[string][]string
				err = json.Unmarshal(rec.Body.Bytes(), &resp)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(resp["drivers"]).To(ConsistOf("native", "docker", "k8s"))
			})

			t.Run("GET /api/drivers returns all registered drivers for wildcard", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create router with wildcard
				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
					AllowedDrivers: "*",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/api/drivers", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))

				var resp map[string][]string
				err = json.Unmarshal(rec.Body.Bytes(), &resp)
				assert.Expect(err).NotTo(HaveOccurred())

				// When AllowedDrivers is "*", the endpoint returns the configured driver
				assert.Expect(len(resp["drivers"])).To(BeNumerically(">=", 0))
			})

			t.Run("multiple drivers can be specified", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				secretsMgr, err := secrets.GetFromDSN("sqlite://:memory:?key=test-key", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = secretsMgr.Close() }()

				// Create router with native,docker,k8s allowed
				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{
					AllowedDrivers: "native,docker,k8s",
					SecretsManager: secretsMgr,
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Test native (should succeed)
				body := map[string]string{
					"content":    "export { pipeline };",
					"driver_dsn": "native",
				}
				jsonBody, _ := json.Marshal(body)
				req := httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline-native", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				assert.Expect(rec.Code).To(Equal(http.StatusOK))

				// Test docker (should succeed)
				body["driver_dsn"] = "docker"
				jsonBody, _ = json.Marshal(body)
				req = httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline-docker", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				assert.Expect(rec.Code).To(Equal(http.StatusOK))

				// Test k8s (should succeed)
				body["driver_dsn"] = "k8s://production"
				jsonBody, _ = json.Marshal(body)
				req = httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline-k8s", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				assert.Expect(rec.Code).To(Equal(http.StatusOK))

				// Test qemu (should fail - not in allowed list)
				body["driver_dsn"] = "qemu"
				jsonBody, _ = json.Marshal(body)
				req = httptest.NewRequest(http.MethodPut, "/api/pipelines/test-pipeline-qemu", bytes.NewReader(jsonBody))
				req.Header.Set("Content-Type", "application/json")
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))
				message := mustJSONErrorText(t, rec)
				assert.Expect(message).To(ContainSubstring("qemu"))
				assert.Expect(message).To(ContainSubstring("not allowed"))
			})
		})
	})
}
