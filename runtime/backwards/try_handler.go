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

	for i, innerStep := range step.Try {
		innerPrefix := fmt.Sprintf("%s/try/%s", pathPrefix, zeroPadWithLength(i, len(step.Try)))

		if stepErr := sc.ProcessStep(&innerStep, innerPrefix); stepErr != nil {
			sc.Logger.Debug("try.step.swallowed", "step", i, "err", stepErr)

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
