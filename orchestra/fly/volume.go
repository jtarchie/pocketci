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
		// No shared volume yet — create one.
		volumeName := sanitizeVolumeName(f.namespace + "_workspace")

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
			return nil, fmt.Errorf("failed to create fly shared volume %q: %w", volumeName, err)
		}

		f.trackVolume(vol.ID)
		f.logger.Info("fly.volume.created.shared", "volume", vol.ID, "name", volumeName)

		f.mu.Lock()
		f.sharedVolumeID = vol.ID
		sharedID = vol.ID
		f.mu.Unlock()
	}

	// Create a logical volume backed by the shared physical volume.
	// Path is the subdirectory under /workspace in the container.
	v := &Volume{
		id:             sharedID,
		name:           sanitizeVolumeName(fmt.Sprintf("%s_%s", f.namespace, name)),
		userFacingName: name,
		path:           "/workspace/" + name,
		driver:         f,
	}

	f.mu.Lock()
	f.volumes[name] = v
	f.mu.Unlock()

	return v, nil
}
