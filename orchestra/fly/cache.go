package fly

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	fly "github.com/superfly/fly-go"
	"github.com/superfly/fly-go/flaps"
	"golang.org/x/crypto/ssh"

	"github.com/jtarchie/pocketci/cache"
)

const cacheHelperImage = "busybox:latest"

// findVolumeByName looks up a tracked Fly volume by its user-facing name.
func (f *Fly) findVolumeByName(volumeName string) *Volume {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, v := range f.volumes {
		if v.userFacingName == volumeName {
			return v
		}
	}

	return nil
}

// findVolumeByID looks up a tracked Fly volume by its physical volume ID.
func (f *Fly) findVolumeByID(volumeID string) (*Volume, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, v := range f.volumes {
		if v.id == volumeID {
			return v, true
		}
	}

	return nil, false
}

// launchHelperMachine returns a running busybox machine with the given volume
// attached. On the first call for a volume it creates a new machine; on
// subsequent calls it resumes the suspended machine from its memory snapshot,
// which is much faster than a cold boot.
// The caller must call destroyHelperMachine (which suspends, not destroys) when done.
func (f *Fly) launchHelperMachine(ctx context.Context, vol *Volume) (*fly.Machine, error) {
	f.mu.Lock()
	existingID := f.helperMachines[vol.id]
	f.mu.Unlock()

	// If we have a persistent helper for this volume, try to resume it.
	if existingID != "" {
		if machine := f.tryResumeHelperMachine(ctx, existingID, vol); machine != nil {
			return machine, nil
		}
	}

	// No existing helper (or resume failed) – create a fresh machine.

	// If the volume is currently attached to another (non-helper) machine, detach it first.
	f.mu.Lock()
	oldMachineID, attached := f.volumeAttachments[vol.id]
	f.mu.Unlock()

	if attached {
		f.logger.Debug("fly.cache.detach", "volume", vol.id, "oldMachine", oldMachineID)

		err := f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   oldMachineID,
			Kill: true,
		}, "")
		if err != nil {
			f.logger.Warn("fly.cache.detach.error", "volume", vol.id, "machine", oldMachineID, "err", err)
		}
	}

	guest := &fly.MachineGuest{
		CPUKind:  "shared",
		CPUs:     1,
		MemoryMB: 256,
	}

	config := &fly.MachineConfig{
		Image: cacheHelperImage,
		Init: fly.MachineInit{
			Exec: []string{"sleep", "infinity"},
		},
		Guest:       guest,
		AutoDestroy: false,
		Restart: &fly.MachineRestart{
			Policy: fly.MachineRestartPolicyNo,
		},
		Metadata: map[string]string{
			"orchestra.namespace": f.namespace,
			"orchestra.purpose":   "cache-helper",
		},
		Mounts: []fly.MachineMount{
			{
				Volume: vol.id,
				Path:   "/volume",
			},
		},
	}

	input := fly.LaunchMachineInput{
		Config: config,
		Region: f.region,
	}

	f.logger.Debug("fly.cache.helper.launch", "volume", vol.name, "image", cacheHelperImage)

	machine, err := f.client.Launch(ctx, f.appName, input)
	if err != nil {
		return nil, fmt.Errorf("failed to launch cache helper machine: %w", err)
	}

	// Record volume attachment and helper machine
	f.mu.Lock()
	f.volumeAttachments[vol.id] = machine.ID
	f.helperMachines[vol.id] = machine.ID
	f.mu.Unlock()

	// Wait for the machine to start
	err = f.client.Wait(ctx, f.appName, machine.ID, flaps.WithWaitStates("started"), flaps.WithWaitTimeout(2*time.Minute))
	if err != nil {
		// Try to clean up the machine
		_ = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   machine.ID,
			Kill: true,
		}, "")

		return nil, fmt.Errorf("cache helper machine failed to start: %w", err)
	}

	// Refresh machine state to get PrivateIP
	machine, err = f.client.Get(ctx, f.appName, machine.ID)
	if err != nil {
		_ = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   machine.ID,
			Kill: true,
		}, "")

		return nil, fmt.Errorf("failed to get cache helper machine state: %w", err)
	}

	if machine.PrivateIP == "" {
		_ = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   machine.ID,
			Kill: true,
		}, "")

		return nil, errors.New("cache helper machine has no private IP")
	}

	f.logger.Debug("fly.cache.helper.started", "machine", machine.ID, "ip", machine.PrivateIP)

	return machine, nil
}

// tryResumeHelperMachine attempts to resume an existing suspended helper machine.
// Returns the machine on success, or nil if resume failed (cleanup is performed automatically).
func (f *Fly) tryResumeHelperMachine(ctx context.Context, existingID string, vol *Volume) *fly.Machine {
	f.logger.Debug("fly.cache.helper.resume", "volume", vol.name, "machine", existingID)

	cleanupHelper := func() {
		_ = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{ID: existingID, Kill: true}, "")

		f.mu.Lock()
		delete(f.helperMachines, vol.id)
		delete(f.volumeAttachments, vol.id)
		f.mu.Unlock()
	}

	_, startErr := f.client.Start(ctx, f.appName, existingID, "")
	if startErr != nil {
		f.logger.Warn("fly.cache.helper.resume.failed", "machine", existingID, "err", startErr)
		cleanupHelper()

		return nil
	}

	waitErr := f.client.Wait(ctx, f.appName, existingID, flaps.WithWaitStates("started"), flaps.WithWaitTimeout(2*time.Minute))
	if waitErr != nil {
		f.logger.Warn("fly.cache.helper.resume.timeout", "machine", existingID, "err", waitErr)
		cleanupHelper()

		return nil
	}

	machine, getErr := f.client.Get(ctx, f.appName, existingID)
	if getErr != nil || machine.PrivateIP == "" {
		cleanupHelper()

		return nil
	}

	f.logger.Debug("fly.cache.helper.resumed", "machine", machine.ID, "ip", machine.PrivateIP)

	return machine
}

// destroyHelperMachine suspends the helper machine so it can be quickly resumed
// on the next use. The volume stays attached and the machine's memory is
// snapshotted. On Close() the driver will truly destroy it.
func (f *Fly) destroyHelperMachine(ctx context.Context, machineID string) {
	f.logger.Debug("fly.cache.helper.suspend", "machine", machineID)

	err := f.client.Suspend(ctx, f.appName, machineID, "")
	if err != nil {
		f.logger.Warn("fly.cache.helper.suspend.failed", "machine", machineID, "err", err)
		// Fall back to a hard destroy so we don't leak the machine.
		f.mu.Lock()
		for volumeID, id := range f.helperMachines {
			if id == machineID {
				delete(f.helperMachines, volumeID)
				delete(f.volumeAttachments, volumeID)
				break
			}
		}
		f.mu.Unlock()

		_ = f.client.Kill(ctx, f.appName, machineID)

		machine := &fly.Machine{ID: machineID}
		_ = f.client.Wait(ctx, f.appName, machine.ID, flaps.WithWaitStates("stopped"), flaps.WithWaitTimeout(30*time.Second))

		_ = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{ID: machineID, Kill: true}, "")

		return
	}

	// Wait for the machine to reach a fully suspended state.
	machine := &fly.Machine{ID: machineID}
	err = f.client.Wait(ctx, f.appName, machine.ID, flaps.WithWaitStates("suspended"), flaps.WithWaitTimeout(30*time.Second))
	if err != nil {
		f.logger.Warn("fly.cache.helper.suspend.wait", "machine", machineID, "err", err)
	}

	f.logger.Debug("fly.cache.helper.suspended", "machine", machineID)
}

// CopyToVolume implements cache.VolumeDataAccessor.
// It launches a busybox helper machine with the volume mounted, establishes a
// WireGuard tunnel + SSH connection, then walks the tar stream on the client
// side and uploads each entry to /volume via SFTP. This requires no tar binary
// on the remote machine.
func (f *Fly) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	vol := f.findVolumeByName(volumeName)
	if vol == nil {
		return fmt.Errorf("volume %q not found", volumeName)
	}

	// Launch helper machine with the volume
	machine, err := f.launchHelperMachine(ctx, vol)
	if err != nil {
		return fmt.Errorf("failed to launch helper for CopyToVolume: %w", err)
	}
	defer f.destroyHelperMachine(ctx, machine.ID)

	// Establish WireGuard tunnel
	tunnel, err := f.createTunnel(ctx)
	if err != nil {
		return fmt.Errorf("failed to create WireGuard tunnel: %w", err)
	}
	defer tunnel.close(ctx, f.apiClient)

	// Connect via SSH through the tunnel
	sshClient, err := f.dialSSH(ctx, tunnel, machine.PrivateIP)
	if err != nil {
		return fmt.Errorf("failed to SSH to helper machine: %w", err)
	}
	defer func() { _ = sshClient.Close() }()

	// Open SFTP subsystem over the existing SSH connection.
	sftpClient, err := sftp.NewClient(sshClient,
		sftp.UseConcurrentReads(true),
		sftp.UseConcurrentWrites(true),
	)
	if err != nil {
		return fmt.Errorf("failed to open SFTP subsystem: %w", err)
	}
	defer func() { _ = sftpClient.Close() }()

	f.logger.Debug("fly.cache.copytov.start", "volume", volumeName)

	err = uploadTarEntries(sftpClient, reader)
	if err != nil {
		return err
	}

	f.logger.Info("fly.cache.copytov.done", "volume", volumeName)

	// Flush the filesystem to disk before the SSH connection closes.
	// The helper machine will be suspended (not shut down), so the kernel
	// won't automatically flush page cache. The k6 (or any other) container
	// that mounts the same volume afterward sees the block device state, so
	// we must ensure all SFTP-written data is committed to disk first.
	syncSession, syncErr := sshClient.NewSession()
	if syncErr == nil {
		_ = syncSession.Run("sync")
		_ = syncSession.Close()
	}

	return nil
}

// uploadTarEntries walks a tar stream and uploads each entry to /volume via SFTP.
func uploadTarEntries(sftpClient *sftp.Client, reader io.Reader) error {
	tr := tar.NewReader(reader)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		remotePath := path.Join("/volume", hdr.Name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			mkErr := sftpClient.MkdirAll(remotePath)
			if mkErr != nil {
				return fmt.Errorf("failed to create remote directory %q: %w", remotePath, mkErr)
			}

		case tar.TypeReg:
			mkErr := sftpClient.MkdirAll(path.Dir(remotePath))
			if mkErr != nil {
				return fmt.Errorf("failed to create parent dir for %q: %w", remotePath, mkErr)
			}

			rf, err := sftpClient.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
			if err != nil {
				return fmt.Errorf("failed to open remote file %q: %w", remotePath, err)
			}

			_, cpErr := io.Copy(rf, tr)
			if cpErr != nil {
				_ = rf.Close()

				return fmt.Errorf("failed to write remote file %q: %w", remotePath, cpErr)
			}

			closeErr := rf.Close()
			if closeErr != nil {
				return fmt.Errorf("failed to close remote file %q: %w", remotePath, closeErr)
			}
		}
	}
}

// CopyFromVolume implements cache.VolumeDataAccessor.
// It launches a busybox helper machine with the volume mounted, establishes a
// WireGuard tunnel + SSH connection, and streams a tar archive of /volume contents.
func (f *Fly) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	vol := f.findVolumeByName(volumeName)
	if vol == nil {
		return nil, fmt.Errorf("volume %q not found", volumeName)
	}

	// Launch helper machine with the volume
	machine, err := f.launchHelperMachine(ctx, vol)
	if err != nil {
		return nil, fmt.Errorf("failed to launch helper for CopyFromVolume: %w", err)
	}

	// Establish WireGuard tunnel
	tunnel, err := f.createTunnel(ctx)
	if err != nil {
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to create WireGuard tunnel: %w", err)
	}

	// Connect via SSH through the tunnel
	sshClient, err := f.dialSSH(ctx, tunnel, machine.PrivateIP)
	if err != nil {
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to SSH to helper machine: %w", err)
	}

	// Open SSH session and stream tar from /volume
	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	f.logger.Debug("fly.cache.copyfromv.start", "volume", volumeName)

	err = session.Start("tar cf - -C /volume .")
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to start tar: %w", err)
	}

	// Return a ReadCloser that cleans up all resources when closed
	return &cacheReader{
		ReadCloser: io.NopCloser(stdout),
		session:    session,
		sshClient:  sshClient,
		tunnel:     tunnel,
		machineID:  machine.ID,
		driver:     f,
	}, nil
}

// cacheReader wraps the SSH stdout stream and cleans up all resources on Close.
type cacheReader struct {
	io.ReadCloser
	session   *ssh.Session
	sshClient *ssh.Client
	tunnel    *flyTunnel
	machineID string
	driver    *Fly
}

func (r *cacheReader) Close() error {
	ctx := context.Background()

	// Wait for the tar command to finish
	_ = r.session.Wait()
	_ = r.session.Close()

	_ = r.sshClient.Close()
	r.tunnel.close(ctx, r.driver.apiClient)
	r.driver.destroyHelperMachine(ctx, r.machineID)

	r.driver.logger.Info("fly.cache.copyfromv.done")

	return nil
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor.
// Uses the same SSH tunnel approach as CopyFromVolume but tars only specific paths.
func (f *Fly) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	vol := f.findVolumeByName(volumeName)
	if vol == nil {
		return nil, fmt.Errorf("volume %q not found", volumeName)
	}

	machine, err := f.launchHelperMachine(ctx, vol)
	if err != nil {
		return nil, fmt.Errorf("failed to launch helper for ReadFilesFromVolume: %w", err)
	}

	tunnel, err := f.createTunnel(ctx)
	if err != nil {
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to create WireGuard tunnel: %w", err)
	}

	sshClient, err := f.dialSSH(ctx, tunnel, machine.PrivateIP)
	if err != nil {
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to SSH to helper machine: %w", err)
	}

	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Build: tar cf - -C /volume/<subdir> path1 path2 ...
	// The shared physical volume is mounted at /volume; each logical volume
	// is a subdirectory named after the volume's userFacingName.
	// Use shell quoting to handle paths safely.
	quotedPaths := make([]string, len(filePaths))
	for i, fp := range filePaths {
		quotedPaths[i] = "'" + strings.ReplaceAll(fp, "'", "'\\''") + "'"
	}

	baseDir := path.Join("/volume", vol.userFacingName)
	cmd := "tar cf - -C " + baseDir + " " + strings.Join(quotedPaths, " ")

	f.logger.Debug("fly.cache.readfiles.start", "volume", volumeName, "paths", filePaths)

	err = session.Start(cmd)
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		tunnel.close(ctx, f.apiClient)
		f.destroyHelperMachine(ctx, machine.ID)
		return nil, fmt.Errorf("failed to start tar: %w", err)
	}

	return &cacheReader{
		ReadCloser: io.NopCloser(stdout),
		session:    session,
		sshClient:  sshClient,
		tunnel:     tunnel,
		machineID:  machine.ID,
		driver:     f,
	}, nil
}

var _ cache.VolumeDataAccessor = (*Fly)(nil)
