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

			cfg := loadConfig(t, "steps/try.yml")

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

			cfg := loadConfig(t, "steps/do.yml")

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

			cfg := loadConfig(t, "steps/on_failure.yml")

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

			cfg := loadConfig(t, "steps/on_error.yml")

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

			cfg := loadConfig(t, "steps/on_abort.yml")

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

			cfg := loadConfig(t, "steps/on_success.yml")

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

			cfg := loadConfig(t, "steps/in_parallel.yml")

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

			cfg := loadConfig(t, "steps/parallelism.yml")

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

			cfg := loadConfig(t, "steps/ensure.yml")

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

			cfg := loadConfig(t, "steps/skipped_steps.yml")

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

			cfg := loadConfig(t, "steps/caches.yml")

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

			cfg := loadConfig(t, "steps/attempts.yml")

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

			cfg := loadConfig(t, "steps/across.yml")

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

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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

			runner := backwards.New(cfg, driver, store, logger, "test-run")
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
	assert := NewGomegaWithT(t)

	matches, err := filepath.Glob("steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			for _, match := range matches {
				t.Run(filepath.Base(match), func(t *testing.T) {
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

	matches, err := filepath.Glob("steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			for _, match := range matches {
				t.Run(filepath.Base(match), func(t *testing.T) {
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

				runner := backwards.New(cfg, driver, store, logger, "test-run")
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
				runner1 := backwards.New(cfg, driver, store, logger, "run-1")
				err = runner1.Run(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				// Run 2: deploy's passed constraint satisfied by run-1's build success
				runner2 := backwards.New(cfg, driver, store, logger, "run-2")
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

				runner := backwards.New(cfg, driver, store, logger, "run-blocked")
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
