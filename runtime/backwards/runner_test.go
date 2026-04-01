package backwards_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	configpkg "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/native"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

			cfg := loadConfig(t, "../../backwards/steps/try.yml")

			logger := discardLogger()

			driver, err := df.new("test-try-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-try", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestDoStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/do.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-do-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-do", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			cfg := loadConfig(t, "../../backwards/steps/on_failure.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-failure-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-failure", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			cfg := loadConfig(t, "../../backwards/steps/on_error.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-error-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-error", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			cfg := loadConfig(t, "../../backwards/steps/on_abort.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-abort-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-abort", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			cfg := loadConfig(t, "../../backwards/steps/on_success.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-success-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-success", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			cfg := loadConfig(t, "../../backwards/steps/in_parallel.yml")

			logger := discardLogger()

			driver, err := df.new("test-in-parallel-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-in-parallel", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestParallelismStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/parallelism.yml")

			logger := discardLogger()

			driver, err := df.new("test-parallelism-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-parallelism", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestEnsureStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/ensure.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-ensure-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-ensure", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			cfg := loadConfig(t, "../../backwards/steps/skipped_steps.yml")

			logger := discardLogger()

			driver, err := df.new("test-skipped-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-skipped", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestCachesStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/caches.yml")

			logger := discardLogger()

			driver, err := df.new("test-caches-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-caches", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestAttemptsStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/attempts.yml")

			logger := discardLogger()

			driver, err := df.new("test-attempts-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-attempts", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestAcrossStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/across.yml")

			logger := discardLogger()

			driver, err := df.new("test-across-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-across", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
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

// skipJobMutate lists YAML files that fail before reaching assertion validation
// in the Go-native runner, so mutating their job asserts doesn't produce
// "assertion failed" errors.
var skipJobMutate = map[string]bool{
	"cross_run_passed.yml": true, // uses get steps with resources (unsupported)
}

// skipStepMutate lists YAML files whose step-level assertions can't be
// mutation-tested through the Go-native runner.
var skipStepMutate = map[string]bool{
	"cross_run_passed.yml": true, // uses get steps with resources (unsupported)
	"task_file.yml":        true, // uses outputs + file: task reference (unsupported)
	"task_uri.yml":         true, // uses file:// URI task reference (unsupported)
}

func TestMutateJobAsserts(t *testing.T) {
	assert := NewGomegaWithT(t)

	matches, err := filepath.Glob("../../backwards/steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			for _, match := range matches {
				t.Run(filepath.Base(match), func(t *testing.T) {
					if skipJobMutate[filepath.Base(match)] {
						t.Skip("not supported by Go-native runner")
					}

					assert := NewGomegaWithT(t)

					cfg := loadConfig(t, match)
					assert.Expect(cfg.Assert.Execution).NotTo(BeEmpty())

					cfg.Assert.Execution[0] = "unknown-job"

					logger := discardLogger()

					driver, err := df.new("test-mutate-job-"+df.name, logger)
					assert.Expect(err).NotTo(HaveOccurred())

					defer func() { _ = driver.Close() }()

					store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-mutate-job", logger)
					assert.Expect(err).NotTo(HaveOccurred())

					defer func() { _ = store.Close() }()

					runner := backwards.New(cfg, driver, store, logger, "test-run")
					err = runner.Run(context.Background())
					assert.Expect(err).To(HaveOccurred())
					assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
				})
			}
		})
	}
}

func TestMutateStepAsserts(t *testing.T) {
	assert := NewGomegaWithT(t)

	matches, err := filepath.Glob("../../backwards/steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			for _, match := range matches {
				t.Run(filepath.Base(match), func(t *testing.T) {
					if skipStepMutate[filepath.Base(match)] {
						t.Skip("not supported by Go-native runner")
					}

					cfg := loadConfig(t, match)
					locations := collectStepsWithAssertions(cfg)

					if len(locations) == 0 {
						t.Skip("No step-level assertions found")
						return
					}

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

							driver, err := df.new("test-mutate-step-"+df.name, logger)
							assert.Expect(err).NotTo(HaveOccurred())

							defer func() { _ = driver.Close() }()

							store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-mutate-step", logger)
							assert.Expect(err).NotTo(HaveOccurred())

							defer func() { _ = store.Close() }()

							runner := backwards.New(mutated, driver, store, logger, "test-run")
							err = runner.Run(context.Background())
							assert.Expect(err).To(HaveOccurred())
							assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
						})
					}
				})
			}
		})
	}
}
