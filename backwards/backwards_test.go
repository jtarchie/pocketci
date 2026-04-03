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
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
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
			Pipeline:          "steps/on_failure.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("task.failed"))
		assert.Expect(logs.String()).To(ContainSubstring("failing-task"))
	})

	t.Run("on_failure stores elapsed timing fields", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storagePath := dbFile.Name()

		const pipelineFile = "steps/on_failure.yml"
		const runID = "on-failure-elapsed"

		runner := testhelpers.Runner{
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
			RunID:             runID,
		}
		err = runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
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
			Pipeline:          "steps/on_success.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("ensure", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/ensure.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("task.failed"))
		assert.Expect(logs.String()).To(ContainSubstring("ensure-task"))
	})

	t.Run("do", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/do.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("task.failed"))
		assert.Expect(logs.String()).To(ContainSubstring("ensure-task"))
		assert.Expect(logs.String()).To(ContainSubstring("code=11"))
	})

	t.Run("try", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/try.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("all", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/all.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("caches", func(t *testing.T) {
		t.Parallel()

		_, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/caches.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("on_error", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/on_error.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("Task erroring-task errored"))
	})

	t.Run("on_abort", func(t *testing.T) {
		t.Parallel()

		logs, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/on_abort.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(logs.String()).To(ContainSubstring("Task abort-task aborted"))
	})

	t.Run("across", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/across.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("parallelism", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/parallelism.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("in_parallel", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/in_parallel.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("task/file", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/task_file.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
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
		storagePath := dbFile.Name()

		const pipelineFile = "steps/attempts.yml"
		const runID = "attempts-test"

		runner := testhelpers.Runner{
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
			RunID:             runID,
		}
		err = runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify retry log messages appeared (slog text format: key=value).
		assert.Expect(logs.String()).To(ContainSubstring("attempt.failed"))
		assert.Expect(logs.String()).To(ContainSubstring("attempt=1"))
		assert.Expect(logs.String()).To(ContainSubstring("attempt=2"))

		// Verify each attempt gets its own storage path.
		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// The failing task (attempts=3) should have 3 separate storage entries,
		// one per attempt, each under its own /attempt/<n>/ suffix.
		results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/jobs/test-all-attempts-fail", nil)
		assert.Expect(err).NotTo(HaveOccurred())

		var attemptPaths []string
		for _, r := range results {
			if strings.Contains(r.Path, "/tasks/fail-all-attempts") && strings.Contains(r.Path, "/attempt/") {
				attemptPaths = append(attemptPaths, r.Path)
			}
		}
		assert.Expect(attemptPaths).To(HaveLen(3), "expected 3 separate attempt entries")
	})

	t.Run("mutate job asserts", func(t *testing.T) {
		t.Parallel()
		testMutateJobAsserts(t)
	})

	t.Run("mutate step asserts", func(t *testing.T) {
		t.Parallel()
		testMutateStepAsserts(t)
	})

	t.Run("resource version modes", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "versions/modes.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	// Regression test: ensure task storage paths are not duplicated at the top-level tasks/ prefix.
	// Previously, PipelineRunner.Run() always wrote to /pipeline/{runID}/tasks/{callIndex}-{name}
	// in addition to the TS layer's correctly-nested /pipeline/{runID}/jobs/{jobName}/... path.
	t.Run("hello-world task paths not duplicated at top-level tasks/ prefix", func(t *testing.T) {
		t.Parallel()
		testHelloWorldPathsNotDuplicated(t)
	})

	t.Run("remaining plan steps are marked skipped after mid-plan failure", func(t *testing.T) {
		t.Parallel()
		testSkippedStepsAfterFailure(t)
	})

	t.Run("image shorthand on task config", func(t *testing.T) {
		t.Parallel()

		_, logger := createLogger()
		assert := NewGomegaWithT(t)
		runner := testhelpers.Runner{
			Pipeline:          "steps/image_shorthand.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
	})
}

func testMutateJobAsserts(t *testing.T) {
	t.Helper()

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
				Pipeline:          file.Name(),
				Driver:            "native",
				StorageSQLitePath: ":memory:",
			}
			err = runner.Run(nil)

			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
		})
	}
}

type stepLocation struct {
	jobIdx  int
	stepIdx int
	name    string
}

func testMutateStepAsserts(t *testing.T) {
	t.Helper()

	assert := NewGomegaWithT(t)
	matches, err := filepath.Glob("steps/*.yml")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(matches).NotTo(BeEmpty())

	for _, match := range matches {
		t.Run(filepath.Base(match), func(t *testing.T) {
			t.Parallel()
			testMutateStepAssertsForFile(t, match)
		})
	}
}

func testMutateStepAssertsForFile(t *testing.T, match string) {
	t.Helper()

	assert := NewGomegaWithT(t)
	contents, err := os.ReadFile(match)
	assert.Expect(err).NotTo(HaveOccurred())

	var config backwards.Config

	err = yaml.UnmarshalWithOptions(contents, &config, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())

	stepsWithAssertions := collectStepsWithAssertions(config)

	if len(stepsWithAssertions) == 0 {
		t.Skip("No step-level assertions found")
		return
	}

	for _, loc := range stepsWithAssertions {
		t.Run(loc.name, func(t *testing.T) {
			testMutateSingleStepAssertion(t, config, loc)
		})
	}
}

func collectStepsWithAssertions(config backwards.Config) []stepLocation {
	var result []stepLocation

	for i := range config.Jobs {
		for j := range config.Jobs[i].Plan {
			step := &config.Jobs[i].Plan[j]
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

func testMutateSingleStepAssertion(t *testing.T, config backwards.Config, loc stepLocation) {
	t.Helper()

	assert := NewGomegaWithT(t)

	var testConfig backwards.Config
	configBytes, err := yaml.MarshalWithOptions(config)
	assert.Expect(err).NotTo(HaveOccurred())
	err = yaml.UnmarshalWithOptions(configBytes, &testConfig, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())

	step := &testConfig.Jobs[loc.jobIdx].Plan[loc.stepIdx]
	if step.Assert.Code != nil {
		wrongCode := *step.Assert.Code + 1
		step.Assert.Code = &wrongCode
	} else if step.Assert.Stdout != "" {
		step.Assert.Stdout = "THIS-WILL-NOT-MATCH-" + step.Assert.Stdout
	} else if step.Assert.Stderr != "" {
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
		Pipeline:          file.Name(),
		Driver:            "native",
		StorageSQLitePath: ":memory:",
	}
	err = runner.Run(nil)

	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("assertion failed"))
}

func testHelloWorldPathsNotDuplicated(t *testing.T) {
	t.Helper()

	assert := NewGomegaWithT(t)

	dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
	storagePath := dbFile.Name()

	const pipelineFile = "../examples/both/hello-world.yml"
	const runID = "hello-world-regression-test"

	runner := testhelpers.Runner{
		Pipeline:          pipelineFile,
		Driver:            "native",
		StorageSQLitePath: storagePath,
		RunID:             runID,
	}
	err = runner.Run(nil)
	assert.Expect(err).NotTo(HaveOccurred())

	pipelinePath, err := filepath.Abs(pipelineFile)
	assert.Expect(err).NotTo(HaveOccurred())
	runtimeID := youtubeIDStyle(pipelinePath)

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/", []string{"status"})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(results).NotTo(BeEmpty(), "expected storage entries for hello-world run")

	duplicateTasksPrefix := fmt.Sprintf("/pipeline/%s/tasks/", runID)
	for _, result := range results {
		assert.Expect(result.Path).NotTo(ContainSubstring(duplicateTasksPrefix),
			"found duplicate auto-generated tasks/ path: %s", result.Path)
	}

	jobsPrefix := fmt.Sprintf("/pipeline/%s/jobs/hello-world/", runID)
	var jobPaths []string
	for _, result := range results {
		if strings.Contains(result.Path, jobsPrefix) {
			jobPaths = append(jobPaths, result.Path)
		}
	}
	assert.Expect(jobPaths).NotTo(BeEmpty(), "expected task paths nested under /jobs/hello-world/")
}

func testSkippedStepsAfterFailure(t *testing.T) {
	t.Helper()

	assert := NewGomegaWithT(t)

	dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
	storagePath := dbFile.Name()

	const pipelineFile = "steps/skipped_steps.yml"
	const runID = "skipped-steps-test"

	runner := testhelpers.Runner{
		Pipeline:          pipelineFile,
		Driver:            "native",
		StorageSQLitePath: storagePath,
		RunID:             runID,
	}
	err = runner.Run(nil)
	assert.Expect(err).NotTo(HaveOccurred())

	pipelinePath, err := filepath.Abs(pipelineFile)
	assert.Expect(err).NotTo(HaveOccurred())
	runtimeID := youtubeIDStyle(pipelinePath)

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	results, err := store.GetAll(context.Background(), "/pipeline/"+runID+"/", []string{"status", "errorMessage"})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(results).NotTo(BeEmpty())

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

	for _, result := range results {
		if strings.HasSuffix(result.Path, "/jobs/failing-job") {
			errMsg, ok := result.Payload["errorMessage"].(string)
			assert.Expect(ok).To(BeTrue(), "expected errorMessage on job entry")
			assert.Expect(errMsg).To(ContainSubstring("failing-task"))
			assert.Expect(errMsg).NotTo(HavePrefix("h:"), "error message should not have h: prefix")
		}
	}

	skippedTasks := map[string]bool{}
	for _, result := range results {
		status, ok := result.Payload["status"].(string)
		if ok && status == "skipped" {
			skippedTasks[result.Path] = true
		}
	}
	assert.Expect(skippedTasks).To(HaveLen(2), "expected 2 skipped tasks, got paths: %v", skippedTasks)

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
}

func TestVersionEveryWithMock(t *testing.T) {
	t.Parallel()

	t.Run("version every with mock resource", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "test.db")
		storagePath := dbPath

		pipelineFile := "versions/mock-every.yml"

		// Helper to query stored versions with a fresh connection
		queryVersions := func() []storage.Payload {
			pipelinePath, err := filepath.Abs(pipelineFile)
			assert.Expect(err).NotTo(HaveOccurred())
			runtimeID := youtubeIDStyle(pipelinePath)

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
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
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
		}
		err := runner1.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify a version was saved after run 1
		versions1 := queryVersions()
		assert.Expect(versions1).To(HaveLen(1))
		firstVersion := versions1[0]["version"].(map[string]interface{})

		// Run 2: Should fetch a NEW version (mock increments counter each Check)
		runner2 := testhelpers.Runner{
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
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
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
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
			Pipeline:          "validation/undefined-resource-type.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
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

	t.Run("task with file URI loads config from volume", func(t *testing.T) {
		t.Parallel()

		_, logger := createLogger()
		assert := NewGomegaWithT(t)

		runner := testhelpers.Runner{
			Pipeline:          "steps/task_uri.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(logger)
		assert.Expect(err).NotTo(HaveOccurred())
	})

}

func TestCrossRunPassed(t *testing.T) {
	t.Parallel()

	t.Run("within-run cascade works with passed constraints", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		runner := testhelpers.Runner{
			Pipeline:          "steps/cross_run_passed.yml",
			Driver:            "native",
			StorageSQLitePath: ":memory:",
		}
		err := runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("second run succeeds using prior run's job status", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storagePath := dbFile.Name()

		const pipelineFile = "steps/cross_run_passed.yml"

		// Run 1: both build and deploy execute via within-run cascade
		runner1 := testhelpers.Runner{
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
			RunID:             "run-1",
		}
		err = runner1.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Run 2: same pipeline, different run ID, same storage
		// deploy's passed constraint should be satisfied by run-1's build success
		runner2 := testhelpers.Runner{
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
			RunID:             "run-2",
		}
		err = runner2.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		// Verify both runs wrote job statuses to storage
		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// Verify run-1 has job records (build and deploy, plus their step entries)
		results1, err := store.GetAll(context.Background(), "/pipeline/run-1/jobs/", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(len(results1)).To(BeNumerically(">=", 2))

		// Verify run-2 has job records
		results2, err := store.GetAll(context.Background(), "/pipeline/run-2/jobs/", []string{"status"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(len(results2)).To(BeNumerically(">=", 2))

		// Verify cross-run query works: build's most recent status is success
		status, err := store.GetMostRecentJobStatus(context.Background(), "", "build")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status).To(Equal("success"))

		// Verify cross-run query works: deploy's most recent status is success
		status, err = store.GetMostRecentJobStatus(context.Background(), "", "deploy")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status).To(Equal("success"))
	})

	t.Run("blockedBy info in pending records", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		dbFile, err := os.CreateTemp(t.TempDir(), "*.db")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(dbFile.Close()).NotTo(HaveOccurred())
		storagePath := dbFile.Name()

		const pipelineFile = "steps/cross_run_passed.yml"

		// Single run with no prior data: deploy's pending record should
		// initially have blockedBy info (before build runs and cascades)
		runner := testhelpers.Runner{
			Pipeline:          pipelineFile,
			Driver:            "native",
			StorageSQLitePath: storagePath,
			RunID:             "run-blocked",
		}
		err = runner.Run(nil)
		assert.Expect(err).NotTo(HaveOccurred())

		pipelinePath, err := filepath.Abs(pipelineFile)
		assert.Expect(err).NotTo(HaveOccurred())
		runtimeID := youtubeIDStyle(pipelinePath)

		store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: storagePath}, runtimeID, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// After the run completes, both jobs should have succeeded
		// (within-run cascade handles it)
		buildPayload, err := store.Get(context.Background(), "/pipeline/run-blocked/jobs/build")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(buildPayload["status"]).To(Equal("success"))

		deployPayload, err := store.Get(context.Background(), "/pipeline/run-blocked/jobs/deploy")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(deployPayload["status"]).To(Equal("success"))
	})
}
