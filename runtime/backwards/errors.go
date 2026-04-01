package backwards

import (
	"errors"
	"fmt"
)

// ErrAssertionFailed is the sentinel used by errors.Is to detect assertion failures.
var ErrAssertionFailed = errors.New("assertion failed")

// TaskFailedError indicates a task exited with a non-zero code.
type TaskFailedError struct {
	TaskName string
	Code     int
}

func (e *TaskFailedError) Error() string {
	return fmt.Sprintf("task %q failed with exit code %d", e.TaskName, e.Code)
}

// AssertionError wraps ErrAssertionFailed with a descriptive message.
type AssertionError struct {
	Message string
}

func (e *AssertionError) Error() string {
	return fmt.Sprintf("assertion failed: %s", e.Message)
}

func (e *AssertionError) Unwrap() error {
	return ErrAssertionFailed
}
