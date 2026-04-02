package backwards

import (
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
)

// GetHandler executes get steps as no-ops for backwards compatibility.
// Real resource fetching is handled by the TS runtime; this handler exists
// so the Go-native runner can process pipelines containing get steps.
type GetHandler struct{}

func (h *GetHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	resourceName := step.Get

	storageKey := fmt.Sprintf("%s/%s/get/%s", sc.BaseStorageKey(), pathPrefix, resourceName)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":   "pending",
		"resource": resourceName,
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	sc.Logger.Debug("get.step", "resource", resourceName)

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":   "success",
		"resource": resourceName,
	})
	if err != nil {
		return fmt.Errorf("storage set success: %w", err)
	}

	return nil
}
