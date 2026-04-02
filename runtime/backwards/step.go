package backwards

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
)

// StepHandler executes a single step type (task, try, do, etc.).
type StepHandler interface {
	Execute(ctx *StepContext, step *config.Step, pathPrefix string) error
}

// StepContext carries shared state through step execution.
type StepContext struct {
	Ctx             context.Context
	Driver          orchestra.Driver
	Storage         storage.Driver
	Logger          *slog.Logger
	RunID           string
	JobName         string
	ExecutedTasks   []string
	ExecutedTasksMu sync.Mutex
	MaxInFlight     int
	HadFailure      bool // true when a step failed, even if handled by a step-level hook
	ProcessStep     func(step *config.Step, pathPrefix string) error
	CacheVolumes    map[string]string // maps cache path → volume name, shared across tasks in a job
	KnownVolumes    map[string]string // maps mount name → driver volume name, shared across tasks in a job
	Resources       config.Resources
	ResourceTypes   config.ResourceTypes
	JobParams       map[string]string // webhook trigger params, injected as base env into task steps
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
