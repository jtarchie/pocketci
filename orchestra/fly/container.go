package fly

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path"
	"strings"
	"sync"
	"time"

	fly "github.com/superfly/fly-go"
	"github.com/superfly/fly-go/flaps"

	"github.com/jtarchie/pocketci/orchestra"
)

// Fly's flaps events feed is eventually consistent: client.Wait(states=stopped)
// returns as soon as the state flips, but the exit event may not be appended
// to machine.Events yet. Without retry, a single Get right after Wait can
// observe a stopped machine with no exit event and produce a spurious -1.
// These vars bound how long resolveFinalExitCode waits for the event. They
// are var (not const) so tests can shrink the backoff budget.
var (
	finalStateRetryAttempts = 5
	finalStateRetryInitial  = 500 * time.Millisecond
	finalStateRetryMax      = 8 * time.Second
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

	// Cancel signal for the background waitForStop goroutine. Set before
	// the goroutine is launched; invoked from Cleanup() so the goroutine
	// exits promptly even when Fly's Wait endpoint is hanging.
	waitCancel context.CancelFunc

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

// pollUntilStopped loops the Fly Wait endpoint until the machine reaches a
// terminal state. The Fly proxy caps the Wait timeout at 60 s regardless of
// what we request, so long-running tasks require multiple poll iterations.
func (c *Container) pollUntilStopped(ctx context.Context) {
	for {
		err := c.driver.client.Wait(ctx, c.driver.appName, c.machineID, flaps.WithWaitStates("stopped"), flaps.WithWaitTimeout(60*time.Second))
		if err == nil {
			return
		}

		c.driver.logger.Debug("fly.machine.wait.polling", "machine", c.machineID, "err", err)

		// Check whether the machine is already in a terminal state or still running.
		m, getErr := c.driver.client.Get(ctx, c.driver.appName, c.machineID)
		if getErr != nil {
			c.driver.logger.Warn("fly.machine.get.polling.error", "machine", c.machineID, "err", getErr)
			return
		}

		switch m.State {
		case "stopped", "destroyed", "error":
			return
		}
		// Machine still running ("started", "starting", etc.) — loop.
	}
}

// waitForStop blocks until the machine reaches the "stopped" state, then
// caches the exit code. Called as a goroutine after launch.
//
// The context is owned by this goroutine and cancelled from Cleanup() so
// a hung Fly Wait endpoint can't pin the goroutine for the lifetime of
// the process.
func (c *Container) waitForStop(ctx context.Context) {
	c.pollUntilStopped(ctx)

	exitCode := resolveFinalExitCode(ctx, c.driver.logger, c.machineID, func(gctx context.Context) (*fly.Machine, error) {
		return c.driver.client.Get(gctx, c.driver.appName, c.machineID)
	})

	c.mu.Lock()
	c.done = true
	c.exitCode = exitCode
	c.mu.Unlock()

	c.driver.logger.Debug("fly.machine.stopped", "machine", c.machineID, "exitCode", exitCode)
}

// resolveFinalExitCode fetches a stopped machine and returns its exit code,
// retrying when the events feed hasn't yet recorded the "exit" event.
//
// Fly's flaps API is eventually consistent, so "stopped machine with no exit
// event yet" is distinct from "stopped machine that will never have an exit
// event" (OOM, resource kill). We give the events feed a bounded window to
// catch up before falling back to the -1 "forced kill" sentinel.
//
// getMachine is the fetch callback so the retry loop is unit-testable without
// a real flaps client. It is invoked at least once (for the happy path) and
// at most finalStateRetryAttempts times.
func resolveFinalExitCode(ctx context.Context, logger *slog.Logger, machineID string, getMachine func(context.Context) (*fly.Machine, error)) int {
	var (
		lastMachine *fly.Machine
		lastErr     error
	)

	delay := finalStateRetryInitial

	for attempt := 1; attempt <= finalStateRetryAttempts; attempt++ {
		machine, err := getMachine(ctx)
		if err == nil {
			lastMachine = machine
			lastErr = nil

			if code, ok := exitCodeFromEvents(machine); ok {
				return code
			}
		} else {
			lastErr = err

			logger.Debug("fly.machine.get.final.retry",
				"machine", machineID,
				"attempt", attempt,
				"max", finalStateRetryAttempts,
				"err", err,
			)
		}

		if attempt == finalStateRetryAttempts {
			break
		}

		select {
		case <-ctx.Done():
			logger.Warn("fly.machine.exit.resolve.cancelled",
				"machine", machineID,
				"attempt", attempt,
				"err", ctx.Err(),
			)

			return -1
		case <-time.After(delay):
		}

		delay *= 2
		if delay > finalStateRetryMax {
			delay = finalStateRetryMax
		}
	}

	// Exhausted retries without finding an exit event. Distinguish the two
	// degraded cases so operators can tell an API outage from a real
	// forced-kill scenario.
	if lastErr != nil {
		logger.Warn("fly.machine.get.final.error",
			"machine", machineID,
			"attempts", finalStateRetryAttempts,
			"err", lastErr,
		)
	} else {
		logger.Warn("fly.machine.exit.missing",
			"machine", machineID,
			"attempts", finalStateRetryAttempts,
			"state", machineState(lastMachine),
		)
	}

	return -1
}

// exitCodeFromEvents returns the most recent exit event's code and true if
// one is present, or (0, false) if the events feed has no exit event yet.
func exitCodeFromEvents(m *fly.Machine) (int, bool) {
	if m == nil {
		return 0, false
	}

	for i := len(m.Events) - 1; i >= 0; i-- {
		event := m.Events[i]
		if event.Type == "exit" && event.Request != nil && event.Request.ExitEvent != nil {
			return event.Request.ExitEvent.ExitCode, true
		}
	}

	return 0, false
}

func machineState(m *fly.Machine) string {
	if m == nil {
		return ""
	}

	return m.State
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

	// Signal the background waitForStop goroutine to exit. Even if the
	// Destroy below succeeds, Fly's Wait may take seconds to notice; the
	// cancel short-circuits it.
	if c.waitCancel != nil {
		c.waitCancel()
	}

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
		_ = c.driver.client.Wait(ctx, c.driver.appName, machine.ID, flaps.WithWaitStates("stopped"), flaps.WithWaitTimeout(30*time.Second))
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
		volume, err := f.CreateVolume(ctx, taskMount.Name, taskMount.SizeGB)
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

	applyGuestLimits(guest, logger, task.ContainerLimits)

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

		logger.Error("fly.machine.launch.error", slog.String("name", machineName), slog.String("error", err.Error()))

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
	// Ctx is decoupled from the parent: a client disconnect must not
	// interrupt the goroutine. Cleanup() cancels it explicitly.
	waitCtx, waitCancel := context.WithCancel(context.Background())
	container.waitCancel = waitCancel

	go container.waitForStop(waitCtx) //nolint:contextcheck // deliberate: background goroutine outlives parent context

	return container, nil
}

// recoverExistingMachine attempts to find and reuse an existing machine by name
// when a Launch call fails (idempotency). Returns the container or an error if
// no matching machine was found.
func (f *Fly) recoverExistingMachine(ctx context.Context, machineName, sharedVolumeID string, logger *slog.Logger) (*Container, error) {
	list, listErr := f.client.List(ctx, f.appName, "")
	if listErr != nil {
		return nil, fmt.Errorf("list machines: %w", listErr)
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
			// Use the retrying resolver even when we already have a Machine:
			// the events feed on the snapshot we received may not yet have
			// the exit event populated (same eventual-consistency window that
			// affects waitForStop immediately after Wait returns).
			container.exitCode = resolveFinalExitCode(ctx, logger, m.ID, func(gctx context.Context) (*fly.Machine, error) {
				return f.client.Get(gctx, f.appName, m.ID)
			})
			container.done = true
		} else {
			waitCtx, waitCancel := context.WithCancel(context.Background())
			container.waitCancel = waitCancel

			go container.waitForStop(waitCtx) //nolint:contextcheck // deliberate: background goroutine outlives parent context
		}

		return container, nil
	}

	return nil, fmt.Errorf("no existing machine named %s", machineName)
}

// buildInitExec constructs the init exec command for a Fly machine,
// handling workdir and workspace volume mount mappings.
//
// workDir may be absolute or relative; relative paths are resolved under
// /workspace (e.g. "repo" → "/workspace/repo").  Both workDir and mappings
// applyGuestLimits overrides a MachineGuest's cpu_kind, CPU count, and memory
// with per-task limits from the pipeline YAML.  It must be called after
// guest.SetSize() so the driver-level preset is already applied as the base.
//
// When the requested memory is not a multiple of Fly's CPU-kind increment,
// it is rounded up. When memory exceeds the per-size ceiling for shared
// CPUs, the CPU count is auto-upgraded. Both adjustments are logged at Info
// level so users see why they're billed for more than they asked for.
func applyGuestLimits(guest *fly.MachineGuest, logger *slog.Logger, limits orchestra.ContainerLimits) {
	if limits.CPUKind != "" {
		guest.CPUKind = limits.CPUKind
	}

	if limits.CPU > 0 {
		guest.CPUs = int(limits.CPU)
	}

	if limits.Memory > 0 {
		guest.MemoryMB = roundMemory(guest.CPUKind, int(limits.Memory/(1024*1024)), logger)
		upgradeSharedCPUs(guest, logger)
	}
}

// roundMemory rounds requestedMB up to the nearest Fly memory step
// (256 MB for shared, 1024 MB for performance CPUs).
func roundMemory(cpuKind string, requestedMB int, logger *slog.Logger) int {
	memStep := 256
	if cpuKind == "performance" {
		memStep = 1024
	}

	if requestedMB%memStep == 0 {
		return requestedMB
	}

	rounded := ((requestedMB / memStep) + 1) * memStep

	if logger != nil {
		logger.Info("fly.guest.memory.rounded",
			"requested_mb", requestedMB,
			"rounded_mb", rounded,
			"step_mb", memStep,
			"cpu_kind", cpuKind,
		)
	}

	return rounded
}

// upgradeSharedCPUs auto-upgrades the CPU count so the requested memory
// stays within the shared-CPU per-size ceiling:
//
//	shared-cpu-1x: max 2048 MB (1 CPU)
//	shared-cpu-2x: max 4096 MB (2 CPUs)
//	shared-cpu-4x: max 8192 MB (4 CPUs)
//
// Only applies to shared CPUs; performance machines accept any memory.
func upgradeSharedCPUs(guest *fly.MachineGuest, logger *slog.Logger) {
	if guest.CPUKind != "shared" {
		return
	}

	originalCPUs := guest.CPUs

	switch {
	case guest.MemoryMB > 4096 && guest.CPUs < 4:
		guest.CPUs = 4
	case guest.MemoryMB > 2048 && guest.CPUs < 2:
		guest.CPUs = 2
	}

	if logger == nil || guest.CPUs == originalCPUs {
		return
	}

	logger.Info("fly.guest.cpu.upgraded",
		"from_cpus", originalCPUs,
		"to_cpus", guest.CPUs,
		"memory_mb", guest.MemoryMB,
		"reason", "shared-CPU size ceiling",
	)
}

// may be set at the same time — the mount symlinks are always created first.
func buildInitExec(command []string, workDir string, mappings []mountMapping) []string {
	// Resolve the final working directory.
	// Empty workDir defaults to /workspace when there are mounts; otherwise
	// the command inherits the process cwd as-is.
	finalCD := ""
	if workDir != "" {
		if path.IsAbs(workDir) {
			finalCD = workDir
		} else {
			finalCD = "/workspace/" + workDir
		}
	} else if len(mappings) > 0 {
		finalCD = "/workspace"
	}

	if len(mappings) > 0 {
		initParts := mountSetupCommands(mappings)

		return []string{"/bin/sh", "-c",
			strings.Join(initParts, " && ") +
				" && cd " + shellescape(finalCD) + " && exec " + shelljoin(command),
		}
	}

	if finalCD != "" {
		return []string{"/bin/sh", "-c", "cd " + shellescape(finalCD) + " && exec " + shelljoin(command)}
	}

	return command
}

// mountSetupCommands returns the shell commands that create each volume
// subdirectory under /workspace and symlink the mount path to it.
//
// Absolute mount paths (e.g. /root/.deno, /go/pkg/mod) are symlinked at
// their real container path. Relative paths live under /workspace. Both
// init-exec (task mode) and setupWorkspaceMounts (sandbox mode) use this
// so the two code paths can't drift.
func mountSetupCommands(mappings []mountMapping) []string {
	var parts []string

	for _, m := range mappings {
		parts = append(parts, "mkdir -p /workspace/"+m.volumeName)

		if m.mountPath == m.volumeName {
			continue
		}

		symlinkTarget := "/workspace/" + m.mountPath
		if path.IsAbs(m.mountPath) {
			symlinkTarget = m.mountPath
		}

		parentDir := path.Dir(symlinkTarget)
		parts = append(parts, "mkdir -p "+parentDir)
		parts = append(parts, "ln -sfn /workspace/"+m.volumeName+" "+symlinkTarget)
	}

	return parts
}

// extractExitCode scans machine events in reverse to find the exit code.
// Returns -1 when no exit event is found, indicating a forced kill (OOM,
// resource limit, etc.) rather than a clean process exit.
//
// This is a snapshot helper — it does not retry. Callers that have just
// observed a state transition to "stopped" should use resolveFinalExitCode
// instead so Fly's eventually-consistent events feed has time to catch up.
func (f *Fly) extractExitCode(m *fly.Machine) int {
	if code, ok := exitCodeFromEvents(m); ok {
		return code
	}

	return -1
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

	cleanupErr := vol.Cleanup(ctx)
	if cleanupErr != nil {
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
