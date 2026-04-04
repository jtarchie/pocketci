package backwards

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

// StepHandler executes a single step type (task, try, do, etc.).
type StepHandler interface {
	Execute(ctx *StepContext, step *config.Step, pathPrefix string) error
}

// FailureKind identifies the type of failure that was handled by a step hook.
type FailureKind int

const (
	FailureKindNone    FailureKind = 0
	FailureKindFailed  FailureKind = 1
	FailureKindErrored FailureKind = 2
	FailureKindAborted FailureKind = 3
)

// StepContext carries shared state through step execution.
type StepContext struct {
	// --- Execution infrastructure ---
	Ctx     context.Context //nolint:containedctx // step handlers are called via StepHandler interface that cannot accept context parameters
	Driver  orchestra.Driver
	Storage storage.Driver
	Logger  *slog.Logger

	// --- Job identity ---
	RunID       string
	JobName     string
	PipelineID  string // pipeline scope for secrets and resource version scoping
	MaxInFlight int
	JobParams   map[string]string // webhook trigger params, injected as base env into task steps

	// --- Task execution tracking (protected by ExecutedTasksMu) ---
	ExecutedTasks   []string
	ExecutedTasksMu sync.Mutex
	// PreRegisteredTasks holds task names already appended to ExecutedTasks
	// (e.g. by InParallelHandler) to avoid double-counting.
	PreRegisteredTasks map[string]bool
	// FailureCount tracks how many failures have been handled by step-level hooks.
	// Use the delta between before/after handler.Execute to detect per-step failures.
	FailureCount int
	// LastFailureKind is the kind of the most recently handled failure.
	LastFailureKind FailureKind
	// ProcessStep dispatches a step to its handler; set by JobRunner.
	ProcessStep func(step *config.Step, pathPrefix string) error

	// --- Volume and cache management ---
	CacheVolumes map[string]string // maps cache path → volume name, shared across tasks in a job
	// CacheVolumeObjects holds volume objects for each cache path so Cleanup()
	// can persist contents to S3 after task execution.
	CacheVolumeObjects map[string]orchestra.Volume
	KnownVolumes       map[string]string // maps mount name → driver volume name, shared across tasks in a job

	// --- Pipeline resources ---
	Resources     config.Resources
	ResourceTypes config.ResourceTypes

	// --- Notification and agent subsystems ---
	Notifier       *jsapi.Notifier
	PipelineRunner pipelinerunner.Runner // for agent sandbox/volume creation
	SecretsManager secrets.Manager       // for agent API key resolution
	AgentBaseURLs  map[string]string     // overrides agent provider base URLs; used in tests to avoid global state
}

// appendExecutedTask appends a task name to ExecutedTasks under the mutex.
func (sc *StepContext) appendExecutedTask(name string) {
	sc.ExecutedTasksMu.Lock()
	sc.ExecutedTasks = append(sc.ExecutedTasks, name)
	sc.ExecutedTasksMu.Unlock()
}

// BaseStorageKey returns the storage prefix for the current job.
func (sc *StepContext) BaseStorageKey() string {
	return fmt.Sprintf("/pipeline/%s/jobs/%s", sc.RunID, sc.JobName)
}

// statusFromErr returns "failure" if err is non-nil, "success" otherwise.
func statusFromErr(err error) string {
	if err != nil {
		return "failure"
	}

	return "success"
}

// validateExecution compares expected vs actual execution order and returns an
// AssertionError if they differ. The label identifies the scope (e.g. "pipeline", "job \"name\"").
func validateExecution(label string, expected, got []string) error {
	if expected == nil {
		return nil
	}

	if len(expected) != len(got) {
		return &AssertionError{
			Message: fmt.Sprintf("%s execution: expected %s, got %s",
				label, formatList(expected), formatList(got)),
		}
	}

	for i := range expected {
		if expected[i] != got[i] {
			return &AssertionError{
				Message: fmt.Sprintf("%s execution[%d]: expected %q, got %q",
					label, i, expected[i], got[i]),
			}
		}
	}

	return nil
}

// zeroPadWithLength zero-pads num based on the number of digits in (length-1).
func zeroPadWithLength(num, length int) string {
	if length <= 1 {
		return strconv.Itoa(num)
	}

	places := len(strconv.Itoa(length - 1))

	return fmt.Sprintf("%0*d", places, num)
}
