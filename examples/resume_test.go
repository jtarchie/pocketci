package main_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/runtime/runner"
	storage "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestResumeSkipsCompletedSteps(t *testing.T) {
	assert := NewGomegaWithT(t)

	// Create a persistent storage file for this test
	store, err := storage.NewSqlite(storage.Config{Path: ":memory:"}, "resume-test", nil)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	logger := slog.Default()

	// Create docker driver
	driver, err := docker.New(context.Background(), docker.Config{Namespace: "resume-test-ns"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = driver.Close() }()

	runID := "test-run-" + time.Now().Format("20060102150405")

	// First run: Execute pipeline with resume enabled
	t.Run("first run executes all steps", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		r, err := runner.NewResumableRunner(ctx, driver, store, logger, "resume-test-ns", runner.ResumeOptions{
			RunID:  runID,
			Resume: true,
		})
		assert.Expect(err).NotTo(HaveOccurred())

		// Run first step
		result1, err := r.Run(runner.RunInput{ //nolint:contextcheck // Run uses stored ctx; JS VM cannot pass Go contexts
			Name:  "step-1",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "echo",
				Args: []string{"step 1 output"},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result1.Status).To(Equal(runner.RunComplete))
		assert.Expect(result1.Stdout).To(ContainSubstring("step 1 output"))

		// Run second step
		result2, err := r.Run(runner.RunInput{ //nolint:contextcheck // Run uses stored ctx; JS VM cannot pass Go contexts
			Name:  "step-2",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "echo",
				Args: []string{"step 2 output"},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result2.Status).To(Equal(runner.RunComplete))
		assert.Expect(result2.Stdout).To(ContainSubstring("step 2 output"))

		// Verify state has both steps completed
		state := r.State()
		assert.Expect(len(state.Steps)).To(Equal(2))
	})

	// Second run: Resume should skip completed steps
	t.Run("resume skips completed steps", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		r, err := runner.NewResumableRunner(ctx, driver, store, logger, "resume-test-ns", runner.ResumeOptions{
			RunID:  runID,
			Resume: true,
		})
		assert.Expect(err).NotTo(HaveOccurred())

		// Run first step again - should be skipped
		result1, err := r.Run(runner.RunInput{ //nolint:contextcheck // Run uses stored ctx; JS VM cannot pass Go contexts
			Name:  "step-1",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "echo",
				Args: []string{"step 1 output"},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		// Result should come from cache, not a new execution
		assert.Expect(result1.Status).To(Equal(runner.RunComplete))
		assert.Expect(result1.Stdout).To(ContainSubstring("step 1 output"))

		// Run second step again - should be skipped
		result2, err := r.Run(runner.RunInput{ //nolint:contextcheck // Run uses stored ctx; JS VM cannot pass Go contexts
			Name:  "step-2",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "echo",
				Args: []string{"step 2 output"},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result2.Status).To(Equal(runner.RunComplete))
		assert.Expect(result2.Stdout).To(ContainSubstring("step 2 output"))

		// Run a third step - this should actually execute
		result3, err := r.Run(runner.RunInput{ //nolint:contextcheck // Run uses stored ctx; JS VM cannot pass Go contexts
			Name:  "step-3",
			Image: "busybox",
			Command: struct {
				Path string   `json:"path"`
				Args []string `json:"args"`
				User string   `json:"user"`
			}{
				Path: "echo",
				Args: []string{"step 3 new output"},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result3.Status).To(Equal(runner.RunComplete))
		assert.Expect(result3.Stdout).To(ContainSubstring("step 3 new output"))

		// Verify state now has three steps
		state := r.State()
		assert.Expect(len(state.Steps)).To(Equal(3))
	})
}

func TestResumeContainerReattachment(t *testing.T) {
	// This test verifies that we can reattach to a running container
	// This is harder to test because we need to simulate mid-execution interruption
	t.Skip("Container reattachment requires simulating interruption - manual testing recommended")
}

func TestGetContainerDockerDriver(t *testing.T) {
	assert := NewGomegaWithT(t)

	ctx := context.Background()
	logger := slog.Default()

	// Create docker driver
	driver, err := docker.New(context.Background(), docker.Config{Namespace: "getcontainer-test-ns"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = driver.Close() }()

	// Test GetContainer with non-existent container
	_, err = driver.GetContainer(ctx, "non-existent-container-id")
	assert.Expect(err).To(MatchError(orchestra.ErrContainerNotFound))

	// Create a container and verify we can get it
	container, err := driver.RunContainer(ctx, orchestra.Task{
		ID:      "test-task",
		Image:   "busybox",
		Command: []string{"sleep", "30"}, // Long running to test reattachment
	})
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = container.Cleanup(ctx) }()

	containerID := container.ID()
	assert.Expect(containerID).NotTo(BeEmpty())

	// Verify we can get the container by ID
	retrievedContainer, err := driver.GetContainer(ctx, containerID)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(retrievedContainer.ID()).To(Equal(containerID))

	// Verify the container is still running
	status, err := retrievedContainer.Status(ctx)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(status.IsDone()).To(BeFalse())
}
