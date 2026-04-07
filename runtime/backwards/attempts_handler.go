package backwards

import (
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
)

func (jr *JobRunner) runAttemptFailureHook(sc *StepContext, step *config.Step, pathPrefix string, lastErr error) error {
	switch {
	case isAbortError(lastErr) && step.OnAbort != nil:
		sc.Logger.Debug(lastErr.Error())
		sc.FailureCount++
		sc.LastFailureKind = FailureKindAborted

		abortPrefix := pathPrefix + "/on_abort"
		err := jr.processStep(sc, step.OnAbort, abortPrefix)
		if err != nil {
			sc.Logger.Warn("step.on_abort.failed", "prefix", pathPrefix, "error", err)
		}

		return nil
	case isErroredError(lastErr) && step.OnError != nil:
		sc.Logger.Debug(lastErr.Error())
		sc.FailureCount++
		sc.LastFailureKind = FailureKindErrored

		errorPrefix := pathPrefix + "/on_error"
		err := jr.processStep(sc, step.OnError, errorPrefix)
		if err != nil {
			sc.Logger.Warn("step.on_error.failed", "prefix", pathPrefix, "error", err)
		}

		return nil
	case isFailedError(lastErr) && step.OnFailure != nil:
		sc.FailureCount++
		sc.LastFailureKind = FailureKindFailed

		failurePrefix := pathPrefix + "/on_failure"
		err := jr.processStep(sc, step.OnFailure, failurePrefix)
		if err != nil {
			sc.Logger.Warn("step.on_failure.failed", "prefix", pathPrefix, "error", err)
		}

		return nil
	}

	return lastErr
}

// executeWithAttempts retries a step up to step.Attempts times.
// Hooks are stripped from inner attempts and handled once after all attempts.
func (jr *JobRunner) executeWithAttempts(sc *StepContext, step *config.Step, pathPrefix string) error {
	maxAttempts := step.Attempts

	// Clone the step and strip hooks + attempts to prevent re-processing.
	stripped := *step
	stripped.Attempts = 0
	stripped.Ensure = nil
	stripped.OnFailure = nil
	stripped.OnSuccess = nil
	stripped.OnError = nil
	stripped.OnAbort = nil

	var lastErr error

	succeeded := false

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptPrefix := fmt.Sprintf("%s/attempt/%d", pathPrefix, attempt)
		err := jr.processStep(sc, &stripped, attemptPrefix)
		if err != nil {
			lastErr = err
			sc.Logger.Debug("attempt.failed", "attempt", attempt, "max", maxAttempts, "err", err)

			continue
		}

		succeeded = true

		break
	}

	// Run the appropriate hook once after all attempts.
	if succeeded {
		if step.OnSuccess != nil {
			successPrefix := pathPrefix + "/on_success"
			err := jr.processStep(sc, step.OnSuccess, successPrefix)
			if err != nil {
				lastErr = err
			}
		}
	} else {
		lastErr = jr.runAttemptFailureHook(sc, step, pathPrefix, lastErr)
	}

	// Ensure always runs after attempts and hooks.
	if step.Ensure != nil {
		ensurePrefix := pathPrefix + "/ensure"
		err := jr.processStep(sc, step.Ensure, ensurePrefix)
		if err != nil {
			sc.Logger.Warn("step.ensure.failed", "prefix", pathPrefix, "error", err)
		}
	}

	return lastErr
}
