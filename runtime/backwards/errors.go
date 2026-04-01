package backwards

import (
	"errors"
	"fmt"
)

// isAbortError reports whether err is a TaskAbortedError.
func isAbortError(err error) bool {
	var abortErr *TaskAbortedError
	return errors.As(err, &abortErr)
}

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

// TaskAbortedError indicates a task was aborted due to a timeout.
type TaskAbortedError struct {
	TaskName string
}

func (e *TaskAbortedError) Error() string {
	return fmt.Sprintf("Task %s aborted", e.TaskName)
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
