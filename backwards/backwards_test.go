package backwards_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/jtarchie/pocketci/backwards"
	_ "github.com/jtarchie/pocketci/orchestra/docker"
	_ "github.com/jtarchie/pocketci/orchestra/native"
	_ "github.com/jtarchie/pocketci/resources/mock"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

// youtubeIDStyle generates an 11-character ID from a hash of the input
// This matches the implementation in runner.go
func youtubeIDStyle(input string) string {
	hash := sha256.Sum256([]byte(input))
	encoded := base64.RawURLEncoding.EncodeToString(hash[:])

	const maxLength = 11
	if len(encoded) > maxLength {
		return encoded[:maxLength]
	}

	return encoded
}

func createLogger() (*strings.Builder, *slog.Logger) {
	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	return logs, logger
}

func TestBackwardsCompatibility(t *testing.T) {
	t.Parallel()

	t.Run("on_failure", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/on_failure.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("failing-task failed with code 1"))
	})

	t.Run("on_failure stores elapsed timing fields", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storageURL := fmt.Sprintf("sqlite://%s", dbFile.Name())

		const pipelineFile = "steps/on_failure.yml"
		const runID = "on-failure-elapsed"

		runner := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
			RunID:    runID,
		}
		err = runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		initStorage, found := storage.GetFromDSN(storageURL)
		assert.Expect(found).To(BeTrue())

		store, err := initStorage(storageURL, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/", []string{"status", "started_at", "elapsed"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty())

		var failureTaskPayload storage.Payload
		for _, result := range results {
			status, ok := result.Payload["status"].(string)
			if ok && status == "failure" && strings.Contains(result.Path, "/tasks/") {
				failureTaskPayload = result.Payload
				break
			}
		}

		assert.Expect(failureTaskPayload).NotTo(BeNil(), "expected a failed task payload in storage")

		startedAt, ok := failureTaskPayload["started_at"].(string)
		assert.Expect(ok).To(BeTrue())
		_, err = time.Parse(time.RFC3339, startedAt)
		assert.Expect(err).NotTo(HaveOccurred())

		elapsed, ok := failureTaskPayload["elapsed"].(string)
		assert.Expect(ok).To(BeTrue())
		assert.Expect(elapsed).To(ContainSubstring("s"))
	})

	t.Run("on_success", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/on_success.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("ensure", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/ensure.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("ensure-task failed with code 1"))
	})

	t.Run("do", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/do.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("ensure-task failed with code 11"))
	})

	t.Run("try", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/try.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("all", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/all.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring(`assert`))
		assert.Expect(strings.Count(logs.String(), `assert`)).To(Equal(22))
	})

	t.Run("caches", func(t *testing.T) {
		t.Parallel()

		_, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/caches.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("on_error", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/on_error.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("Task erroring-task errored"))
		assert.Expect(logs.String()).To(ContainSubstring(`assert`))
		assert.Expect(strings.Count(logs.String(), `assert`)).To(Equal(13))
	})

	t.Run("on_abort", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/on_abort.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("Task abort-task aborted"))
	})

	t.Run("across", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/across.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("parallelism", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/parallelism.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("in_parallel", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/in_parallel.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("task/file", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "steps/task_file.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("attempts", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)

		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storageURL := fmt.Sprintf("sqlite://%s", dbFile.Name())

		const pipelineFile = "steps/attempts.yml"
		const runID = "attempts-test"

		runner := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
			RunID:    runID,
		}
		err = runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify retry log messages appeared.
		assert.Expect(logs.String()).To(ContainSubstring("Attempt 1/3 failed, retrying..."))
		assert.Expect(logs.String()).To(ContainSubstring("Attempt 2/3 failed, retrying..."))

		// Verify each attempt gets its own storage path.
		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		initStorage, found := storage.GetFromDSN(storageURL)
		assert.Expect(found).To(BeTrue())

		store, err := initStorage(storageURL, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// The failing task (attempts=3) should have 3 separate storage entries,
		// one per attempt, each under its own /attempt/<n>/ suffix.
		results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/jobs/test-all-attempts-fail", nil)
		assert.Expect(err).NotTo(HaveOccurred())

		var attemptPaths []string
		for _, r := range results {
			if strings.Contains(r.Path, "/tasks/fail-all-attempts/attempt/") {
				attemptPaths = append(attemptPaths, r.Path)
			}
		}
		assert.Expect(attemptPaths).To(HaveLen(3), "expected 3 separate attempt entries")
	})

	t.Run("mutate job asserts", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		matches, err := filepath.Glob("steps/*.yml")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(matches).NotTo(BeEmpty())

		for _, match := range matches {
			// Capture the variable for the closure
			t.Run(filepath.Base(match), func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				contents, err := os.ReadFile(match)
				assert.Expect(err).NotTo(HaveOccurred())

				var config backwards.Config

				err = yaml.UnmarshalWithOptions(contents, &config, yaml.Strict())
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(config.Assert.Execution).NotTo(BeEmpty())

				config.Assert.Execution[0] = "unknown-job"

				file, err := os.CreateTemp(t.TempDir(), "*.yml")
				assert.Expect(err).NotTo(HaveOccurred())

				defer func() { _ = os.Remove(file.Name()) }()

				contents, err = yaml.MarshalWithOptions(config)
				assert.Expect(err).NotTo(HaveOccurred())
				_, err = file.Write(contents)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(file.Close()).NotTo(HaveOccurred())

				runner := testhelpers.Runner{
					Pipeline: file.Name(),
					Driver:   "native",
					Storage:  "sqlite://:memory:",
				}
				err = runner.Run(nil)

				assert.Expect(err).To(HaveOccurred())
				assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
			})
		}
	})

	t.Run("mutate step asserts", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		matches, err := filepath.Glob("steps/*.yml")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(matches).NotTo(BeEmpty())

		for _, match := range matches {
			t.Run(filepath.Base(match), func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				contents, err := os.ReadFile(match)
				assert.Expect(err).NotTo(HaveOccurred())

				var config backwards.Config

				err = yaml.UnmarshalWithOptions(contents, &config, yaml.Strict())
				assert.Expect(err).NotTo(HaveOccurred())

				// Collect all steps with assertions
				type stepLocation struct {
					jobIdx  int
					stepIdx int
					name    string
				}
				var stepsWithAssertions []stepLocation

				for i := range config.Jobs {
					for j := range config.Jobs[i].Plan {
						step := &config.Jobs[i].Plan[j]
						if step.Assert != nil {
							taskName := step.Task
							if taskName == "" {
								taskName = fmt.Sprintf("step-%d", j)
							}
							stepsWithAssertions = append(stepsWithAssertions, stepLocation{
								jobIdx:  i,
								stepIdx: j,
								name:    taskName,
							})
						}
					}
				}

				// Skip files without step-level assertions
				if len(stepsWithAssertions) == 0 {
					t.Skip("No step-level assertions found")
					return
				}

				// Test each step's assertion independently
				for _, loc := range stepsWithAssertions {
					t.Run(loc.name, func(t *testing.T) {
						assert := NewGomegaWithT(t)

						// Make a deep copy of config for this test
						var testConfig backwards.Config
						configBytes, err := yaml.MarshalWithOptions(config)
						assert.Expect(err).NotTo(HaveOccurred())
						err = yaml.UnmarshalWithOptions(configBytes, &testConfig, yaml.Strict())
						assert.Expect(err).NotTo(HaveOccurred())

						// Mutate only this specific step's assertion
						step := &testConfig.Jobs[loc.jobIdx].Plan[loc.stepIdx]
						if step.Assert.Code != nil {
							// Change expected exit code
							wrongCode := *step.Assert.Code + 1
							step.Assert.Code = &wrongCode
						} else if step.Assert.Stdout != "" {
							// Change expected stdout
							step.Assert.Stdout = "THIS-WILL-NOT-MATCH-" + step.Assert.Stdout
						} else if step.Assert.Stderr != "" {
							// Change expected stderr
							step.Assert.Stderr = "THIS-WILL-NOT-MATCH-" + step.Assert.Stderr
						}

						file, err := os.CreateTemp(t.TempDir(), "*.yml")
						assert.Expect(err).NotTo(HaveOccurred())

						defer func() { _ = os.Remove(file.Name()) }()

						contents, err := yaml.MarshalWithOptions(testConfig)
						assert.Expect(err).NotTo(HaveOccurred())
						_, err = file.Write(contents)
						assert.Expect(err).NotTo(HaveOccurred())
						assert.Expect(file.Close()).NotTo(HaveOccurred())

						runner := testhelpers.Runner{
							Pipeline: file.Name(),
							Driver:   "native",
							Storage:  "sqlite://:memory:",
						}
						err = runner.Run(nil)

						assert.Expect(err).To(HaveOccurred())
						assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
					})
				}
			})
		}
	})

	t.Run("resource version modes", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline: "versions/modes.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	// Regression test: ensure task storage paths are not duplicated at the top-level tasks/ prefix.
	// Previously, PipelineRunner.Run() always wrote to /pipeline/{runID}/tasks/{callIndex}-{name}
	// in addition to the TS layer's correctly-nested /pipeline/{runID}/jobs/{jobName}/... path.
	t.Run("hello-world task paths not duplicated at top-level tasks/ prefix", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// Use a persistent DB so we can re-open it after the run to inspect stored paths.
		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storageURL := fmt.Sprintf("sqlite://%s", dbFile.Name())

		const pipelineFile = "../examples/both/hello-world.yml"
		const runID = "hello-world-regression-test"

		runner := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
			RunID:    runID,
		}
		err = runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Re-open storage using the runtimeID derived from the pipeline's absolute path.
		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		initStorage, found := storage.GetFromDSN(storageURL)
		assert.Expect(found).To(BeTrue())

		store, err := initStorage(storageURL, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// Fetch all stored entries for this run.
		results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty(), "expected storage entries for hello-world run")

		// Assert NO path uses the old auto-generated top-level /tasks/ prefix.
		duplicateTasksPrefix := fmt.Sprintf("/pipeline/%s/tasks/", runID)
		for _, result := range results {
			assert.Expect(result.Path).NotTo(ContainSubstring(duplicateTasksPrefix),
				"found duplicate auto-generated tasks/ path: %s", result.Path)
		}

		// Assert task paths ARE correctly nested under /jobs/hello-world/.
		jobsPrefix := fmt.Sprintf("/pipeline/%s/jobs/hello-world/", runID)
		var jobPaths []string
		for _, result := range results {
			if strings.Contains(result.Path, jobsPrefix) {
				jobPaths = append(jobPaths, result.Path)
			}
		}
		assert.Expect(jobPaths).NotTo(BeEmpty(), "expected task paths nested under /jobs/hello-world/")
	})

	t.Run("remaining plan steps are marked skipped after mid-plan failure", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storageURL := fmt.Sprintf("sqlite://%s", dbFile.Name())

		const pipelineFile = "steps/skipped_steps.yml"
		const runID = "skipped-steps-test"

		runner := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
			RunID:    runID,
		}
		err = runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		initStorage, found := storage.GetFromDSN(storageURL)
		assert.Expect(found).To(BeTrue())

		store, err := initStorage(storageURL, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/", []string{"status", "errorMessage"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(results).NotTo(BeEmpty())

		// Verify the failing task has "failure" status
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

		// Verify the job-level error message is clean (no "h:" prefix)
		for _, result := range results {
			if strings.HasSuffix(result.Path, "/jobs/failing-job") {
				errMsg, ok := result.Payload["errorMessage"].(string)
				assert.Expect(ok).To(BeTrue(), "expected errorMessage on job entry")
				assert.Expect(errMsg).To(ContainSubstring("failing-task failed"))
				assert.Expect(errMsg).NotTo(HavePrefix("h:"), "error message should not have h: prefix")
			}
		}

		// Verify both remaining steps got "skipped" status
		skippedTasks := map[string]bool{}
		for _, result := range results {
			status, ok := result.Payload["status"].(string)
			if ok && status == "skipped" {
				skippedTasks[result.Path] = true
			}
		}
		assert.Expect(skippedTasks).To(HaveLen(2), "expected 2 skipped tasks, got paths: %v", skippedTasks)

		// Verify the skipped paths contain the expected task names
		var foundSkippedA, foundSkippedB bool
		for path := range skippedTasks {
			if strings.Contains(path, "tasks/skipped-task-a") {
				foundSkippedA = true
			}
			if strings.Contains(path, "tasks/skipped-task-b") {
				foundSkippedB = true
			}
		}
		assert.Expect(foundSkippedA).To(BeTrue(), "expected skipped-task-a in skipped entries")
		assert.Expect(foundSkippedB).To(BeTrue(), "expected skipped-task-b in skipped entries")
	})
}

func TestVersionEveryWithMock(t *testing.T) {
	t.Parallel()

	t.Run("version every with mock resource", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "test.db")
		storageURL := fmt.Sprintf("sqlite://%s", dbPath)

		pipelineFile := "versions/mock-every.yml"

		// Helper to query stored versions with a fresh connection
		queryVersions := func() []storage.Payload {
			pipelinePath, err := filepath.Abs(pipelineFile)
			assert.Expect(err).NotTo(HaveOccurred())
			runtimeID := youtubeIDStyle(pipelinePath)

			initStorage, found := storage.GetFromDSN(storageURL)
			assert.Expect(found).To(BeTrue())

			store, err := initStorage(storageURL, runtimeID, nil)
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = store.Close() }()

			scopedResourceName := fmt.Sprintf("%s/%s", runtimeID, "counter")
			metaKey := fmt.Sprintf("/rv/%s/meta", scopedResourceName)

			meta, err := store.Get(context.Background(), metaKey)
			if err != nil {
				return nil
			}

			count := int(meta["count"].(float64))
			versions := make([]storage.Payload, 0, count)
			for i := range count {
				key := fmt.Sprintf("/rv/%s/versions/%010d", scopedResourceName, i)
				v, err := store.Get(context.Background(), key)
				if err == nil {
					versions = append(versions, v)
				}
			}
			return versions
		}

		// Run 1: Should fetch the first version
		runner1 := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
		}
		err := runner1.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify a version was saved after run 1
		versions1 := queryVersions()
		assert.Expect(versions1).To(HaveLen(1))
		firstVersion := versions1[0]["version"].(map[string]interface{})

		// Run 2: Should fetch a NEW version (mock increments counter each Check)
		runner2 := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
		}
		err = runner2.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify we now have 2 distinct versions
		versions2 := queryVersions()
		assert.Expect(versions2).To(HaveLen(2))

		// Verify the versions are different (versions are ordered by ID ascending, so newest is last)
		secondVersion := versions2[len(versions2)-1]["version"].(map[string]interface{}) // Most recent is last
		assert.Expect(secondVersion).NotTo(Equal(firstVersion))

		// Run 3: Get another version
		runner3 := testhelpers.Runner{
			Pipeline: pipelineFile,
			Driver:   "native",
			Storage:  storageURL,
		}
		err = runner3.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify we now have 3 distinct versions
		versions3 := queryVersions()
		assert.Expect(versions3).To(HaveLen(3))
	})

	t.Run("validates undefined resource types", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// Test that pipeline with undefined resource type fails validation
		runner := testhelpers.Runner{
			Pipeline: "validation/undefined-resource-type.yml",
			Driver:   "native",
			Storage:  "sqlite://:memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("resource type"))
	})

	t.Run("validates resource type is defined", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// Test that pipeline with properly defined resource type passes validation
		// We're just testing that validation doesn't reject it
		pipeline, err := backwards.NewPipeline("validation/valid-with-resource-type.yml")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(pipeline).NotTo(BeEmpty())
	})

	t.Run("validates explicitly defined resource types are recognized", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// Test that pipeline with explicitly defined git resource type passes validation
		// This should not fail during validation
		pipeline, err := backwards.NewPipeline("validation/valid-with-default-resource-type.yml")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(pipeline).NotTo(BeEmpty())
	})

}
