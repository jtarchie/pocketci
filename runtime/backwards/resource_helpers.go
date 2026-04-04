package backwards

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/resources"
	"github.com/samber/lo"
)

// findResource looks up a resource by name from the pipeline config.
func findResource(rs config.Resources, name string) (*config.Resource, error) {
	r, ok := lo.Find(rs, func(r config.Resource) bool { return r.Name == name })
	if !ok {
		return nil, fmt.Errorf("resource %q not found", name)
	}

	return &r, nil
}

// findResourceType looks up a resource type by name from the pipeline config.
func findResourceType(rts config.ResourceTypes, typeName string) (*config.ResourceType, error) {
	rt, ok := lo.Find(rts, func(rt config.ResourceType) bool { return rt.Name == typeName })
	if !ok {
		return nil, fmt.Errorf("resource type %q not found", typeName)
	}

	return &rt, nil
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

	status, err := waitForContainer(sc.Ctx, container)
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

// waitForContainer polls a container for completion, respecting context cancellation.
func waitForContainer(ctx context.Context, container orchestra.Container) (orchestra.ContainerStatus, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
			status, err := container.Status(ctx)
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

// fetchNativeResource fetches a resource version using the native resource API.
// params is nil for put's implicit get (no step params needed).
func fetchNativeResource(sc *StepContext, resource *config.Resource, version map[string]string, params map[string]string) error {
	volName := resourceVolumeName(sc.RunID, resource.Name)

	vol, err := sc.Driver.CreateVolume(sc.Ctx, volName, 0)
	if err != nil {
		return fmt.Errorf("create volume for %q: %w", resource.Name, err)
	}

	sc.KnownVolumes[resource.Name] = volName

	res, err := resources.Get(resource.Type)
	if err != nil {
		return fmt.Errorf("get native resource %q: %w", resource.Type, err)
	}

	_, err = res.In(sc.Ctx, vol.Path(), resources.InRequest{
		Source:  resource.Source,
		Version: version,
		Params:  paramsToAnyMap(params),
	})
	if err != nil {
		return fmt.Errorf("native fetch %q: %w", resource.Name, err)
	}

	sc.appendExecutedTask("get-" + resource.Name)

	return nil
}

// fetchContainerResource fetches a resource version using a resource container (/opt/resource/in).
func fetchContainerResource(sc *StepContext, resource *config.Resource, resourceType *config.ResourceType, version map[string]string, resourceName, pathPrefix string) error {
	image, _ := resourceType.Source["repository"].(string)
	volName := resourceVolumeName(sc.RunID, resourceName)
	sc.KnownVolumes[resourceName] = volName

	mounts := orchestra.Mounts{
		{Name: volName, Path: resourceName},
	}

	stdinData, err := resourceStdinJSON(map[string]any{
		"source":  resource.Source,
		"version": version,
	})
	if err != nil {
		return err
	}

	taskName := fmt.Sprintf("get-%s-%s", resourceName, pathPrefix)

	_, err = runResourceContainer(sc, taskName, image, []string{"/opt/resource/in", "./" + resourceName}, mounts, stdinData)
	if err != nil {
		return fmt.Errorf("container fetch %q: %w", resourceName, err)
	}

	sc.appendExecutedTask("get-" + resourceName)

	return nil
}

// resourceStdinJSON builds the JSON stdin payload for a resource container operation.
func resourceStdinJSON(fields map[string]any) ([]byte, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("marshal resource stdin: %w", err)
	}

	return data, nil
}
