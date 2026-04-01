package backwards

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

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
	Ctx           context.Context
	Driver        orchestra.Driver
	Storage       storage.Driver
	Logger        *slog.Logger
	RunID         string
	JobName       string
	ExecutedTasks []string
	ProcessStep   func(step *config.Step, pathPrefix string) error
}

// BaseStorageKey returns the storage prefix for the current job.
func (sc *StepContext) BaseStorageKey() string {
	return fmt.Sprintf("/pipeline/%s/jobs/%s", sc.RunID, sc.JobName)
}

// zeroPadWithLength zero-pads num based on the number of digits in (length-1).
func zeroPadWithLength(num, length int) string {
	if length <= 1 {
		return strconv.Itoa(num)
	}

	places := len(strconv.Itoa(length - 1))

	return fmt.Sprintf("%0*d", places, num)
}
