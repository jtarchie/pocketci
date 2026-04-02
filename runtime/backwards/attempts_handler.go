package backwards

import (
	config "github.com/jtarchie/pocketci/backwards"
)

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
		if err := jr.processStep(sc, &stripped, pathPrefix); err != nil {
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
			if err := jr.processStep(sc, step.OnSuccess, successPrefix); err != nil {
				lastErr = err
			}
		}
	} else {
		switch {
		case isAbortError(lastErr) && step.OnAbort != nil:
			sc.Logger.Debug(lastErr.Error())
			sc.HadFailure = true

			abortPrefix := pathPrefix + "/on_abort"
			if err := jr.processStep(sc, step.OnAbort, abortPrefix); err != nil {
				sc.Logger.Warn("step.on_abort.failed", "prefix", pathPrefix, "error", err)
			}

			lastErr = nil
		case isErroredError(lastErr) && step.OnError != nil:
			sc.Logger.Debug(lastErr.Error())
			sc.HadFailure = true

			errorPrefix := pathPrefix + "/on_error"
			if err := jr.processStep(sc, step.OnError, errorPrefix); err != nil {
				sc.Logger.Warn("step.on_error.failed", "prefix", pathPrefix, "error", err)
			}

			lastErr = nil
		case isFailedError(lastErr) && step.OnFailure != nil:
			sc.HadFailure = true

			failurePrefix := pathPrefix + "/on_failure"
			if err := jr.processStep(sc, step.OnFailure, failurePrefix); err != nil {
				sc.Logger.Warn("step.on_failure.failed", "prefix", pathPrefix, "error", err)
			}

			lastErr = nil
		}
	}

	// Ensure always runs after attempts and hooks.
	if step.Ensure != nil {
		ensurePrefix := pathPrefix + "/ensure"
		if err := jr.processStep(sc, step.Ensure, ensurePrefix); err != nil {
			sc.Logger.Warn("step.ensure.failed", "prefix", pathPrefix, "error", err)
		}
	}

	return lastErr
}
