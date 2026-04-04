package backwards_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	configpkg "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/native"
	_ "github.com/jtarchie/pocketci/resources/mock"
	"github.com/jtarchie/pocketci/runtime/agent"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// TEST_API_KEY must be set before any parallel subtests run. Since all tests
	// use the same fake value, a single os.Setenv at process start is safe.
	_ = os.Setenv("TEST_API_KEY", "fake-test-key")
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// fakeLLMResponse returns a minimal OpenAI-compatible chat completion JSON.
func fakeLLMResponse(content string, promptTokens, completionTokens, totalTokens int) string {
	return fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion","created":1730000000,"model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
		content, promptTokens, completionTokens, totalTokens)
}

// configureFakeLLM points the "test" LLM provider at an httptest server returning the given response.
// fakeLLMBaseURL starts a fake LLM server returning response and returns the base URL to use in RunnerOptions.
// Use this for parallel tests. For sequential tests use configureFakeLLM.
func fakeLLMBaseURL(t *testing.T, response string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	return server.URL + "/v1"
}

func configureFakeLLM(t *testing.T, response string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	orig := agent.DefaultBaseURLs["test"]
	agent.DefaultBaseURLs["test"] = server.URL + "/v1"
	t.Cleanup(func() { agent.DefaultBaseURLs["test"] = orig })
	t.Setenv("TEST_API_KEY", "fake-test-key")
}

// configureFakeLLMWithCapture is like configureFakeLLM but also captures the raw request body.
func configureFakeLLMWithCapture(t *testing.T, response string, body *string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	orig := agent.DefaultBaseURLs["test"]
	agent.DefaultBaseURLs["test"] = server.URL + "/v1"
	t.Cleanup(func() { agent.DefaultBaseURLs["test"] = orig })
	t.Setenv("TEST_API_KEY", "fake-test-key")
}

// configureFakeLLMError points the "test" provider at a server that returns HTTP 500.
func configureFakeLLMError(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"LLM provider unavailable","type":"server_error"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	orig := agent.DefaultBaseURLs["test"]
	agent.DefaultBaseURLs["test"] = server.URL + "/v1"
	t.Cleanup(func() { agent.DefaultBaseURLs["test"] = orig })
	t.Setenv("TEST_API_KEY", "fake-test-key")
}

func debugLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func loadConfig(t *testing.T, path string) *configpkg.Config {
	t.Helper()

	assert := NewGomegaWithT(t)

	contents, err := os.ReadFile(path)
	assert.Expect(err).NotTo(HaveOccurred())

	var cfg configpkg.Config

	err = yaml.UnmarshalWithOptions(contents, &cfg, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())

	return &cfg
}

type driverFactory struct {
	name string
	new  func(namespace string, logger *slog.Logger) (orchestra.Driver, error)
}

var drivers = []driverFactory{
	{
		name: "native",
		new: func(namespace string, logger *slog.Logger) (orchestra.Driver, error) {
			return native.New(context.Background(), native.Config{Namespace: namespace}, logger)
		},
	},
	{
		name: "docker",
		new: func(namespace string, logger *slog.Logger) (orchestra.Driver, error) {
			return docker.New(context.Background(), docker.Config{Namespace: namespace}, logger)
		},
	},
}

func TestTryStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/try.yml")

			logger := discardLogger()

			driver, err := df.new("test-try-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-try", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestDoStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/do.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-do-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-do", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("ensure-task"))
		})
	}
}

func TestOnFailureStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/on_failure.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-failure-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-failure", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("task.failed"))
			assert.Expect(logs.String()).To(ContainSubstring("on-failure-task"))
		})
	}
}

func TestOnErrorStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/on_error.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-error-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-error", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("Task erroring-task errored"))
			assert.Expect(logs.String()).To(ContainSubstring("on-erroring-task"))
		})
	}
}

func TestOnAbortStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/on_abort.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-abort-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-abort", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("Task abort-task aborted"))
			assert.Expect(logs.String()).To(ContainSubstring("on-abort-task"))
		})
	}
}

func TestOnSuccessStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/on_success.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-success-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-success", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("on-success-task"))
		})
	}
}

func TestInParallelStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/in_parallel.yml")

			logger := discardLogger()

			driver, err := df.new("test-in-parallel-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-in-parallel", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestPipelineMaxInFlightStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/pipeline_max_in_flight.yml")

			logger := discardLogger()

			driver, err := df.new("test-pipeline-mif-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-pipeline-mif", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestParallelismStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/parallelism.yml")

			logger := discardLogger()

			driver, err := df.new("test-parallelism-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-parallelism", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestEnsureStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/ensure.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-ensure-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-ensure", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("ensure-task"))
			assert.Expect(logs.String()).To(ContainSubstring("step.ensure.failed"))
		})
	}
}

func TestSkippedSteps(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/skipped_steps.yml")

			logger := discardLogger()

			driver, err := df.new("test-skipped-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-skipped", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify failing-task has "failure" status.
			results, err := store.GetAll(context.Background(), "/pipeline/test-run/", []string{"status"})
			assert.Expect(err).NotTo(HaveOccurred())

			var failingTaskFound bool
			for _, result := range results {
				if strings.Contains(result.Path, "tasks/failing-task") {
					status, ok := result.Payload["status"].(string)
					assert.Expect(ok).To(BeTrue())
					assert.Expect(status).To(Equal("failure"))
					failingTaskFound = true
				}
			}
			assert.Expect(failingTaskFound).To(BeTrue(), "expected failing-task in storage")

			// Verify exactly 2 skipped entries.
			skippedPaths := map[string]bool{}
			for _, result := range results {
				status, ok := result.Payload["status"].(string)
				if ok && status == "skipped" {
					skippedPaths[result.Path] = true
				}
			}
			assert.Expect(skippedPaths).To(HaveLen(2), "expected 2 skipped tasks, got: %v", skippedPaths)

			var foundA, foundB bool
			for path := range skippedPaths {
				if strings.Contains(path, "tasks/skipped-task-a") {
					foundA = true
				}
				if strings.Contains(path, "tasks/skipped-task-b") {
					foundB = true
				}
			}
			assert.Expect(foundA).To(BeTrue(), "expected skipped-task-a in skipped entries")
			assert.Expect(foundB).To(BeTrue(), "expected skipped-task-b in skipped entries")
		})
	}
}

func TestCachesStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/caches.yml")

			logger := discardLogger()

			driver, err := df.new("test-caches-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-caches", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestAttemptsStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/attempts.yml")

			logger := discardLogger()

			driver, err := df.new("test-attempts-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-attempts", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestAcrossStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/across.yml")

			logger := discardLogger()

			driver, err := df.new("test-across-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-across", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestTaskFileStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/task_file.yml")

			logger := discardLogger()

			driver, err := df.new("test-task-file-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-task-file", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestTaskURIStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/task_uri.yml")

			logger := discardLogger()

			driver, err := df.new("test-task-uri-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-task-uri", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestStderrAssertionStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			if df.name == "native" {
				t.Skip("native driver does not separate stderr from stdout")
			}

			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/stderr.yml")

			logger := discardLogger()

			driver, err := df.new("test-stderr-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-stderr", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestValidateResourceTypes(t *testing.T) {
	t.Run("undefined resource type", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/undefined-resource-type.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("resource type"))
	})

	t.Run("valid with resource type", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/valid-with-resource-type.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("valid with default resource type", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/valid-with-default-resource-type.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).NotTo(HaveOccurred())
	})
}

func TestValidateConfig(t *testing.T) {
	t.Run("duplicate job names", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/duplicate-job-names.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("duplicate job name"))
	})

	t.Run("get step references undefined resource", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/undefined-resource-in-get.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("undefined resource"))
	})

	t.Run("passed constraint references unknown job", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/passed-references-unknown-job.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("unknown job"))
	})

	t.Run("circular passed constraint", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/circular-passed-constraint.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("circular passed constraint"))
	})

	t.Run("valid pipeline with passed constraints", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg := loadConfig(t, "validation/valid-with-passed-constraints.yml")
		err := backwards.ValidateConfig(cfg)
		assert.Expect(err).NotTo(HaveOccurred())
	})
}

type stepLocation struct {
	jobIdx  int
	stepIdx int
	name    string
}

func deepCopyConfig(t *testing.T, cfg *configpkg.Config) *configpkg.Config {
	t.Helper()

	assert := NewGomegaWithT(t)

	data, err := yaml.MarshalWithOptions(*cfg)
	assert.Expect(err).NotTo(HaveOccurred())

	var copied configpkg.Config

	err = yaml.UnmarshalWithOptions(data, &copied, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())

	return &copied
}

func collectStepsWithAssertions(cfg *configpkg.Config) []stepLocation {
	var result []stepLocation

	for i := range cfg.Jobs {
		for j := range cfg.Jobs[i].Plan {
			step := &cfg.Jobs[i].Plan[j]
			if step.Assert != nil {
				taskName := step.Task
				if taskName == "" {
					taskName = fmt.Sprintf("step-%d", j)
				}

				result = append(result, stepLocation{
					jobIdx:  i,
					stepIdx: j,
					name:    taskName,
				})
			}
		}
	}

	return result
}

func TestMutateJobAsserts(t *testing.T) {
	// Assertion failure logic is driver-agnostic; native avoids Docker overhead.
	assert := NewGomegaWithT(t)

	matches, err := filepath.Glob("steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	nativeDF := drivers[0]

	for _, match := range matches {
		t.Run(filepath.Base(match), func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, match)
			assert.Expect(cfg.Assert.Execution).NotTo(BeEmpty())

			cfg.Assert.Execution[0] = "unknown-job"

			logger := discardLogger()

			driver, err := nativeDF.new("test-mutate-job-"+strings.TrimSuffix(filepath.Base(match), ".yml"), logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-mutate-job", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			llmURL := fakeLLMBaseURL(t, fakeLLMResponse("stub", 5, 5, 10))

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{AgentBaseURLs: map[string]string{"test": llmURL}})
			err = runner.Run(context.Background())
			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
		})
	}
}

func TestMutateStepAsserts(t *testing.T) {
	// Assertion failure logic is driver-agnostic; native avoids Docker overhead.
	assert := NewGomegaWithT(t)

	matches, err := filepath.Glob("steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	nativeDF := drivers[0]

	for _, match := range matches {
		t.Run(filepath.Base(match), func(t *testing.T) {
			assert := NewGomegaWithT(t)
			cfg := loadConfig(t, match)
			locations := collectStepsWithAssertions(cfg)
			assert.Expect(locations).NotTo(BeEmpty(), "every step YAML must have at least one step-level assertion")

			for _, loc := range locations {
				t.Run(loc.name, func(t *testing.T) {
					assert := NewGomegaWithT(t)

					mutated := deepCopyConfig(t, cfg)
					step := &mutated.Jobs[loc.jobIdx].Plan[loc.stepIdx]

					if step.Assert.Code != nil {
						wrongCode := *step.Assert.Code + 1
						step.Assert.Code = &wrongCode
					} else if step.Assert.Stdout != "" {
						step.Assert.Stdout = "THIS-WILL-NOT-MATCH-" + step.Assert.Stdout
					} else if step.Assert.Stderr != "" {
						step.Assert.Stderr = "THIS-WILL-NOT-MATCH-" + step.Assert.Stderr
					}

					logger := discardLogger()

					driver, err := nativeDF.new("test-mutate-step-"+strings.TrimSuffix(filepath.Base(match), ".yml")+"-"+loc.name, logger)
					assert.Expect(err).NotTo(HaveOccurred())

					defer func() { _ = driver.Close() }()

					store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-mutate-step", logger)
					assert.Expect(err).NotTo(HaveOccurred())

					defer func() { _ = store.Close() }()

					llmURL := fakeLLMBaseURL(t, fakeLLMResponse("stub", 5, 5, 10))

					runner := backwards.New(mutated, driver, store, logger, "test-run", "", backwards.RunnerOptions{AgentBaseURLs: map[string]string{"test": llmURL}})
					err = runner.Run(context.Background())
					assert.Expect(err).To(HaveOccurred())
					assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
				})
			}
		})
	}
}

func TestCrossRunPassed(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			t.Run("within-run cascade", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				cfg := loadConfig(t, "steps/cross_run_passed.yml")
				logger := discardLogger()

				driver, err := df.new("test-cross-run-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = driver.Close() }()

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-cross-run", logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = store.Close() }()

				runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())
			})

			t.Run("second run uses prior run status", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				storagePath := filepath.Join(t.TempDir(), "cross-run.db")
				logger := discardLogger()

				cfg := loadConfig(t, "steps/cross_run_passed.yml")

				driver, err := df.new("test-cross-run2-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = driver.Close() }()

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, "test-cross-ns", logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = store.Close() }()

				// Run 1: both build and deploy execute via within-run cascade
				runner1 := backwards.New(cfg, driver, store, logger, "run-1", "", backwards.RunnerOptions{})
				err = runner1.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				// Run 2: deploy's passed constraint satisfied by run-1's build success
				runner2 := backwards.New(cfg, driver, store, logger, "run-2", "", backwards.RunnerOptions{})
				err = runner2.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				// Verify both runs wrote job statuses
				results1, err := store.GetAll(context.Background(), "/pipeline/run-1/jobs/", []string{"status"})
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(len(results1)).To(BeNumerically(">=", 2))

				results2, err := store.GetAll(context.Background(), "/pipeline/run-2/jobs/", []string{"status"})
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(len(results2)).To(BeNumerically(">=", 2))

				// Verify cross-run queries
				status, err := store.GetMostRecentJobStatus(context.Background(), "", "build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("success"))

				status, err = store.GetMostRecentJobStatus(context.Background(), "", "deploy")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("success"))
			})

			t.Run("both jobs succeed in storage", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				storagePath := filepath.Join(t.TempDir(), "blocked.db")
				logger := discardLogger()

				cfg := loadConfig(t, "steps/cross_run_passed.yml")

				driver, err := df.new("test-cross-run3-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = driver.Close() }()

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, "test-blocked-ns", logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = store.Close() }()

				runner := backwards.New(cfg, driver, store, logger, "run-blocked", "", backwards.RunnerOptions{})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				buildPayload, err := store.Get(context.Background(), "/pipeline/run-blocked/jobs/build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(buildPayload["status"]).To(Equal("success"))

				deployPayload, err := store.Get(context.Background(), "/pipeline/run-blocked/jobs/deploy")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(deployPayload["status"]).To(Equal("success"))
			})
		})
	}
}

func TestGetVersionModes(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/version_modes.yml")
			logger := discardLogger()

			driver, err := df.new("test-get-modes-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-get-modes", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestGetMockEvery(t *testing.T) {
	// This test uses the native mock resource's incrementing counter.
	// The concourse/mock-resource Docker image behaves differently,
	// so this test only runs on the native driver.
	assert := NewGomegaWithT(t)

	cfg := loadConfig(t, "steps/mock_every.yml")
	logger := discardLogger()

	driver, err := native.New(context.Background(), native.Config{Namespace: "test-mock-every"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = driver.Close() }()

	storagePath := filepath.Join(t.TempDir(), "mock-every.db")

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, "test-mock-every-ns", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	// Run 1: should fetch the first version.
	runner := backwards.New(cfg, driver, store, logger, "run-1", "", backwards.RunnerOptions{})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	versions1, err := backwards.ListResourceVersions(context.Background(), store, "default/counter", 0)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(len(versions1)).To(BeNumerically(">=", 1))

	// Run 2: should fetch a new version (counter increments).
	runner2 := backwards.New(cfg, driver, store, logger, "run-2", "", backwards.RunnerOptions{})
	err = runner2.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	versions2, err := backwards.ListResourceVersions(context.Background(), store, "default/counter", 0)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(len(versions2)).To(BeNumerically(">", len(versions1)))
}

func TestPutBasic(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/put_basic.yml")
			logger := discardLogger()

			driver, err := df.new("test-put-basic-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-put-basic", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify version was persisted.
			versions, err := backwards.ListResourceVersions(context.Background(), store, "default/my-output", 0)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(len(versions)).To(Equal(1))
			assert.Expect(versions[0].Version["version"]).To(Equal("42"))
		})
	}
}

func TestPrewritePendingJobs(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			t.Run("dependsOn metadata persists after execution", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				cfg := loadConfig(t, "steps/cross_run_passed.yml")
				logger := discardLogger()

				driver, err := df.new("test-prewrite-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = driver.Close() }()

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-prewrite", logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = store.Close() }()

				runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				// build has no passed constraints -> dependsOn is empty
				buildPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(buildPayload["status"]).To(Equal("success"))
				assert.Expect(buildPayload["dependsOn"]).To(Equal([]any{}))

				// deploy depends on build via passed constraint
				deployPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/deploy")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(deployPayload["status"]).To(Equal("success"))
				assert.Expect(deployPayload["dependsOn"]).To(Equal([]any{"build"}))
			})

			t.Run("blockedBy metadata for unsatisfied dependencies", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				// Use a pipeline where deploy depends on build via passed,
				// but remove the build job so deploy never runs and the
				// pre-write pending record with blockedBy is the final state.
				cfg := loadConfig(t, "steps/cross_run_passed.yml")

				// Keep only the deploy job (remove build) so build never
				// executes and deploy stays pending with blockedBy.
				var deployOnly []configpkg.Job

				for _, j := range cfg.Jobs {
					if j.Name == "deploy" {
						deployOnly = append(deployOnly, j)
					}
				}

				cfg.Jobs = deployOnly
				cfg.Assert.Execution = nil // remove pipeline assertions since build won't run

				logger := discardLogger()

				driver, err := df.new("test-prewrite-blocked-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = driver.Close() }()

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-prewrite-blocked", logger)
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = store.Close() }()

				runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				// deploy was never executed because build (its dependency)
				// never ran; the pre-write pending record is the final state.
				deployPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/deploy")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(deployPayload["status"]).To(Equal("pending"))
				assert.Expect(deployPayload["dependsOn"]).To(Equal([]any{"build"}))

				// blockedBy should list build with "never-run" status
				blockedBy, ok := deployPayload["blockedBy"].([]any)
				assert.Expect(ok).To(BeTrue(), fmt.Sprintf("expected blockedBy to be []any, got %T", deployPayload["blockedBy"]))
				assert.Expect(blockedBy).To(HaveLen(1))

				entry, ok := blockedBy[0].(map[string]any)
				assert.Expect(ok).To(BeTrue())
				assert.Expect(entry["job"]).To(Equal("build"))
				assert.Expect(entry["lastStatus"]).To(Equal("never-run"))
			})
		})
	}
}

func TestJobParams(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/job_params.yml")
			logger := discardLogger()

			driver, err := df.new("test-job-params-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-job-params", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{
				WebhookData: &jsapi.WebhookData{
					Provider: "github", EventType: "push", Method: "POST",
					Headers: map[string]string{}, Query: map[string]string{}, Body: "{}",
				},
			})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestTaskURIHTTPStep(t *testing.T) {
	taskYAML := `
image_resource:
  type: registry-image
  source:
    repository: busybox
run:
  path: sh
  args: ["-c", "echo HTTP-SUCCESS"]
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = fmt.Fprint(w, taskYAML)
	}))
	defer server.Close()

	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)
			logger := discardLogger()

			exitCode := 0
			cfg := &configpkg.Config{
				Jobs: configpkg.Jobs{
					{
						Name: "http-job",
						Plan: configpkg.Steps{
							{
								Task: "http-task",
								URI:  server.URL + "/task.yml",
								Assert: &struct {
									Code   *int   `yaml:"code,omitempty"`
									Stderr string `yaml:"stderr,omitempty"`
									Stdout string `yaml:"stdout,omitempty"`
								}{
									Code:   &exitCode,
									Stdout: "HTTP-SUCCESS",
								},
							},
						},
					},
				},
			}

			driver, err := df.new("test-http-uri-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-http-uri", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify storage has load-uri entry with success status.
			payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/http-job/0/load-uri")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(payload["status"]).To(Equal("success"))
			assert.Expect(payload["uri"]).To(Equal(server.URL + "/task.yml"))
			assert.Expect(payload["elapsed"]).NotTo(BeEmpty())
		})
	}
}

func TestTaskURIHTTPErrorStep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)
			logger := discardLogger()

			cfg := &configpkg.Config{
				Jobs: configpkg.Jobs{
					{
						Name: "http-error-job",
						Plan: configpkg.Steps{
							{
								Task: "http-error-task",
								URI:  server.URL + "/missing.yml",
							},
						},
					},
				},
			}

			driver, err := df.new("test-http-err-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-http-err", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("http-error-task errored"))

			// Verify storage has load-uri entry with failure status.
			payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/http-error-job/0/load-uri")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(payload["status"]).To(Equal("failure"))
			assert.Expect(payload["errorMessage"]).To(ContainSubstring("HTTP 404"))
		})
	}
}

func TestTaskFileStorageTracking(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/task_file.yml")
			logger := discardLogger()

			driver, err := df.new("test-file-track-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-file-track", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// The second step (index 1) loads from file.
			payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/success-job/1/load-file")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(payload["status"]).To(Equal("success"))
			assert.Expect(payload["file"]).To(Equal("task-output/task_file.yml"))
			assert.Expect(payload["volume"]).To(Equal("task-output"))
			assert.Expect(payload["elapsed"]).NotTo(BeEmpty())
		})
	}
}

func TestTaskURIFileSchemeStorageTracking(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/task_uri.yml")
			logger := discardLogger()

			driver, err := df.new("test-uri-file-track-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-uri-file-track", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// file:// URIs delegate to trackLoadFile, so storage key is load-file.
			payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/success-job/1/load-file")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(payload["status"]).To(Equal("success"))
			assert.Expect(payload["file"]).To(Equal("task-output/task_file.yml"))
			assert.Expect(payload["volume"]).To(Equal("task-output"))
		})
	}
}

func TestNotifyStepSingle(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	var mu sync.Mutex

	var received int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := discardLogger()
	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"test-webhook": {Type: "http", URL: server.URL},
	})
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "test-pipeline",
		Status:       "pending",
	})

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-notify-single", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "notify-job",
			Plan: configpkg.Steps{{
				Notify:  "test-webhook",
				Message: "Build completed",
			}},
		}},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	mu.Lock()
	defer mu.Unlock()

	assert.Expect(received).To(Equal(1))

	payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/notify-job/0/notify/test-webhook")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("success"))
}

func TestNotifyStepMultiple(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	var mu sync.Mutex

	var received int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := discardLogger()
	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"webhook-1": {Type: "http", URL: server.URL + "/hook1"},
		"webhook-2": {Type: "http", URL: server.URL + "/hook2"},
	})
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "test-pipeline",
		Status:       "pending",
	})

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-notify-multi", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "notify-job",
			Plan: configpkg.Steps{{
				Notify:  []any{"webhook-1", "webhook-2"},
				Message: "Deploy done",
			}},
		}},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	mu.Lock()
	defer mu.Unlock()

	assert.Expect(received).To(Equal(2))

	payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/notify-job/0/notify/webhook-1-webhook-2")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("success"))
}

func TestNotifyStepFailure(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	logger := discardLogger()
	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"failing-webhook": {Type: "http", URL: server.URL},
	})
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "test-pipeline",
		Status:       "pending",
	})

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-notify-fail", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "notify-job",
			Plan: configpkg.Steps{{
				Notify:  "failing-webhook",
				Message: "This should fail",
			}},
		}},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier})
	err = runner.Run(context.Background())
	assert.Expect(err).To(HaveOccurred())

	payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/notify-job/0/notify/failing-webhook")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("failure"))
}

func TestNotifyStepAsync(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	var mu sync.Mutex

	var received int

	done := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()

		select {
		case done <- struct{}{}:
		default:
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := discardLogger()
	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"async-webhook": {Type: "http", URL: server.URL},
	})
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "test-pipeline",
		Status:       "pending",
	})

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-notify-async", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "notify-job",
			Plan: configpkg.Steps{{
				Notify:  "async-webhook",
				Message: "Async notification",
				Async:   true,
			}},
		}},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	// Storage should be success immediately (async doesn't block).
	payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/notify-job/0/notify/async-webhook")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("success"))

	// Wait for the async goroutine to complete.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async notification")
	}

	mu.Lock()
	defer mu.Unlock()

	assert.Expect(received).To(Equal(1))
}

func TestNotifyStepNilNotifier(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-notify-nil", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "notify-job",
			Plan: configpkg.Steps{{
				Notify:  "some-webhook",
				Message: "Should fail",
			}},
		}},
	}

	// Pass nil notifier.
	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{})
	err = runner.Run(context.Background())
	assert.Expect(err).To(HaveOccurred())

	payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/notify-job/0/notify/some-webhook")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("failure"))
}

func TestNotifyYAMLParsing(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Single string form.
	singleYAML := []byte(`notify: slack-channel
message: "Build done"
async: false`)

	var step configpkg.Step

	err := yaml.UnmarshalWithOptions(singleYAML, &step, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(step.NotifyNames()).To(Equal([]string{"slack-channel"}))
	assert.Expect(step.Message).To(Equal("Build done"))
	assert.Expect(step.Async).To(BeFalse())

	// List of strings form.
	multiYAML := []byte(`notify:
  - slack-channel
  - teams-webhook
message: "Build done"`)

	var step2 configpkg.Step

	err = yaml.UnmarshalWithOptions(multiYAML, &step2, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(step2.NotifyNames()).To(Equal([]string{"slack-channel", "teams-webhook"}))
	assert.Expect(step2.Message).To(Equal("Build done"))
}

func TestNotifyStepIntegration(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	var mu sync.Mutex

	var received int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-notify-int", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"hook-1": {Type: "http", URL: server.URL + "/hook1"},
		"hook-2": {Type: "http", URL: server.URL + "/hook2"},
	})
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "integration-pipeline",
		Status:       "pending",
	})

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "notify-multi-step",
			Plan: configpkg.Steps{
				{
					Notify:  "hook-1",
					Message: "First notification",
				},
				{
					Notify:  "hook-2",
					Message: "Second notification",
				},
			},
		}},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	mu.Lock()
	defer mu.Unlock()

	assert.Expect(received).To(Equal(2))

	// Verify both steps recorded success in storage.
	payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/notify-multi-step/0/notify/hook-1")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("success"))

	payload, err = store.Get(context.Background(), "/pipeline/test-run/jobs/notify-multi-step/1/notify/hook-2")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("success"))
}

func TestTargetedJobsRunsOnlyTargeted(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	var mu sync.Mutex

	var received int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := discardLogger()
	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"test-hook": {Type: "http", URL: server.URL},
	})
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "test-pipeline",
		Status:       "pending",
	})

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-targeted", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{
			{Name: "job-a", Plan: configpkg.Steps{{Notify: "test-hook", Message: "a"}}},
			{Name: "job-b", Plan: configpkg.Steps{{Notify: "test-hook", Message: "b"}}},
			{Name: "job-c", Plan: configpkg.Steps{{Notify: "test-hook", Message: "c"}}},
		},
		Assert: struct {
			Execution []string `yaml:"execution,omitempty"`
		}{
			Execution: []string{"job-b"},
		},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier, TargetJobs: []string{"job-b"}})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	mu.Lock()
	defer mu.Unlock()

	assert.Expect(received).To(Equal(1))

	bPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/job-b")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(bPayload["status"]).To(Equal("success"))

	aPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/job-a")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(aPayload["status"]).To(Equal("skipped"))

	cPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/job-c")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(cPayload["status"]).To(Equal("skipped"))
}

func TestTargetedJobsNotFound(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-targeted-nf", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{
			{Name: "real-job", Plan: configpkg.Steps{{Notify: "hook", Message: "x"}}},
		},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{TargetJobs: []string{"nonexistent-job"}})
	err = runner.Run(context.Background())
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring(`target job "nonexistent-job" not found`))
}

func TestTargetedJobsUnsatisfiedPassedSkipped(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-targeted-skip", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	cfg := &configpkg.Config{
		Resources:     configpkg.Resources{{Name: "src", Type: "mock"}},
		ResourceTypes: configpkg.ResourceTypes{{Name: "mock", Type: "registry-image", Source: map[string]any{"repository": "mock"}}},
		Jobs: []configpkg.Job{
			{Name: "build", Plan: configpkg.Steps{{Notify: "hook", Message: "build"}}},
			{Name: "deploy", Plan: configpkg.Steps{{
				Get:       "src",
				GetConfig: configpkg.GetConfig{Passed: []string{"build"}},
			}}},
		},
	}

	runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{TargetJobs: []string{"deploy"}})
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	deployPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/deploy")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(deployPayload["status"]).To(Equal("skipped"))
	assert.Expect(deployPayload["reason"]).To(Equal("passed constraints not satisfied"))

	buildPayload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/build")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(buildPayload["status"]).To(Equal("skipped"))
}

func TestTargetedJobsCascade(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/targeted_cascade.yml")
			// Override assertion to match targeted cascade from "build"
			cfg.Assert.Execution = []string{"build", "test", "deploy"}
			logger := discardLogger()

			driver, err := df.new("test-targeted-cascade-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-targeted-cascade", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{TargetJobs: []string{"build"}})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			for _, jobName := range []string{"build", "test", "deploy"} {
				payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/"+jobName)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(payload["status"]).To(Equal("success"), "job %s should be success", jobName)
			}
		})
	}
}

func TestTargetedJobsAssertionsValidate(t *testing.T) {
	t.Parallel()

	t.Run("correct assertion passes", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		logger := discardLogger()
		notifier := jsapi.NewNotifier(logger)
		notifier.SetConfigs(map[string]jsapi.NotifyConfig{
			"hook": {Type: "http", URL: server.URL},
		})
		notifier.SetContext(jsapi.NotifyContext{PipelineName: "test", Status: "pending"})

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-assert-ok", logger)
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = store.Close() }()

		cfg := &configpkg.Config{
			Jobs: []configpkg.Job{
				{Name: "job-a", Plan: configpkg.Steps{{Notify: "hook", Message: "a"}}},
				{Name: "job-b", Plan: configpkg.Steps{{Notify: "hook", Message: "b"}}},
			},
			Assert: struct {
				Execution []string `yaml:"execution,omitempty"`
			}{
				Execution: []string{"job-a"},
			},
		}

		runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier, TargetJobs: []string{"job-a"}})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("wrong assertion fails", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		logger := discardLogger()
		notifier := jsapi.NewNotifier(logger)
		notifier.SetConfigs(map[string]jsapi.NotifyConfig{
			"hook": {Type: "http", URL: server.URL},
		})
		notifier.SetContext(jsapi.NotifyContext{PipelineName: "test", Status: "pending"})

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-assert-fail", logger)
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = store.Close() }()

		cfg := &configpkg.Config{
			Jobs: []configpkg.Job{
				{Name: "job-a", Plan: configpkg.Steps{{Notify: "hook", Message: "a"}}},
				{Name: "job-b", Plan: configpkg.Steps{{Notify: "hook", Message: "b"}}},
			},
			Assert: struct {
				Execution []string `yaml:"execution,omitempty"`
			}{
				Execution: []string{"job-b"},
			},
		}

		runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{Notifier: notifier, TargetJobs: []string{"job-a"}})
		err = runner.Run(context.Background())
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("assertion"))
	})
}

func TestTargetedJobsCrossRunPassedSatisfied(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			storagePath := filepath.Join(t.TempDir(), "targeted-cross-run.db")
			logger := discardLogger()

			driver, err := df.new("test-targeted-cross-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, "test-targeted-cross", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			// Run 1: full execution (no targeting) — all three jobs run.
			cfg1 := loadConfig(t, "steps/targeted_cascade.yml")

			runner1 := backwards.New(cfg1, driver, store, logger, "run-1", "", backwards.RunnerOptions{})
			err = runner1.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Run 2: target only "deploy" — its passed constraint on "test"
			// is satisfied from run-1 via cross-run storage lookup.
			cfg2 := loadConfig(t, "steps/targeted_cascade.yml")
			cfg2.Assert.Execution = []string{"deploy"}

			runner2 := backwards.New(cfg2, driver, store, logger, "run-2", "", backwards.RunnerOptions{TargetJobs: []string{"deploy"}})
			err = runner2.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			deployPayload, err := store.Get(context.Background(), "/pipeline/run-2/jobs/deploy")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(deployPayload["status"]).To(Equal("success"))

			// build and test should be skipped in run-2
			for _, jobName := range []string{"build", "test"} {
				payload, err := store.Get(context.Background(), "/pipeline/run-2/jobs/"+jobName)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(payload["status"]).To(Equal("skipped"), "job %s should be skipped in run-2", jobName)
			}
		})
	}
}

func TestTargetedJobsEmptyTargetRunsAll(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/cross_run_passed.yml")
			logger := discardLogger()

			driver, err := df.new("test-targeted-empty-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-targeted-empty", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			// Empty target list should behave like nil — run all jobs.
			runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{TargetJobs: []string{}})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			for _, jobName := range []string{"build", "deploy"} {
				payload, err := store.Get(context.Background(), "/pipeline/test-run/jobs/"+jobName)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(payload["status"]).To(Equal("success"), "job %s should be success", jobName)
			}
		})
	}
}

func TestGateApproved(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-gate-approved", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	ctx := context.Background()

	pipeline, err := store.SavePipeline(ctx, "test-pipeline", "{}", "native", "yaml")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "test", storage.TriggerInput{})
	assert.Expect(err).NotTo(HaveOccurred())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := jsapi.NewNotifier(logger)
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"hook": {Type: "http", URL: server.URL},
	})
	notifier.SetContext(jsapi.NotifyContext{PipelineName: "test", Status: "pending"})

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "gated-job",
			Gate: &configpkg.GateConfig{Message: "proceed?"},
			Plan: configpkg.Steps{{Notify: "hook", Message: "done"}},
		}},
	}

	// Approve the gate asynchronously after the runner saves it.
	go func() {
		for {
			time.Sleep(50 * time.Millisecond)

			gates, err := store.GetGatesByRunID(ctx, run.ID)
			if err != nil || len(gates) == 0 {
				continue
			}

			_ = store.ResolveGate(ctx, gates[0].ID, storage.GateStatusApproved, "tester")

			return
		}
	}()

	runner := backwards.New(cfg, nil, store, logger, run.ID, pipeline.ID, backwards.RunnerOptions{Notifier: notifier})
	err = runner.Run(ctx)
	assert.Expect(err).NotTo(HaveOccurred())

	payload, err := store.Get(ctx, "/pipeline/"+run.ID+"/jobs/gated-job")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(payload["status"]).To(Equal("success"))
}

func TestGateRejected(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-gate-rejected", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	ctx := context.Background()

	pipeline, err := store.SavePipeline(ctx, "test-pipeline", "{}", "native", "yaml")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "test", storage.TriggerInput{})
	assert.Expect(err).NotTo(HaveOccurred())

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "gated-job",
			Gate: &configpkg.GateConfig{Message: "deploy to prod?"},
			Plan: configpkg.Steps{{Notify: "hook", Message: "should not run"}},
		}},
	}

	// Reject the gate asynchronously.
	go func() {
		for {
			time.Sleep(50 * time.Millisecond)

			gates, err := store.GetGatesByRunID(ctx, run.ID)
			if err != nil || len(gates) == 0 {
				continue
			}

			_ = store.ResolveGate(ctx, gates[0].ID, storage.GateStatusRejected, "reviewer")

			return
		}
	}()

	runner := backwards.New(cfg, nil, store, logger, run.ID, pipeline.ID, backwards.RunnerOptions{})
	err = runner.Run(ctx)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("rejected"))
	assert.Expect(err.Error()).To(ContainSubstring("reviewer"))
}

func TestGateTimeout(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-gate-timeout", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	ctx := context.Background()

	pipeline, err := store.SavePipeline(ctx, "test-pipeline", "{}", "native", "yaml")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "test", storage.TriggerInput{})
	assert.Expect(err).NotTo(HaveOccurred())

	cfg := &configpkg.Config{
		Jobs: []configpkg.Job{{
			Name: "gated-job",
			Gate: &configpkg.GateConfig{
				Message: "approve?",
				Timeout: "100ms",
			},
			Plan: configpkg.Steps{{Notify: "hook", Message: "should not run"}},
		}},
	}

	runner := backwards.New(cfg, nil, store, logger, run.ID, pipeline.ID, backwards.RunnerOptions{})
	err = runner.Run(ctx)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("timed out"))
}

// ── Webhook Trigger Filter/Dedup/Params ─────────────────────────────────────

func githubWebhookData() *jsapi.WebhookData {
	return &jsapi.WebhookData{
		Provider:  "github",
		EventType: "push",
		Method:    "POST",
		Headers:   map[string]string{"X-GitHub-Delivery": "abc-123"},
		Query:     map[string]string{},
		Body:      "{}",
	}
}

func newNativeDriver(t *testing.T, namespace string) orchestra.Driver {
	t.Helper()

	driver, err := native.New(context.Background(), native.Config{Namespace: namespace}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = driver.Close() })

	return driver
}

func TestWebhookFilter(t *testing.T) {
	t.Run("manual trigger always runs with filter set", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-filter-manual")

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "filtered-job",
				Triggers: &configpkg.Triggers{
					Webhook: &configpkg.WebhookTriggerConfig{
						Filter: `provider == "github"`,
					},
				},
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-filter", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// nil WebhookData = manual trigger, job should run
		runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		results, err := store.GetAll(context.Background(), "/pipeline/test-run/jobs/filtered-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty())
		assert.Expect(results[len(results)-1].Payload["status"]).To(Equal("success"))
	})

	t.Run("matching expression runs the job", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-filter-match")

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "filtered-job",
				Triggers: &configpkg.Triggers{
					Webhook: &configpkg.WebhookTriggerConfig{
						Filter: `provider == "github"`,
					},
				},
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-filter", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{
			WebhookData: githubWebhookData(),
		})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		results, err := store.GetAll(context.Background(), "/pipeline/test-run/jobs/filtered-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty())
		assert.Expect(results[len(results)-1].Payload["status"]).To(Equal("success"))
	})

	t.Run("non-matching expression skips the job", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "filtered-job",
				Triggers: &configpkg.Triggers{
					Webhook: &configpkg.WebhookTriggerConfig{
						Filter: `provider == "github"`,
					},
				},
				Plan: configpkg.Steps{{
					Task: "failing-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "sh", Args: []string{"-c", "exit 1"}},
					},
				}},
			}},
		}

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-filter", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// Slack provider does not match filter — job should be skipped, NOT fail
		runner := backwards.New(cfg, nil, store, logger, "test-run", "", backwards.RunnerOptions{
			WebhookData: &jsapi.WebhookData{
				Provider: "slack", EventType: "message", Method: "POST",
				Headers: map[string]string{}, Query: map[string]string{}, Body: "{}",
			},
		})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred()) // skipped, not failed

		results, err := store.GetAll(context.Background(), "/pipeline/test-run/jobs/filtered-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty())
		assert.Expect(results[len(results)-1].Payload["status"]).To(Equal("skipped"))
	})

	t.Run("no filter with webhook present runs the job", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-filter-nofilter")

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "no-filter-job",
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-filter", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{
			WebhookData: githubWebhookData(),
		})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("deprecated webhook_trigger field works", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-filter-deprecated")

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name:           "deprecated-filter-job",
				WebhookTrigger: `provider == "github"`,
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-filter", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{
			WebhookData: githubWebhookData(),
		})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
	})
}

func TestWebhookParams(t *testing.T) {
	t.Run("provider and eventType injected as env vars", func(t *testing.T) {
		for _, df := range drivers {
			t.Run(df.name, func(t *testing.T) {
				assert := NewGomegaWithT(t)
				logger := discardLogger()

				driver, err := df.new("test-wh-params-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = driver.Close() }()

				cfg := &configpkg.Config{
					Jobs: configpkg.Jobs{{
						Name: "params-job",
						Triggers: &configpkg.Triggers{
							Webhook: &configpkg.WebhookTriggerConfig{
								Params: map[string]string{
									"MY_PROVIDER": "provider",
									"MY_EVENT":    "eventType",
								},
							},
						},
						Plan: configpkg.Steps{{
							Task: "check-params",
							TaskConfig: &configpkg.TaskConfig{
								Platform:      "linux",
								ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
								Run: &configpkg.TaskConfigRun{
									Path: "sh",
									Args: []string{"-c", `test "$MY_PROVIDER" = "github" && test "$MY_EVENT" = "pull_request"`},
								},
							},
						}},
					}},
				}

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-wh-params", logger)
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = store.Close() }()

				runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{
					WebhookData: &jsapi.WebhookData{
						Provider: "github", EventType: "pull_request", Method: "POST",
						Headers: map[string]string{}, Query: map[string]string{}, Body: "{}",
					},
				})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())
			})
		}
	})

	t.Run("payload fields injected as env vars", func(t *testing.T) {
		for _, df := range drivers {
			t.Run(df.name, func(t *testing.T) {
				assert := NewGomegaWithT(t)
				logger := discardLogger()

				driver, err := df.new("test-wh-payload-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = driver.Close() }()

				cfg := &configpkg.Config{
					Jobs: configpkg.Jobs{{
						Name: "payload-job",
						Triggers: &configpkg.Triggers{
							Webhook: &configpkg.WebhookTriggerConfig{
								Params: map[string]string{
									"PR_NUMBER": `string(payload.number)`,
									"PR_REPO":   `"https://github.com/" + payload.pull_request.head.repo.full_name + ".git"`,
								},
							},
						},
						Plan: configpkg.Steps{{
							Task: "check-payload",
							TaskConfig: &configpkg.TaskConfig{
								Platform:      "linux",
								ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
								Run: &configpkg.TaskConfigRun{
									Path: "sh",
									Args: []string{"-c", `test "$PR_NUMBER" = "42" && test "$PR_REPO" = "https://github.com/org/repo.git"`},
								},
							},
						}},
					}},
				}

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-wh-payload", logger)
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = store.Close() }()

				body := `{"number":42,"pull_request":{"head":{"repo":{"full_name":"org/repo"}}}}`
				runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{
					WebhookData: &jsapi.WebhookData{
						Provider: "github", EventType: "pull_request", Method: "POST",
						Headers: map[string]string{}, Query: map[string]string{}, Body: body,
					},
				})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())
			})
		}
	})

	t.Run("empty params on manual trigger", func(t *testing.T) {
		for _, df := range drivers {
			t.Run(df.name, func(t *testing.T) {
				assert := NewGomegaWithT(t)
				logger := discardLogger()

				driver, err := df.new("test-wh-manual-"+df.name, logger)
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = driver.Close() }()

				cfg := &configpkg.Config{
					Jobs: configpkg.Jobs{{
						Name: "manual-params-job",
						Triggers: &configpkg.Triggers{
							Webhook: &configpkg.WebhookTriggerConfig{
								Params: map[string]string{
									"MY_PROVIDER": "provider",
								},
							},
						},
						Plan: configpkg.Steps{{
							Task: "echo-task",
							TaskConfig: &configpkg.TaskConfig{
								Platform:      "linux",
								ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
								Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
							},
						}},
					}},
				}

				store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-manual-params", logger)
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = store.Close() }()

				// nil WebhookData = manual trigger; params should be empty, job should still run
				runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
				err = runner.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())
			})
		}
	})
}

func TestWebhookDedup(t *testing.T) {
	t.Run("manual trigger is never a duplicate", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-dedup-manual")

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "dedup-job",
				Triggers: &configpkg.Triggers{
					Webhook: &configpkg.WebhookTriggerConfig{
						DedupKey: `headers["X-GitHub-Delivery"]`,
					},
				},
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-dedup", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		runner := backwards.New(cfg, driver, store, logger, "test-run", "", backwards.RunnerOptions{})
		err = runner.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		results, err := store.GetAll(context.Background(), "/pipeline/test-run/jobs/dedup-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty())
		assert.Expect(results[len(results)-1].Payload["status"]).To(Equal("success"))
	})

	t.Run("first call runs, second call is duplicate", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-dedup-dup")

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-dedup", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		pipeline, err := store.SavePipeline(context.Background(), "test-dedup-pipeline", "content", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "dedup-job",
				Triggers: &configpkg.Triggers{
					Webhook: &configpkg.WebhookTriggerConfig{
						DedupKey: `headers["X-GitHub-Delivery"]`,
					},
				},
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		webhookData := &jsapi.WebhookData{
			Provider: "github", EventType: "push", Method: "POST",
			Headers: map[string]string{"X-GitHub-Delivery": "abc-123"},
			Query:   map[string]string{}, Body: "{}",
		}

		// Run 1: not a duplicate
		runner1 := backwards.New(cfg, driver, store, logger, "run-1", pipeline.ID, backwards.RunnerOptions{
			WebhookData: webhookData,
		})
		err = runner1.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		results1, err := store.GetAll(context.Background(), "/pipeline/run-1/jobs/dedup-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results1).NotTo(BeEmpty())
		assert.Expect(results1[len(results1)-1].Payload["status"]).To(Equal("success"))

		// Run 2: same webhook data — should be a duplicate (skipped)
		runner2 := backwards.New(cfg, driver, store, logger, "run-2", pipeline.ID, backwards.RunnerOptions{
			WebhookData: webhookData,
		})
		err = runner2.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		results2, err := store.GetAll(context.Background(), "/pipeline/run-2/jobs/dedup-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results2).NotTo(BeEmpty())
		assert.Expect(results2[len(results2)-1].Payload["status"]).To(Equal("skipped"))
	})

	t.Run("different keys are not duplicates", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		logger := discardLogger()
		driver := newNativeDriver(t, "test-dedup-diff")

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-dedup", logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		pipeline, err := store.SavePipeline(context.Background(), "test-dedup-pipeline", "content", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		cfg := &configpkg.Config{
			Jobs: configpkg.Jobs{{
				Name: "dedup-job",
				Triggers: &configpkg.Triggers{
					Webhook: &configpkg.WebhookTriggerConfig{
						DedupKey: `headers["X-GitHub-Delivery"]`,
					},
				},
				Plan: configpkg.Steps{{
					Task: "echo-task",
					TaskConfig: &configpkg.TaskConfig{
						Platform:      "linux",
						ImageResource: configpkg.ImageResource{Type: "registry-image", Source: map[string]any{"repository": "busybox"}},
						Run:           &configpkg.TaskConfigRun{Path: "echo", Args: []string{"hello"}},
					},
				}},
			}},
		}

		// Run 1: first delivery
		runner1 := backwards.New(cfg, driver, store, logger, "run-1", pipeline.ID, backwards.RunnerOptions{
			WebhookData: &jsapi.WebhookData{
				Provider: "github", EventType: "push", Method: "POST",
				Headers: map[string]string{"X-GitHub-Delivery": "first"},
				Query:   map[string]string{}, Body: "{}",
			},
		})
		err = runner1.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		// Run 2: different delivery — not a duplicate
		runner2 := backwards.New(cfg, driver, store, logger, "run-2", pipeline.ID, backwards.RunnerOptions{
			WebhookData: &jsapi.WebhookData{
				Provider: "github", EventType: "push", Method: "POST",
				Headers: map[string]string{"X-GitHub-Delivery": "second"},
				Query:   map[string]string{}, Body: "{}",
			},
		})
		err = runner2.Run(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		results2, err := store.GetAll(context.Background(), "/pipeline/run-2/jobs/dedup-job", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results2).NotTo(BeEmpty())
		assert.Expect(results2[len(results2)-1].Payload["status"]).To(Equal("success"))
	})
}
