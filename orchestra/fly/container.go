package fly

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	fly "github.com/superfly/fly-go"

	"github.com/jtarchie/pocketci/orchestra"
)

// mountMapping tracks the relationship between a volume subdirectory and its mount path.
type mountMapping struct {
	volumeName string // subdirectory on the shared volume (volume's userFacingName)
	mountPath  string // path the task script sees (mount key)
}

type Container struct {
	machineID  string
	instanceID string
	driver     *Fly

	// Cached final state, set when the machine finishes
	mu       sync.Mutex
	done     bool
	exitCode int
}

// ID returns the Fly Machine ID.
func (c *Container) ID() string {
	return c.machineID
}

type containerStatus struct {
	done     bool
	exitCode int
}

func (s *containerStatus) IsDone() bool {
	return s.done
}

func (s *containerStatus) ExitCode() int {
	return s.exitCode
}

// waitForStop blocks until the machine reaches the "stopped" state, then
// caches the exit code. Called as a goroutine after launch.
func (c *Container) waitForStop() {
	ctx := context.Background()

	// Use the Fly Wait endpoint which long-polls instead of repeated GETs.
	// This is much more efficient and avoids rate limiting.
	machine := &fly.Machine{ID: c.machineID, InstanceID: c.instanceID}

	err := c.driver.client.Wait(ctx, c.driver.appName, machine, "stopped", 5*time.Minute)
	if err != nil {
		c.driver.logger.Warn("fly.machine.wait.error", "machine", c.machineID, "err", err)
	}

	// Fetch final state to get exit code
	finalMachine, err := c.driver.client.Get(ctx, c.driver.appName, c.machineID)
	if err != nil {
		c.driver.logger.Warn("fly.machine.get.final.error", "machine", c.machineID, "err", err)

		c.mu.Lock()
		c.done = true
		c.exitCode = -1
		c.mu.Unlock()

		return
	}

	exitCode := 0

	for i := len(finalMachine.Events) - 1; i >= 0; i-- {
		event := finalMachine.Events[i]
		if event.Type == "exit" && event.Request != nil && event.Request.ExitEvent != nil {
			exitCode = event.Request.ExitEvent.ExitCode
			break
		}
	}

	c.mu.Lock()
	c.done = true
	c.exitCode = exitCode
	c.mu.Unlock()

	c.driver.logger.Debug("fly.machine.stopped", "machine", c.machineID, "exitCode", exitCode)
}

func (c *Container) Status(_ context.Context) (orchestra.ContainerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return &containerStatus{
		done:     c.done,
		exitCode: c.exitCode,
	}, nil
}

// Logs retrieves machine stdout/stderr from the Fly log API.
// The API streams logs via NATS and is filtered by machine instance ID.
// When follow is true, polls until the machine exits and the context is cancelled.
// When follow is false, fetches all currently available logs.
func (c *Container) Logs(ctx context.Context, stdout, stderr io.Writer, follow bool) error {
	nextToken := ""

	for {
		entries, token, err := c.fetchLogs(ctx, nextToken)
		if err != nil {
			return fmt.Errorf("failed to fetch logs: %w", err)
		}

		writeLogEntries(entries, stdout, stderr)

		// Only advance the token when the API returned a non-empty one.
		// An empty token means "no new logs yet" — preserving the last valid
		// token prevents re-fetching the entire log history on the next poll.
		if token != "" {
			nextToken = token
		}

		if !follow {
			return nil
		}

		// Check if machine is done
		c.mu.Lock()
		done := c.done
		c.mu.Unlock()

		if done {
			// Drain any remaining logs that arrived after the last poll.
			c.drainLogs(ctx, nextToken, stdout, stderr)

			return nil
		}

		select {
		case <-ctx.Done():
			// Context was cancelled — attempt one final drain in case the
			// machine finished and there are unseen log entries.  Use a
			// short-lived background context so the API call still succeeds.
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer drainCancel()

			c.drainLogs(drainCtx, nextToken, stdout, stderr) //nolint:contextcheck // deliberate: drain after parent ctx cancelled

			return nil
		case <-time.After(500 * time.Millisecond):
			continue
		}
	}
}

// writeLogEntries writes log entries to the appropriate writer based on level.
// Only app-level logs are included.
func writeLogEntries(entries []logEntry, stdout, stderr io.Writer) {
	for _, entry := range entries {
		writer := stdout
		if entry.Level == "error" || entry.Level == "warning" {
			writer = stderr
		}

		if entry.Provider == "app" {
			_, _ = fmt.Fprintln(writer, entry.Message)
		}
	}
}

// drainLogs fetches and writes all remaining log entries starting from nextToken.
func (c *Container) drainLogs(ctx context.Context, nextToken string, stdout, stderr io.Writer) {
	for {
		entries, token, err := c.fetchLogs(ctx, nextToken)
		if err != nil || len(entries) == 0 {
			break
		}

		writeLogEntries(entries, stdout, stderr)

		if token != "" {
			nextToken = token
		} else {
			break
		}
	}
}

type logEntry struct {
	Message  string
	Level    string
	Provider string
}

func (c *Container) fetchLogs(ctx context.Context, nextToken string) ([]logEntry, string, error) {
	sdkEntries, token, err := c.driver.apiClient.GetAppLogs(ctx, c.driver.appName, nextToken, "", c.machineID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch logs: %w", err)
	}

	entries := make([]logEntry, 0, len(sdkEntries))
	for _, e := range sdkEntries {
		entries = append(entries, logEntry{
			Message:  e.Message,
			Level:    e.Level,
			Provider: e.Meta.Event.Provider,
		})
	}

	return entries, token, nil
}

func (c *Container) Cleanup(ctx context.Context) error {
	c.driver.logger.Debug("fly.machine.cleanup", "machine", c.machineID)

	// Stop the machine first if it's running
	machine, err := c.driver.client.Get(ctx, c.driver.appName, c.machineID)
	if err != nil {
		// Machine may already be gone
		return nil
	}

	if machine.State == "started" || machine.State == "starting" {
		err = c.driver.client.Kill(ctx, c.driver.appName, c.machineID)
		if err != nil {
			c.driver.logger.Warn("fly.machine.kill.error", "machine", c.machineID, "err", err)
		}

		// Wait briefly for stop
		_ = c.driver.client.Wait(ctx, c.driver.appName, machine, "stopped", 30*time.Second)
	}

	err = c.driver.client.Destroy(ctx, c.driver.appName, fly.RemoveMachineInput{
		ID:   c.machineID,
		Kill: true,
	}, "")
	if err != nil {
		return fmt.Errorf("failed to destroy machine %s: %w", c.machineID, err)
	}

	return nil
}

func (f *Fly) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	logger := f.logger.With("taskID", task.ID)

	machineName := SanitizeAppName(fmt.Sprintf("%s-%s", f.namespace, task.ID))

	// Build environment variables
	env := make(map[string]string)
	maps.Copy(env, task.Env)

	// Build machine mounts — all logical mounts share a single physical volume
	// mounted at /workspace, with each mount as a subdirectory.
	var mounts []fly.MachineMount

	var mountMappings []mountMapping

	var sharedVolumeID string

	for _, taskMount := range task.Mounts {
		volume, err := f.CreateVolume(ctx, taskMount.Name, 1)
		if err != nil {
			logger.Error("fly.volume.create.error", "name", taskMount.Name, "err", err)
			return nil, fmt.Errorf("failed to create volume: %w", err)
		}

		flyVolume, _ := volume.(*Volume)
		sharedVolumeID = flyVolume.id
		mountMappings = append(mountMappings, mountMapping{
			volumeName: flyVolume.userFacingName,
			mountPath:  taskMount.Path,
		})
	}

	if sharedVolumeID != "" {
		// If this volume is currently attached to another machine, destroy
		// that machine first so the volume can be reattached.
		f.mu.Lock()
		oldMachineID, attached := f.volumeAttachments[sharedVolumeID]
		f.mu.Unlock()

		if attached {
			logger.Debug("fly.volume.detach", "volume", sharedVolumeID, "oldMachine", oldMachineID)

			err := f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
				ID:   oldMachineID,
				Kill: true,
			}, "")
			if err != nil {
				logger.Warn("fly.volume.detach.error", "volume", sharedVolumeID, "machine", oldMachineID, "err", err)
			}
		}

		mounts = append(mounts, fly.MachineMount{
			Volume: sharedVolumeID,
			Path:   "/workspace",
		})
	}

	// Configure guest size
	guest := &fly.MachineGuest{}

	err := guest.SetSize(f.size)
	if err != nil {
		logger.Warn("fly.guest.size.fallback", "size", f.size, "err", err)
		// Fallback to manual config if preset not found
		guest.CPUKind = "shared"
		guest.CPUs = 1
		guest.MemoryMB = 256
	}

	// Override with task-specific limits if provided
	if task.ContainerLimits.CPU > 0 {
		guest.CPUs = int(task.ContainerLimits.CPU)
	}

	if task.ContainerLimits.Memory > 0 {
		guest.MemoryMB = int(task.ContainerLimits.Memory / (1024 * 1024)) // Convert bytes to MB
	}

	initExec := buildInitExec(task.Command, task.WorkDir, mountMappings)

	config := &fly.MachineConfig{
		Image: task.Image,
		Init: fly.MachineInit{
			Exec: initExec,
		},
		Env:         env,
		Guest:       guest,
		AutoDestroy: false,
		Restart: &fly.MachineRestart{
			Policy: fly.MachineRestartPolicyNo,
		},
		Metadata: map[string]string{
			"orchestra.namespace": f.namespace,
			"orchestra.task":      task.ID,
		},
		Mounts: mounts,
	}

	input := fly.LaunchMachineInput{
		Config: config,
		Region: f.region,
		Name:   machineName,
	}

	logger.Debug("fly.machine.launch", "name", machineName, "image", task.Image)

	machine, err := f.client.Launch(ctx, f.appName, input)
	if err != nil {
		container, recoverErr := f.recoverExistingMachine(ctx, machineName, sharedVolumeID, logger)
		if recoverErr == nil {
			return container, nil
		}

		// Clean up the shared volume so it doesn't get stranded when the
		// caller cannot use it (e.g. insufficient Fly resources).
		f.cleanupStrandedVolume(ctx, sharedVolumeID, logger)

		logger.Error("fly.machine.launch.error", "name", machineName, "err", err)

		return nil, fmt.Errorf("failed to launch fly machine: %w", err)
	}

	f.trackMachine(machine.ID)

	// Record volume→machine attachments for future detach
	if sharedVolumeID != "" {
		f.mu.Lock()
		f.volumeAttachments[sharedVolumeID] = machine.ID
		f.mu.Unlock()
	}

	logger.Info("fly.machine.launched", "machine", machine.ID, "name", machineName, "state", machine.State)

	container := &Container{
		machineID:  machine.ID,
		instanceID: machine.InstanceID,
		driver:     f,
	}

	// Start background goroutine to wait for the machine to stop.
	// This uses the Fly Wait endpoint (long-poll) instead of repeated GETs,
	// avoiding rate limiting and providing immediate status updates.
	go container.waitForStop() //nolint:contextcheck // deliberate: background goroutine outlives parent context

	return container, nil
}

// recoverExistingMachine attempts to find and reuse an existing machine by name
// when a Launch call fails (idempotency). Returns the container or an error if
// no matching machine was found.
func (f *Fly) recoverExistingMachine(ctx context.Context, machineName, sharedVolumeID string, logger *slog.Logger) (*Container, error) {
	list, listErr := f.client.List(ctx, f.appName, "")
	if listErr != nil {
		return nil, listErr
	}

	for _, m := range list {
		if m.Name != machineName {
			continue
		}

		logger.Info("fly.machine.existing", "machine", m.ID, "name", machineName, "state", m.State)
		f.trackMachine(m.ID)

		if sharedVolumeID != "" {
			f.mu.Lock()
			f.volumeAttachments[sharedVolumeID] = m.ID
			f.mu.Unlock()
		}

		container := &Container{
			machineID:  m.ID,
			instanceID: m.InstanceID,
			driver:     f,
		}

		if m.State == "stopped" || m.State == "destroyed" {
			container.exitCode = f.extractExitCode(m)
			container.done = true
		} else {
			go container.waitForStop() //nolint:contextcheck // deliberate: background goroutine outlives parent context
		}

		return container, nil
	}

	return nil, fmt.Errorf("no existing machine named %s", machineName)
}

// buildInitExec constructs the init exec command for a Fly machine,
// handling workdir and workspace volume mount mappings.
func buildInitExec(command []string, workDir string, mappings []mountMapping) []string {
	if workDir != "" {
		return []string{"/bin/sh", "-c", "cd " + shellescape(workDir) + " && exec " + shelljoin(command)}
	}

	if len(mappings) > 0 {
		var initParts []string
		for _, m := range mappings {
			initParts = append(initParts, "mkdir -p /workspace/"+m.volumeName)
			if m.mountPath != m.volumeName {
				initParts = append(initParts, "ln -sfn /workspace/"+m.volumeName+" /workspace/"+m.mountPath)
			}
		}

		return []string{"/bin/sh", "-c",
			strings.Join(initParts, " && ") +
				" && cd /workspace && exec " + shelljoin(command),
		}
	}

	return command
}

// extractExitCode scans machine events in reverse to find the exit code.
func (f *Fly) extractExitCode(m *fly.Machine) int {
	for i := len(m.Events) - 1; i >= 0; i-- {
		event := m.Events[i]
		if event.Type == "exit" && event.Request != nil && event.Request.ExitEvent != nil {
			return event.Request.ExitEvent.ExitCode
		}
	}

	return 0
}

// cleanupStrandedVolume removes a shared volume that can't be used because the
// machine launch failed and no existing machine could be recovered.
func (f *Fly) cleanupStrandedVolume(ctx context.Context, sharedVolumeID string, logger *slog.Logger) {
	if sharedVolumeID == "" {
		return
	}

	vol, ok := f.findVolumeByID(sharedVolumeID)
	if !ok {
		return
	}

	if cleanupErr := vol.Cleanup(ctx); cleanupErr != nil {
		logger.Warn("fly.machine.launch.volume.cleanup.error", "volume", sharedVolumeID, "err", cleanupErr)
	}
}

// GetContainer finds and returns an existing machine by its ID.
// Returns ErrContainerNotFound if the machine does not exist.
func (f *Fly) GetContainer(ctx context.Context, containerID string) (orchestra.Container, error) {
	m, err := f.client.Get(ctx, f.appName, containerID)
	if err != nil {
		return nil, orchestra.ErrContainerNotFound
	}

	return &Container{
		machineID:  m.ID,
		instanceID: m.InstanceID,
		driver:     f,
	}, nil
}
