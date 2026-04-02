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

// isErroredError reports whether err is a TaskErroredError.
func isErroredError(err error) bool {
	var erroredErr *TaskErroredError
	return errors.As(err, &erroredErr)
}

// isFailedError reports whether err is a TaskFailedError.
func isFailedError(err error) bool {
	var failedErr *TaskFailedError
	return errors.As(err, &failedErr)
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

// TaskErroredError indicates a task encountered an infrastructure error.
type TaskErroredError struct {
	TaskName string
	Err      error
}

func (e *TaskErroredError) Error() string {
	return fmt.Sprintf("Task %s errored", e.TaskName)
}

func (e *TaskErroredError) Unwrap() error {
	return e.Err
}

// AssertionError wraps ErrAssertionFailed with a descriptive message.
type AssertionError struct {
	Message string
}

func (e *AssertionError) Error() string {
	return "assertion failed: " + e.Message
}

func (e *AssertionError) Unwrap() error {
	return ErrAssertionFailed
}
