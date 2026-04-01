package backwards

import (
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
)

// DoHandler executes inner steps sequentially and propagates errors.
type DoHandler struct{}

func (h *DoHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	storageKey := fmt.Sprintf("%s/%s/do", sc.BaseStorageKey(), pathPrefix)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": "pending",
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	var stepErr error

	for i, innerStep := range step.Do {
		innerPrefix := fmt.Sprintf("%s/do/%s", pathPrefix, zeroPadWithLength(i, len(step.Do)))

		if err := sc.ProcessStep(&innerStep, innerPrefix); err != nil {
			stepErr = err

			break
		}
	}

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": statusFromErr(stepErr),
	})
	if err != nil {
		return fmt.Errorf("storage set result: %w", err)
	}

	return stepErr
}
