package runtime_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/runtime/runner"
	storage "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestStreamLogsWithCallback(t *testing.T) {
	t.Parallel()

	t.Run("streams logs via callback while container runs", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Create storage
		store, err := storage.NewSqlite("sqlite://:memory:", "stream-test", nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		ctx := context.Background()
		logger := slog.Default()

		// Create docker driver
		driver, err := docker.New(docker.Config{Namespace: "stream-test-ns"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close() }()

		runID := "stream-test-run"

		// Create pipeline runner
		r := runner.NewPipelineRunner(ctx, driver, store, logger, "stream-test-ns", runID)
		defer func() { _ = r.CleanupVolumes() }()

		// Track callback invocations
		var mu sync.Mutex
		var stdoutChunks []string
		var callbackCount int

		// Run a task that produces output with small delays
		result, err := r.Run(runner.RunInput{
			Name:  "streaming-task",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "sh",
				Args: []string{"-c", "echo line1; sleep 0.1; echo line2; sleep 0.1; echo line3"},
			},
			OnOutput: func(stream string, data string) {
				mu.Lock()
				defer mu.Unlock()
				callbackCount++
				if stream == "stdout" {
					stdoutChunks = append(stdoutChunks, data)
				}
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Status).To(Equal(runner.RunComplete))
		assert.Expect(result.Code).To(Equal(0))

		// Verify callback was invoked
		mu.Lock()
		assert.Expect(callbackCount).To(BeNumerically(">", 0))
		fullStdout := ""
		for _, chunk := range stdoutChunks {
			fullStdout += chunk
		}
		mu.Unlock()

		// Verify all lines were captured
		assert.Expect(fullStdout).To(ContainSubstring("line1"))
		assert.Expect(fullStdout).To(ContainSubstring("line2"))
		assert.Expect(fullStdout).To(ContainSubstring("line3"))

		// Verify final result also has all output
		assert.Expect(result.Stdout).To(ContainSubstring("line1"))
		assert.Expect(result.Stdout).To(ContainSubstring("line2"))
		assert.Expect(result.Stdout).To(ContainSubstring("line3"))
	})

	t.Run("callback is optional", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Create storage
		store, err := storage.NewSqlite("sqlite://:memory:", "stream-test", nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		ctx := context.Background()
		logger := slog.Default()

		// Create docker driver
		driver, err := docker.New(docker.Config{Namespace: "stream-test-ns-optional"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close() }()

		runID := "stream-test-optional"

		// Create pipeline runner
		r := runner.NewPipelineRunner(ctx, driver, store, logger, "stream-test-ns-optional", runID)
		defer func() { _ = r.CleanupVolumes() }()

		// Run a task WITHOUT callback - should still work
		result, err := r.Run(runner.RunInput{
			Name:  "no-streaming-task",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "echo",
				Args: []string{"hello without streaming"},
			},
			// No OnOutput callback provided
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Status).To(Equal(runner.RunComplete))
		assert.Expect(result.Stdout).To(ContainSubstring("hello without streaming"))
	})

	t.Run("handles container errors during streaming", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Create storage
		store, err := storage.NewSqlite("sqlite://:memory:", "stream-error-test", nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		ctx := context.Background()
		logger := slog.Default()

		// Create docker driver
		driver, err := docker.New(docker.Config{Namespace: "stream-error-ns"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close() }()

		runID := "stream-error-run"

		// Create pipeline runner
		r := runner.NewPipelineRunner(ctx, driver, store, logger, "stream-error-ns", runID)
		defer func() { _ = r.CleanupVolumes() }()

		// Track callback invocations
		var mu sync.Mutex
		var capturedStdout string

		// Run a task that outputs then fails
		result, err := r.Run(runner.RunInput{
			Name:  "error-task",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "sh",
				Args: []string{"-c", "echo before-error; exit 1"},
			},
			OnOutput: func(stream string, data string) {
				mu.Lock()
				defer mu.Unlock()
				if stream == "stdout" {
					capturedStdout += data
				}
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Status).To(Equal(runner.RunComplete))
		assert.Expect(result.Code).To(Equal(1))
		assert.Expect(result.Stdout).To(ContainSubstring("before-error"))

		// Verify callback captured output even with error
		mu.Lock()
		assert.Expect(capturedStdout).To(ContainSubstring("before-error"))
		mu.Unlock()
	})

	t.Run("context cancellation stops streaming", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Create storage
		store, err := storage.NewSqlite("sqlite://:memory:", "stream-cancel-test", nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		logger := slog.Default()

		// Create docker driver with unique namespace
		uniqueNS := "stream-cancel-ns-" + time.Now().Format("150405")
		driver, err := docker.New(docker.Config{Namespace: uniqueNS}, logger)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close() }()

		// Create a context that will be cancelled
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		runID := "stream-cancel-run-" + time.Now().Format("150405")

		// Create pipeline runner
		r := runner.NewPipelineRunner(ctx, driver, store, logger, uniqueNS, runID)
		defer func() { _ = r.CleanupVolumes() }()

		// Run a task that takes longer than the timeout
		result, err := r.Run(runner.RunInput{
			Name:  "cancel-task",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "sh",
				Args: []string{"-c", "echo before-cancel; sleep 10; echo after-cancel"},
			},
			OnOutput: func(stream string, data string) {
				// Callback may or may not be called depending on timing
			},
		})

		// The task should be aborted due to context cancellation
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Status).To(Equal(runner.RunAbort))
	})
}
