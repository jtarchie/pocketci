package fly

import (
	"context"
	"fmt"
	"time"

	fly "github.com/superfly/fly-go"
	"github.com/superfly/fly-go/flaps"

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

func (v *Volume) Cleanup(ctx context.Context) error {
	v.driver.logger.Debug("fly.volume.cleanup", "volume", v.id, "name", v.name)

	// Destroy any helper machine attached to this volume before deleting it,
	// otherwise the Fly API rejects the delete with "volume is currently bound to machine".
	v.driver.mu.Lock()
	helperID := v.driver.helperMachines[v.id]
	v.driver.mu.Unlock()

	if helperID != "" {
		v.driver.logger.Debug("fly.volume.cleanup.helper", "volume", v.id, "machine", helperID)

		_ = v.driver.client.Kill(ctx, v.driver.appName, helperID)

		machine := &fly.Machine{ID: helperID}
		_ = v.driver.client.Wait(ctx, v.driver.appName, machine.ID, flaps.WithWaitStates("stopped"), flaps.WithWaitTimeout(30*time.Second))

		_ = v.driver.client.Destroy(ctx, v.driver.appName, fly.RemoveMachineInput{
			ID:   helperID,
			Kill: true,
		}, "")

		v.driver.mu.Lock()
		delete(v.driver.helperMachines, v.id)
		delete(v.driver.volumeAttachments, v.id)
		v.driver.mu.Unlock()
	}

	_, err := v.driver.client.DeleteVolume(ctx, v.driver.appName, v.id)
	if err != nil {
		return fmt.Errorf("failed to delete volume %s: %w", v.id, err)
	}

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
