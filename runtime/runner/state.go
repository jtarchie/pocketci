package runner

import (
	"encoding/json"
	"time"
)

// StepStatus represents the execution status of a pipeline step.
type StepStatus string

const (
	// StepStatusPending indicates the step has not started yet.
	StepStatusPending StepStatus = "pending"
	// StepStatusRunning indicates the step is currently executing.
	StepStatusRunning StepStatus = "running"
	// StepStatusCompleted indicates the step finished successfully.
	StepStatusCompleted StepStatus = "completed"
	// StepStatusFailed indicates the step finished with an error.
	StepStatusFailed StepStatus = "failed"
	// StepStatusAborted indicates the step was interrupted/aborted.
	StepStatusAborted StepStatus = "aborted"
)

// StepKind distinguishes between different types of steps.
type StepKind string

const (
	StepKindRun   StepKind = "run"
	StepKindAgent StepKind = "agent"
)

// StepState represents the persisted state of a pipeline step.
type StepState struct {
	// StepID is a unique identifier for this step within the pipeline run.
	StepID string `json:"step_id"`
	// Name is the human-readable name of the step.
	Name string `json:"name"`
	// Kind distinguishes run steps from agent steps.
	Kind StepKind `json:"kind"`
	// Status is the current execution status.
	Status StepStatus `json:"status"`
	// ContainerID is the driver-specific container identifier (for reattachment).
	ContainerID string `json:"container_id,omitempty"`
	// TaskID is the task identifier used by the orchestrator.
	TaskID string `json:"task_id,omitempty"`
	// StartedAt is when the step started executing.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// CompletedAt is when the step finished (successfully or not).
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// ExitCode is the container exit code (if completed).
	ExitCode *int `json:"exit_code,omitempty"`
	// Result stores the serialized step result for completed run steps.
	Result *RunResult `json:"result,omitempty"`
	// AgentResultJSON stores the serialized agent result for completed agent steps.
	// Stored as raw JSON to avoid import cycles with the agent package.
	AgentResultJSON json.RawMessage `json:"agent_result,omitempty"`
	// Error stores the error message if the step failed.
	Error string `json:"error,omitempty"`
}

// IsTerminal returns true if the step is in a terminal state (completed, failed, or aborted).
func (s *StepState) IsTerminal() bool {
	switch s.Status {
	case StepStatusCompleted, StepStatusFailed, StepStatusAborted:
		return true
	case StepStatusPending, StepStatusRunning:
		return false
	}

	return false
}

// IsResumable returns true if the step can be resumed (running state with a container ID).
func (s *StepState) IsResumable() bool {
	return s.Status == StepStatusRunning && s.ContainerID != ""
}

// CanSkip returns true if the step was already completed successfully and can be skipped on resume.
func (s *StepState) CanSkip() bool {
	if s.Status != StepStatusCompleted {
		return false
	}

	if s.Kind == StepKindAgent {
		return len(s.AgentResultJSON) > 0
	}

	return s.Result != nil
}

// ShouldRetry returns true if the step previously failed or was aborted and should be re-run.
func (s *StepState) ShouldRetry() bool {
	return s.Status == StepStatusFailed || s.Status == StepStatusAborted
}

// MarkForRetry resets a failed/aborted step so it can be re-executed.
func (s *StepState) MarkForRetry() {
	s.Status = StepStatusPending
	s.Error = ""
	s.Result = nil
	s.AgentResultJSON = nil
	s.ExitCode = nil
	s.CompletedAt = nil
	s.ContainerID = ""
}

// VolumeState represents the persisted state of a pipeline volume.
type VolumeState struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// PipelineState represents the persisted state of an entire pipeline run.
type PipelineState struct {
	// RunID is a unique identifier for this pipeline run.
	RunID string `json:"run_id"`
	// Steps contains the state of each step, keyed by step ID.
	Steps map[string]*StepState `json:"steps"`
	// StepOrder maintains the order in which steps were created.
	StepOrder []string `json:"step_order"`
	// Volumes contains the state of each volume, keyed by volume name.
	Volumes map[string]*VolumeState `json:"volumes,omitempty"`
	// StartedAt is when the pipeline run started.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// CompletedAt is when the pipeline run finished.
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// ResumeEnabled indicates if resumability was enabled for this run.
	ResumeEnabled bool `json:"resume_enabled"`
}

// NewPipelineState creates a new pipeline state for a run.
func NewPipelineState(runID string, resumeEnabled bool) *PipelineState {
	now := time.Now()
	return &PipelineState{
		RunID:         runID,
		Steps:         make(map[string]*StepState),
		StepOrder:     make([]string, 0),
		Volumes:       make(map[string]*VolumeState),
		StartedAt:     &now,
		ResumeEnabled: resumeEnabled,
	}
}

// GetStep returns the step state for a given step ID, or nil if not found.
func (p *PipelineState) GetStep(stepID string) *StepState {
	return p.Steps[stepID]
}

// SetStep adds or updates a step state.
func (p *PipelineState) SetStep(state *StepState) {
	if _, exists := p.Steps[state.StepID]; !exists {
		p.StepOrder = append(p.StepOrder, state.StepID)
	}
	p.Steps[state.StepID] = state
}

// LastStep returns the last step in execution order, or nil if no steps.
func (p *PipelineState) LastStep() *StepState {
	if len(p.StepOrder) == 0 {
		return nil
	}
	return p.Steps[p.StepOrder[len(p.StepOrder)-1]]
}

// InProgressSteps returns all steps that are currently running.
func (p *PipelineState) InProgressSteps() []*StepState {
	var result []*StepState
	for _, stepID := range p.StepOrder {
		if step := p.Steps[stepID]; step.Status == StepStatusRunning {
			result = append(result, step)
		}
	}
	return result
}
