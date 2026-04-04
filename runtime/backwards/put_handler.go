package backwards

import (
	"encoding/json"
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/resources"
	"github.com/jtarchie/pocketci/storage"
)

// PutHandler executes put steps by pushing a resource then implicitly fetching it.
type PutHandler struct{}

func (h *PutHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	resourceName := step.Put

	storageKey := fmt.Sprintf("%s/%s/put/%s", sc.BaseStorageKey(), pathPrefix, resourceName)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":   "pending",
		"resource": resourceName,
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	resource, err := findResource(sc.Resources, resourceName)
	if err != nil {
		return fmt.Errorf("put step: %w", err)
	}

	resourceType, err := findResourceType(sc.ResourceTypes, resource.Type)
	if err != nil {
		return fmt.Errorf("put step: %w", err)
	}

	isNative := sc.Driver.Name() == "native" && resources.IsNative(resource.Type)

	params := step.Params

	// Phase 1: Push (out).
	var version map[string]string

	if isNative {
		version, err = h.pushNative(sc, resource, params)
	} else {
		version, err = h.pushContainer(sc, resource, resourceType, params, resourceName, pathPrefix)
	}

	if err != nil {
		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
			"status":   "error",
			"resource": resourceName,
			"error":    err.Error(),
		})

		return fmt.Errorf("put step push: %w", err)
	}

	// Phase 2: Implicit get (in).
	noGet := step.PutConfig != nil && step.PutConfig.NoGet
	if !noGet {
		if isNative {
			err = h.fetchNative(sc, resource, version)
		} else {
			err = h.fetchContainer(sc, resource, resourceType, version, resourceName, pathPrefix)
		}

		if err != nil {
			_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
				"status":   "error",
				"resource": resourceName,
				"error":    err.Error(),
			})

			return fmt.Errorf("put step implicit get: %w", err)
		}
	}

	scopedName := getScopedResourceName(sc.PipelineID, resourceName)

	if saveErr := SaveResourceVersion(sc.Ctx, sc.Storage, scopedName, version, sc.JobName); saveErr != nil {
		return fmt.Errorf("put step save version: %w", saveErr)
	}

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":   "success",
		"resource": resourceName,
		"version":  versionToAnyMap(version),
	})
	if err != nil {
		return fmt.Errorf("storage set success: %w", err)
	}

	return nil
}

func (h *PutHandler) pushNative(
	sc *StepContext,
	resource *config.Resource,
	params map[string]string,
) (map[string]string, error) {
	res, err := resources.Get(resource.Type)
	if err != nil {
		return nil, fmt.Errorf("get native resource %q: %w", resource.Type, err)
	}

	// Use an existing known volume as srcDir if available, otherwise use empty string.
	srcDir := ""
	if volName, ok := sc.KnownVolumes[resource.Name]; ok {
		vol, volErr := sc.Driver.CreateVolume(sc.Ctx, volName, 0)
		if volErr == nil {
			srcDir = vol.Path()
		}
	}

	resp, err := res.Out(sc.Ctx, srcDir, resources.OutRequest{
		Source: resource.Source,
		Params: paramsToAnyMap(params),
	})
	if err != nil {
		return nil, fmt.Errorf("native push %q: %w", resource.Name, err)
	}

	sc.ExecutedTasksMu.Lock()
	sc.ExecutedTasks = append(sc.ExecutedTasks, "put-"+resource.Name)
	sc.ExecutedTasksMu.Unlock()

	return resp.Version, nil
}

func (h *PutHandler) pushContainer(
	sc *StepContext,
	resource *config.Resource,
	resourceType *config.ResourceType,
	params map[string]string,
	resourceName string,
	pathPrefix string,
) (map[string]string, error) {
	image, _ := resourceType.Source["repository"].(string)
	volName := fmt.Sprintf("vol-%s-%s", sc.RunID, resourceName)
	sc.KnownVolumes[resourceName] = volName

	mounts := orchestra.Mounts{
		{Name: volName, Path: resourceName},
	}

	stdinFields := map[string]any{
		"source": resource.Source,
	}
	if params != nil {
		stdinFields["params"] = paramsToAnyMap(params)
	}

	stdinData, err := resourceStdinJSON(stdinFields)
	if err != nil {
		return nil, err
	}

	taskName := fmt.Sprintf("put-%s-%s", resourceName, pathPrefix)

	stdout, err := runResourceContainer(sc, taskName, image, []string{"/opt/resource/out", "./" + resourceName}, mounts, stdinData)
	if err != nil {
		return nil, fmt.Errorf("container push %q: %w", resourceName, err)
	}

	sc.ExecutedTasksMu.Lock()
	sc.ExecutedTasks = append(sc.ExecutedTasks, "put-"+resourceName)
	sc.ExecutedTasksMu.Unlock()

	var result struct {
		Version map[string]string `json:"version"`
	}

	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		return nil, fmt.Errorf("parse push output for %q: %w", resourceName, err)
	}

	return result.Version, nil
}

func (h *PutHandler) fetchNative(
	sc *StepContext,
	resource *config.Resource,
	version map[string]string,
) error {
	volName := fmt.Sprintf("vol-%s-%s", sc.RunID, resource.Name)

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
	})
	if err != nil {
		return fmt.Errorf("native fetch after put %q: %w", resource.Name, err)
	}

	sc.ExecutedTasksMu.Lock()
	sc.ExecutedTasks = append(sc.ExecutedTasks, "get-"+resource.Name)
	sc.ExecutedTasksMu.Unlock()

	return nil
}

func (h *PutHandler) fetchContainer(
	sc *StepContext,
	resource *config.Resource,
	resourceType *config.ResourceType,
	version map[string]string,
	resourceName string,
	pathPrefix string,
) error {
	image, _ := resourceType.Source["repository"].(string)
	volName := fmt.Sprintf("vol-%s-%s", sc.RunID, resourceName)
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
		return fmt.Errorf("container fetch after put %q: %w", resourceName, err)
	}

	sc.ExecutedTasksMu.Lock()
	sc.ExecutedTasks = append(sc.ExecutedTasks, "get-"+resourceName)
	sc.ExecutedTasksMu.Unlock()

	return nil
}
