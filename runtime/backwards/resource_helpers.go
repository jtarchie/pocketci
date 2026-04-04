package backwards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
)

// findResource looks up a resource by name from the pipeline config.
func findResource(resources config.Resources, name string) (*config.Resource, error) {
	for i := range resources {
		if resources[i].Name == name {
			return &resources[i], nil
		}
	}

	return nil, fmt.Errorf("resource %q not found", name)
}

// findResourceType looks up a resource type by name from the pipeline config.
func findResourceType(resourceTypes config.ResourceTypes, typeName string) (*config.ResourceType, error) {
	for i := range resourceTypes {
		if resourceTypes[i].Name == typeName {
			return &resourceTypes[i], nil
		}
	}

	return nil, fmt.Errorf("resource type %q not found", typeName)
}

// paramsToAnyMap converts map[string]string to map[string]any for the resources API.
func paramsToAnyMap(params map[string]string) map[string]any {
	if params == nil {
		return nil
	}

	m := make(map[string]any, len(params))
	for k, v := range params {
		m[k] = v
	}

	return m
}

// resolveLimit determines the effective concurrency limit.
// Priority: job MaxInFlight > step limit > step count (unlimited).
func resolveLimit(sc *StepContext, stepLimit, stepCount int) int {
	if sc.MaxInFlight > 0 {
		return sc.MaxInFlight
	}

	if stepLimit > 0 {
		return stepLimit
	}

	return stepCount
}

// resourceVolumeName returns the driver volume name for a resource mount.
func resourceVolumeName(runID, name string) string {
	return fmt.Sprintf("vol-%s-%s", runID, name)
}

// getScopedResourceName returns a scoped name for resource version storage.
// pipelineID is used to scope versions per-pipeline, matching the JS runtime's
// behaviour of `${pipelineID}/${resourceName}`.
func getScopedResourceName(pipelineID, resourceName string) string {
	if pipelineID == "" {
		pipelineID = "default"
	}

	return pipelineID + "/" + resourceName
}

// runResourceContainer runs a container task for resource operations (check, in, out).
// It returns the container's stdout and any error.
func runResourceContainer(sc *StepContext, taskName, image string, command []string, mounts orchestra.Mounts, stdinData []byte) (string, error) {
	task := orchestra.Task{
		ID:      fmt.Sprintf("%s-%s", sc.JobName, taskName),
		Command: command,
		Image:   image,
		Mounts:  mounts,
		Stdin:   bytes.NewReader(stdinData),
	}

	container, err := sc.Driver.RunContainer(sc.Ctx, task)
	if err != nil {
		return "", fmt.Errorf("run container %q: %w", taskName, err)
	}

	defer func() { _ = container.Cleanup(sc.Ctx) }()

	status, err := waitForContainerWithTimeout(sc, container)
	if err != nil {
		return "", fmt.Errorf("wait for container %q: %w", taskName, err)
	}

	var stdout, stderr bytes.Buffer

	err = container.Logs(sc.Ctx, &stdout, &stderr, false)
	if err != nil {
		sc.Logger.Error("resource.container.logs.error", "task", taskName, "err", err)
	}

	if status.ExitCode() != 0 {
		return "", fmt.Errorf("container %q exited with code %d: %s", taskName, status.ExitCode(), stderr.String())
	}

	return stdout.String(), nil
}

// waitForContainerWithTimeout polls a container for completion.
func waitForContainerWithTimeout(sc *StepContext, container orchestra.Container) (orchestra.ContainerStatus, error) {
	for {
		select {
		case <-sc.Ctx.Done():
			return nil, fmt.Errorf("context cancelled: %w", sc.Ctx.Err())
		default:
			status, err := container.Status(sc.Ctx)
			if err != nil {
				return nil, fmt.Errorf("container status: %w", err)
			}

			if status.IsDone() {
				return status, nil
			}

			time.Sleep(10 * time.Millisecond)
		}
	}
}

// resourceStdinJSON builds the JSON stdin payload for a resource container operation.
func resourceStdinJSON(fields map[string]any) ([]byte, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("marshal resource stdin: %w", err)
	}

	return data, nil
}
