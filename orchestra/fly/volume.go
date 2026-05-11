package fly

import (
	"context"
	"fmt"

	fly "github.com/superfly/fly-go"

	"github.com/jtarchie/pocketci/orchestra"
)

type Volume struct {
	id             string
	name           string // sanitized internal name for Fly API
	userFacingName string // original name passed by the user
	path           string
	driver         *Fly
}

func (v *Volume) Name() string {
	return v.userFacingName
}

func (v *Volume) Path() string {
	return v.path
}

func (v *Volume) Cleanup(_ context.Context) error {
	// Per-logical-volume cleanup is a no-op on Fly because every logical
	// volume shares one physical Fly volume (`<namespace>_workspace`).
	// Deleting the physical volume from the first cleanup invalidated all
	// other logical volumes (and any in-flight cache persist that needs to
	// mount the volume on a helper machine — see issue with the cache
	// helper getting "volume not found").
	//
	// Final teardown of the shared volume + any persistent cache helpers
	// happens in `Fly.Close()`, which iterates `volumeIDs` and helper
	// machines once at the end of the run.
	v.driver.logger.Debug("fly.volume.cleanup.noop", "volume", v.id, "name", v.name)

	return nil
}

func (f *Fly) CreateVolume(ctx context.Context, name string, size int) (orchestra.Volume, error) {
	// Check if we already have a logical volume with this name.
	f.mu.Lock()
	existing, ok := f.volumes[name]
	f.mu.Unlock()

	if ok {
		f.logger.Info("fly.volume.reuse", "volume", existing.id, "name", name)
		return existing, nil
	}

	// All logical volumes share a single physical Fly volume per namespace.
	// This avoids the Fly API's 1-volume-per-machine limit.
	f.mu.Lock()
	sharedID := f.sharedVolumeID
	f.mu.Unlock()

	if sharedID == "" {
		newID, err := f.createSharedVolume(ctx, size)
		if err != nil {
			return nil, err
		}

		sharedID = newID
	} else {
		err := f.extendSharedVolumeIfNeeded(ctx, sharedID, size)
		if err != nil {
			return nil, err
		}
	}

	// Create a logical volume backed by the shared physical volume.
	// Path is the subdirectory under /workspace in the container.
	v := &Volume{
		id:             sharedID,
		name:           SanitizeVolumeName(fmt.Sprintf("%s_%s", f.namespace, name)),
		userFacingName: name,
		path:           "/workspace/" + name,
		driver:         f,
	}

	f.mu.Lock()
	f.volumes[name] = v
	f.mu.Unlock()

	return v, nil
}

// createSharedVolume creates the per-namespace shared Fly volume. Called once
// per Fly driver lifetime (the first CreateVolume call). Returns the new
// volume's Fly ID.
func (f *Fly) createSharedVolume(ctx context.Context, size int) (string, error) {
	volumeName := SanitizeVolumeName(f.namespace + "_workspace")

	if size <= 0 {
		size = f.diskGB
	}

	if size <= 0 {
		size = 10 // default 10 GB — enough for Go modules, Deno, node_modules
	}

	f.logger.Debug("fly.volume.create.shared", "name", volumeName, "size_gb", size, "region", f.region)

	vol, err := f.client.CreateVolume(ctx, f.appName, fly.CreateVolumeRequest{
		Name:   volumeName,
		Region: f.region,
		SizeGb: &size,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create fly shared volume %q: %w", volumeName, err)
	}

	f.trackVolume(vol.ID)
	f.logger.Info("fly.volume.created.shared", "volume", vol.ID, "name", volumeName)

	f.mu.Lock()
	f.sharedVolumeID = vol.ID
	f.sharedVolumeSize = size
	f.mu.Unlock()

	return vol.ID, nil
}

// extendSharedVolumeIfNeeded grows the shared volume when a later CreateVolume
// call requests more capacity than what was provisioned. The Fly Firecracker
// init resizes the filesystem on next attach, so the next machine boot will
// see the larger /workspace.
//
// Fly volumes cannot be shrunk: a later request for a smaller size silently
// keeps the larger existing volume. We log a warn so operators can spot the
// (billing-relevant) case where a per-cache `size_gb` is smaller than an
// earlier cache's allocation in the same run.
func (f *Fly) extendSharedVolumeIfNeeded(ctx context.Context, sharedID string, size int) error {
	if size <= 0 {
		return nil
	}

	f.mu.Lock()
	currentSize := f.sharedVolumeSize
	f.mu.Unlock()

	if size < currentSize {
		f.logger.Warn("fly.volume.shrink.ignored",
			"volume", sharedID,
			"requested_gb", size,
			"current_gb", currentSize,
			"reason", "fly volumes cannot be shrunk; keeping larger size",
		)

		return nil
	}

	if size == currentSize {
		return nil
	}

	f.logger.Debug("fly.volume.extend", "volume", sharedID, "from_gb", currentSize, "to_gb", size)

	_, _, err := f.client.ExtendVolume(ctx, f.appName, sharedID, size)
	if err != nil {
		return fmt.Errorf("failed to extend fly shared volume %q to %d GB: %w", sharedID, size, err)
	}

	f.mu.Lock()
	f.sharedVolumeSize = size
	f.mu.Unlock()

	f.logger.Info("fly.volume.extended", "volume", sharedID, "size_gb", size)

	return nil
}
