package backwards

import (
	"encoding/json"
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/resources"
	"github.com/jtarchie/pocketci/storage"
)

// GetHandler executes get steps by checking for versions and fetching resources.
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

	resource, err := findResource(sc.Resources, resourceName)
	if err != nil {
		return fmt.Errorf("get step: %w", err)
	}

	resourceType, err := findResourceType(sc.ResourceTypes, resource.Type)
	if err != nil {
		return fmt.Errorf("get step: %w", err)
	}

	versionMode := step.GetConfig.GetVersionMode()
	scopedName := getScopedResourceName(resourceName)
	isNative := sc.Driver.Name() == "native" && resources.IsNative(resource.Type)

	version, err := h.resolveVersionToFetch(sc, step, resource, resourceType, versionMode, scopedName, isNative, pathPrefix)
	if err != nil {
		return fmt.Errorf("get step resolve version: %w", err)
	}

	if isNative {
		err = h.fetchNative(sc, resource, version, step, storageKey)
	} else {
		err = h.fetchContainer(sc, resource, resourceType, version, resourceName, pathPrefix)
	}

	if err != nil {
		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
			"status":   "error",
			"resource": resourceName,
			"error":    err.Error(),
		})

		return fmt.Errorf("get step fetch: %w", err)
	}

	if saveErr := SaveResourceVersion(sc.Ctx, sc.Storage, scopedName, version, sc.JobName); saveErr != nil {
		return fmt.Errorf("get step save version: %w", saveErr)
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

func (h *GetHandler) resolveVersionToFetch(
	sc *StepContext,
	step *config.Step,
	resource *config.Resource,
	resourceType *config.ResourceType,
	versionMode string,
	scopedName string,
	isNative bool,
	pathPrefix string,
) (map[string]string, error) {
	if versionMode == "pinned" {
		pinned := step.GetConfig.GetPinnedVersion()
		if pinned == nil {
			return nil, fmt.Errorf("pinned version is nil for resource %q", resource.Name)
		}

		return pinned, nil
	}

	var lastKnownVersion map[string]string

	if versionMode == "every" {
		stored, err := GetLatestResourceVersion(sc.Ctx, sc.Storage, scopedName)
		if err != nil {
			return nil, fmt.Errorf("get latest stored version: %w", err)
		}

		if stored != nil {
			lastKnownVersion = stored.Version
		}
	}

	versions, err := h.checkVersions(sc, resource, resourceType, lastKnownVersion, isNative, pathPrefix)
	if err != nil {
		return nil, err
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions found for resource %q", resource.Name)
	}

	if versionMode == "every" {
		return h.resolveEveryVersion(sc, scopedName, versions)
	}

	// "latest" mode: return the last version.
	return versions[len(versions)-1], nil
}

func (h *GetHandler) checkVersions(
	sc *StepContext,
	resource *config.Resource,
	resourceType *config.ResourceType,
	lastKnownVersion map[string]string,
	isNative bool,
	pathPrefix string,
) ([]map[string]string, error) {
	if isNative {
		return h.checkNative(sc, resource, lastKnownVersion)
	}

	return h.checkContainer(sc, resource, resourceType, lastKnownVersion, pathPrefix)
}

func (h *GetHandler) checkNative(
	sc *StepContext,
	resource *config.Resource,
	lastKnownVersion map[string]string,
) ([]map[string]string, error) {
	res, err := resources.Get(resource.Type)
	if err != nil {
		return nil, fmt.Errorf("get native resource %q: %w", resource.Type, err)
	}

	resp, err := res.Check(sc.Ctx, resources.CheckRequest{
		Source:  resource.Source,
		Version: lastKnownVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("native check %q: %w", resource.Name, err)
	}

	versions := make([]map[string]string, len(resp))
	for i, v := range resp {
		versions[i] = v
	}

	return versions, nil
}

func (h *GetHandler) checkContainer(
	sc *StepContext,
	resource *config.Resource,
	resourceType *config.ResourceType,
	lastKnownVersion map[string]string,
	pathPrefix string,
) ([]map[string]string, error) {
	image, _ := resourceType.Source["repository"].(string)

	stdinFields := map[string]any{
		"source": resource.Source,
	}
	if lastKnownVersion != nil {
		stdinFields["version"] = lastKnownVersion
	}

	stdinData, err := resourceStdinJSON(stdinFields)
	if err != nil {
		return nil, err
	}

	taskName := fmt.Sprintf("check-%s-%s", resource.Name, pathPrefix)

	stdout, err := runResourceContainer(sc, taskName, image, []string{"/opt/resource/check"}, nil, stdinData)
	if err != nil {
		return nil, fmt.Errorf("container check %q: %w", resource.Name, err)
	}

	var versions []map[string]string
	if err := json.Unmarshal([]byte(stdout), &versions); err != nil {
		return nil, fmt.Errorf("parse check output for %q: %w", resource.Name, err)
	}

	return versions, nil
}

func (h *GetHandler) resolveEveryVersion(
	sc *StepContext,
	scopedName string,
	versions []map[string]string,
) (map[string]string, error) {
	storedVersions, err := ListResourceVersions(sc.Ctx, sc.Storage, scopedName, 0)
	if err != nil {
		return nil, fmt.Errorf("list stored versions: %w", err)
	}

	processedSet := make(map[string]struct{}, len(storedVersions))
	for _, sv := range storedVersions {
		key, err := json.Marshal(sv.Version)
		if err != nil {
			continue
		}

		processedSet[string(key)] = struct{}{}
	}

	for _, v := range versions {
		key, err := json.Marshal(v)
		if err != nil {
			continue
		}

		if _, exists := processedSet[string(key)]; !exists {
			return v, nil
		}
	}

	// All versions already processed — return the last available.
	return versions[len(versions)-1], nil
}

func (h *GetHandler) fetchNative(
	sc *StepContext,
	resource *config.Resource,
	version map[string]string,
	step *config.Step,
	storageKey string,
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
		Params:  paramsToAnyMap(step.GetConfig.Params),
	})
	if err != nil {
		return fmt.Errorf("native fetch %q: %w", resource.Name, err)
	}

	return nil
}

func (h *GetHandler) fetchContainer(
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
		return fmt.Errorf("container fetch %q: %w", resourceName, err)
	}

	return nil
}
