package backwards

import (
	"context"
	"fmt"
	"strings"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/storage"
)

// NotifyHandler sends notifications to configured backends.
type NotifyHandler struct{}

func (h *NotifyHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	identifier := notifyIdentifier(step)
	storageKey := fmt.Sprintf("%s/%s/notify/%s", sc.BaseStorageKey(), pathPrefix, identifier)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": "pending",
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	if sc.Notifier == nil {
		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
			"status": "failure",
		})

		return &TaskFailedError{TaskName: "notify/" + identifier, Code: 1}
	}

	// Update notifier context with current job name and running status.
	sc.Notifier.UpdateContext(func(ctx *jsapi.NotifyContext) {
		ctx.JobName = sc.JobName
		ctx.Status = "running"
	})

	names := step.NotifyNames()

	message := step.Message

	if step.MessageFile != "" {
		contents, err := loadRawBytesFromVolume(sc, step.MessageFile)
		if err != nil {
			_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
				"status": "failure",
			})

			return &TaskFailedError{TaskName: "notify/" + identifier, Code: 1}
		}

		message = string(contents)
	}

	var sendErr error

	if step.Async {
		// Fire-and-forget: launch goroutines with a detached context so they
		// survive after the step (and possibly the pipeline) finishes.
		for _, name := range names {
			go func(n, msg string) {
				err := sc.Notifier.Send(context.Background(), n, msg)
				if err != nil {
					sc.Logger.Error("notification.async.failed",
						"name", n,
						"error", err,
					)
				}
			}(name, message)
		}
	} else {
		for _, name := range names {
			err := sc.Notifier.Send(sc.Ctx, name, message)
			if err != nil {
				sendErr = err

				break
			}
		}
	}

	_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": statusFromErr(sendErr),
	})

	if sendErr != nil {
		return &TaskFailedError{TaskName: "notify/" + identifier, Code: 1}
	}

	return nil
}

// notifyIdentifier returns a stable identifier for the notify step.
// Single name: the name itself. Multiple names: joined with "-".
func notifyIdentifier(step *config.Step) string {
	names := step.NotifyNames()
	if len(names) == 0 {
		return "unknown"
	}

	return strings.Join(names, "-")
}
