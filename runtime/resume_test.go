package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/runtime/runner"
	storage "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestStepState(t *testing.T) {
	t.Parallel()

	t.Run("IsTerminal", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		cases := []struct {
			status   runner.StepStatus
			terminal bool
		}{
			{runner.StepStatusPending, false},
			{runner.StepStatusRunning, false},
			{runner.StepStatusCompleted, true},
			{runner.StepStatusFailed, true},
			{runner.StepStatusAborted, true},
		}

		for _, tc := range cases {
			step := &runner.StepState{Status: tc.status}
			assert.Expect(step.IsTerminal()).To(Equal(tc.terminal), "status %s", tc.status)
		}
	})

	t.Run("IsResumable", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Running step with container ID is resumable
		step := &runner.StepState{
			Status:      runner.StepStatusRunning,
			ContainerID: "container-123",
		}
		assert.Expect(step.IsResumable()).To(BeTrue())

		// Running step without container ID is not resumable
		step = &runner.StepState{
			Status: runner.StepStatusRunning,
		}
		assert.Expect(step.IsResumable()).To(BeFalse())

		// Completed step is not resumable
		step = &runner.StepState{
			Status:      runner.StepStatusCompleted,
			ContainerID: "container-123",
		}
		assert.Expect(step.IsResumable()).To(BeFalse())
	})

	t.Run("CanSkip", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Completed step with result can be skipped
		step := &runner.StepState{
			Status: runner.StepStatusCompleted,
			Result: &runner.RunResult{
				Status: runner.RunComplete,
				Code:   0,
			},
		}
		assert.Expect(step.CanSkip()).To(BeTrue())

		// Completed step without result cannot be skipped
		step = &runner.StepState{
			Status: runner.StepStatusCompleted,
		}
		assert.Expect(step.CanSkip()).To(BeFalse())

		// Running step cannot be skipped
		step = &runner.StepState{
			Status: runner.StepStatusRunning,
			Result: &runner.RunResult{},
		}
		assert.Expect(step.CanSkip()).To(BeFalse())
	})
}

func TestPipelineState(t *testing.T) {
	t.Parallel()

	t.Run("NewPipelineState", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		state := runner.NewPipelineState("run-123", true)
		assert.Expect(state.RunID).To(Equal("run-123"))
		assert.Expect(state.ResumeEnabled).To(BeTrue())
		assert.Expect(state.Steps).To(BeEmpty())
		assert.Expect(state.StepOrder).To(BeEmpty())
		assert.Expect(state.StartedAt).NotTo(BeNil())
	})

	t.Run("SetStep and GetStep", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		state := runner.NewPipelineState("run-123", true)

		step1 := &runner.StepState{
			StepID: "step-1",
			Name:   "First Step",
			Status: runner.StepStatusRunning,
		}
		state.SetStep(step1)

		step2 := &runner.StepState{
			StepID: "step-2",
			Name:   "Second Step",
			Status: runner.StepStatusPending,
		}
		state.SetStep(step2)

		// Verify steps are stored correctly
		assert.Expect(state.GetStep("step-1")).To(Equal(step1))
		assert.Expect(state.GetStep("step-2")).To(Equal(step2))
		assert.Expect(state.GetStep("step-3")).To(BeNil())

		// Verify order is maintained
		assert.Expect(state.StepOrder).To(Equal([]string{"step-1", "step-2"}))
	})

	t.Run("LastStep", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		state := runner.NewPipelineState("run-123", true)
		assert.Expect(state.LastStep()).To(BeNil())

		step1 := &runner.StepState{StepID: "step-1", Name: "First"}
		state.SetStep(step1)
		assert.Expect(state.LastStep()).To(Equal(step1))

		step2 := &runner.StepState{StepID: "step-2", Name: "Second"}
		state.SetStep(step2)
		assert.Expect(state.LastStep()).To(Equal(step2))
	})

	t.Run("InProgressSteps", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		state := runner.NewPipelineState("run-123", true)

		state.SetStep(&runner.StepState{StepID: "step-1", Status: runner.StepStatusCompleted})
		state.SetStep(&runner.StepState{StepID: "step-2", Status: runner.StepStatusRunning})
		state.SetStep(&runner.StepState{StepID: "step-3", Status: runner.StepStatusPending})
		state.SetStep(&runner.StepState{StepID: "step-4", Status: runner.StepStatusRunning})

		inProgress := state.InProgressSteps()
		assert.Expect(inProgress).To(HaveLen(2))
		assert.Expect(inProgress[0].StepID).To(Equal("step-2"))
		assert.Expect(inProgress[1].StepID).To(Equal("step-4"))
	})
}

func TestResumableRunnerStatePersistence(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	// Create an in-memory storage
	store, err := storage.NewSqlite(storage.Config{Path: ":memory:"}, "test-ns", nil)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	t.Run("state is persisted after step execution", func(t *testing.T) {
		// We can't easily test the full execution without a driver,
		// but we can test the state management logic
		now := time.Now()
		state := runner.NewPipelineState("test-run", true)

		step := &runner.StepState{
			StepID:    "step-1",
			Name:      "test-step",
			Status:    runner.StepStatusRunning,
			StartedAt: &now,
		}
		state.SetStep(step)

		// Verify state contains the step
		assert.Expect(state.GetStep("step-1")).NotTo(BeNil())
		assert.Expect(state.GetStep("step-1").Status).To(Equal(runner.StepStatusRunning))

		// Update step to completed
		step.Status = runner.StepStatusCompleted
		step.Result = &runner.RunResult{
			Status: runner.RunComplete,
			Code:   0,
			Stdout: "Hello, World!",
		}

		// Verify step can be skipped now
		assert.Expect(step.CanSkip()).To(BeTrue())
	})
}

func TestResumableRunnerCreation(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	// Create an in-memory storage
	store, err := storage.NewSqlite(storage.Config{Path: ":memory:"}, "test-ns", nil)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Placeholder test - would need mock driver for full test
	_ = ctx
	_ = store
	_ = assert
}
