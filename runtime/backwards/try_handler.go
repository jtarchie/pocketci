package backwards

import (
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
)

// TryHandler executes inner steps sequentially and swallows any errors.
type TryHandler struct{}

func (h *TryHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	storageKey := fmt.Sprintf("%s/%s/try", sc.BaseStorageKey(), pathPrefix)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": "pending",
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	// Track failure state before executing inner steps so we can detect
	// failures that were handled by inner step hooks (on_failure etc.) and
	// propagate them to the outer try step's own hooks. This ensures that
	// `try { do { fail } on_failure: ... } on_failure: outer` fires the
	// outer on_failure even though the inner on_failure already handled it.
	failureBefore := sc.FailureCount

	for i, innerStep := range step.Try {
		innerPrefix := fmt.Sprintf("%s/try/%s", pathPrefix, zeroPadWithLength(i, len(step.Try)))

		stepErr := sc.ProcessStep(&innerStep, innerPrefix)
		if stepErr != nil {
			sc.Logger.Debug("try.step.swallowed", "step", i, "err", stepErr)
			// Record the failure kind if it wasn't already captured by a
			// step-level hook inside processStep.
			if sc.FailureCount == failureBefore {
				sc.FailureCount++
				switch {
				case isAbortError(stepErr):
					sc.LastFailureKind = FailureKindAborted
				case isErroredError(stepErr):
					sc.LastFailureKind = FailureKindErrored
				default:
					sc.LastFailureKind = FailureKindFailed
				}
			}

			break
		}

		// If a step-level hook handled a failure inside this step (returning nil
		// but incrementing FailureCount), treat it as a failure for the try block:
		// stop executing further steps.
		if sc.FailureCount > failureBefore {
			break
		}
	}

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": "success",
	})
	if err != nil {
		return fmt.Errorf("storage set success: %w", err)
	}

	return nil
}
