package qemu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/go-qemu/qmp"
	"github.com/jtarchie/pocketci/orchestra"
)

// ServerConfig holds server-level configuration for the QEMU driver.
type ServerConfig struct {
	Memory   string `json:"memory,omitempty"`    // VM memory (e.g., "2048" or "2G"); default: "2048"
	CPUs     string `json:"cpus,omitempty"`      // VM CPU count; default: "2"
	Accel    string `json:"accel,omitempty"`     // Acceleration: hvf, kvm, tcg, or auto-detected
	Binary   string `json:"binary,omitempty"`    // Path to qemu-system binary (auto-detected by arch)
	CacheDir string `json:"cache_dir,omitempty"` // Directory for image cache (default: ~/.cache/pocketci/qemu)
	Image    string `json:"image,omitempty"`     // Boot image path or URL (optional; downloaded if empty)
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "qemu" }

// Config holds the full configuration for the QEMU driver.
type Config struct {
	ServerConfig
	Namespace string // Per-execution namespace identifier
}

// QEMU implements orchestra.Driver using a local QEMU virtual machine.
// Commands are executed inside the guest via the QEMU Guest Agent (QGA).
// Volumes are shared between host and guest via 9p virtfs.
type QEMU struct {
	cmd        *exec.Cmd
	monitor    *qmp.SocketMonitor
	qga        *QGAClient
	namespace  string
	logger     *slog.Logger
	tempDir    string
	volumesDir string

	bootOnce sync.Once
	bootErr  error

	mu         sync.Mutex
	containers map[string]*Container

	memory    string
	cpus      string
	accel     string
	qemuBin   string
	cacheDir  string
	imagePath string
}

// Name returns the driver name.
func (q *QEMU) Name() string {
	return "qemu"
}

// New creates a new QEMU driver.
func New(_ context.Context, cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	homeDir, _ := os.UserHomeDir()
	defaultCacheDir := filepath.Join(homeDir, ".cache", "pocketci", "qemu")

	defaultAccel := "tcg"
	switch runtime.GOOS {
	case "darwin":
		defaultAccel = "hvf"
	case "linux":
		if _, err := os.Stat("/dev/kvm"); err == nil {
			defaultAccel = "kvm"
		}
	}

	defaultBinary := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		defaultBinary = "qemu-system-aarch64"
	}

	memory := cfg.Memory
	if memory == "" {
		memory = "2048"
	}

	cpus := cfg.CPUs
	if cpus == "" {
		cpus = "2"
	}

	accel := cfg.Accel
	if accel == "" {
		accel = defaultAccel
	}

	binary := cfg.Binary
	if binary == "" {
		binary = defaultBinary
	}

	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = defaultCacheDir
	}

	q := &QEMU{
		namespace:  cfg.Namespace,
		logger:     logger,
		containers: make(map[string]*Container),
		memory:     memory,
		cpus:       cpus,
		accel:      accel,
		qemuBin:    binary,
		cacheDir:   cacheDir,
		imagePath:  cfg.Image,
	}

	return q, nil
}

// ensureVM lazily boots the QEMU VM on first use. Idempotent.
func (q *QEMU) ensureVM(ctx context.Context) error {
	q.bootOnce.Do(func() {
		q.bootErr = q.bootVM(ctx)
	})

	return q.bootErr
}

// bootVM performs the actual VM boot. Called once by ensureVM.
func (q *QEMU) bootVM(ctx context.Context) error {
	q.logger.Info("qemu.vm.starting", "namespace", q.namespace)

	// Create temp dir for runtime files
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("ci-qemu-%s-", q.namespace))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}

	q.tempDir = tempDir

	// Create volumes directory inside temp dir
	q.volumesDir = filepath.Join(tempDir, "volumes")
	if err := os.MkdirAll(q.volumesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create volumes dir: %w", err)
	}

	// Prepare the disk image
	baseImage := q.imagePath
	if baseImage == "" {
		q.logger.Info("qemu.image.downloading", "cache_dir", q.cacheDir)

		baseImage, err = downloadImage(q.cacheDir)
		if err != nil {
			return fmt.Errorf("failed to download image: %w", err)
		}

		q.logger.Info("qemu.image.ready", "path", baseImage)
	}

	// Create overlay (COW) so we never modify the base image
	overlayPath := filepath.Join(tempDir, "disk.qcow2")
	if err := createOverlay(baseImage, overlayPath); err != nil {
		return fmt.Errorf("failed to create overlay: %w", err)
	}

	// Create cloud-init seed ISO
	seedPath := filepath.Join(tempDir, "seed.iso")
	if err := createSeedISO(seedPath, q.namespace); err != nil {
		return fmt.Errorf("failed to create seed ISO: %w", err)
	}

	// Socket paths
	qmpSock := filepath.Join(tempDir, "qmp.sock")

	// Find a free TCP port for QGA
	qgaPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("failed to find free port for QGA: %w", err)
	}

	qgaAddr := fmt.Sprintf("127.0.0.1:%d", qgaPort)

	// Build QEMU command
	args := q.buildQEMUArgs(overlayPath, seedPath, qmpSock, qgaPort)

	q.logger.Info("qemu.vm.command", "binary", q.qemuBin, "args", strings.Join(args, " "))

	cmd := exec.Command(q.qemuBin, args...) //nolint:gosec
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	q.cmd = cmd

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start QEMU: %w", err)
	}

	q.logger.Info("qemu.vm.started", "pid", cmd.Process.Pid)

	// Connect QMP monitor
	if err := q.connectQMP(ctx, qmpSock); err != nil {
		return fmt.Errorf("failed to connect QMP: %w", err)
	}

	q.logger.Info("qemu.qmp.connected")

	// Wait for QGA to become available
	if err := q.connectQGA(ctx, qgaAddr); err != nil {
		return fmt.Errorf("failed to connect QGA: %w", err)
	}

	q.logger.Info("qemu.qga.connected")

	// Mount 9p volumes inside guest
	if err := q.mountVolumes(); err != nil {
		q.logger.Warn("qemu.volumes.mount.failed", "err", err)
		// Non-fatal — volumes may mount later on demand
	}

	return nil
}

// buildQEMUArgs constructs the QEMU command-line arguments.
func (q *QEMU) buildQEMUArgs(overlayPath, seedPath, qmpSock string, qgaPort int) []string {
	args := []string{
		"-nographic",
		"-serial", "none",
		"-m", q.memory,
		"-smp", q.cpus,
		"-accel", q.accel,
	}

	// Machine type
	if runtime.GOARCH == "arm64" {
		args = append(args, "-machine", "virt")
		args = append(args, "-cpu", "host")

		// UEFI firmware for aarch64
		efiPaths := []string{
			"/opt/homebrew/share/qemu/edk2-aarch64-code.fd", // Homebrew Apple Silicon
			"/usr/local/share/qemu/edk2-aarch64-code.fd",    // Homebrew Intel
			"/usr/share/AAVMF/AAVMF_CODE.fd",                // Debian/Ubuntu
			"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw",   // Fedora
			"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",       // Alpine
		}

		for _, path := range efiPaths {
			if _, err := os.Stat(path); err == nil {
				args = append(args, "-bios", path)

				break
			}
		}
	} else {
		args = append(args, "-machine", "q35")
		if q.accel == "tcg" {
			args = append(args, "-cpu", "max")
		} else {
			args = append(args, "-cpu", "host")
		}
	}

	// Boot disk
	args = append(args,
		"-drive", fmt.Sprintf("file=%s,if=virtio,format=qcow2", overlayPath),
	)

	// Cloud-init seed ISO
	args = append(args,
		"-drive", fmt.Sprintf("file=%s,if=virtio,format=raw", seedPath),
	)

	// QMP monitor socket
	args = append(args,
		"-qmp", fmt.Sprintf("unix:%s,server=on,wait=off", qmpSock),
	)

	// QGA virtio-serial channel (TCP socket for reliable reconnection)
	args = append(args,
		"-chardev", fmt.Sprintf("socket,host=127.0.0.1,port=%d,server=on,wait=off,id=qga0", qgaPort),
		"-device", "virtio-serial",
		"-device", "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0",
	)

	// 9p shared volumes directory
	args = append(args,
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=volumes,security_model=mapped-xattr,id=volumes", q.volumesDir),
	)

	return args
}

// connectQMP connects to the QMP monitor socket with retries.
func (q *QEMU) connectQMP(ctx context.Context, sockPath string) error {
	deadline := time.After(60 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for QMP socket at %s", sockPath)
		default:
		}

		if _, err := os.Stat(sockPath); err != nil {
			time.Sleep(500 * time.Millisecond)

			continue
		}

		mon, err := qmp.NewSocketMonitor("unix", sockPath, 5*time.Second)
		if err != nil {
			time.Sleep(500 * time.Millisecond)

			continue
		}

		if err := mon.Connect(); err != nil {
			time.Sleep(500 * time.Millisecond)

			continue
		}

		q.monitor = mon

		return nil
	}
}

// waitForCloudInit polls the guest agent until cloud-init has finished.
// Returns nil once boot-finished is detected, or an error on timeout/cancel.
func (q *QEMU) waitForCloudInit(ctx context.Context, addr string, deadline <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return errors.New("timeout waiting for cloud-init to finish")
		default:
		}

		client, err := NewQGAClient("tcp", addr)
		if err != nil {
			q.logger.Debug("qemu.qga.cloud-init.connect-retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		pid, err := client.Exec("/bin/sh", []string{"-c",
			"test -f /var/lib/cloud/instance/boot-finished",
		}, nil, nil)
		if err != nil {
			_ = client.Close()
			q.logger.Debug("qemu.qga.cloud-init.exec-retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		time.Sleep(time.Second)

		result, err := client.ExecStatus(pid)

		_ = client.Close()

		if err != nil {
			q.logger.Debug("qemu.qga.cloud-init.status-retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		if result.Exited && result.ExitCode == 0 {
			q.logger.Info("qemu.cloud-init.finished")

			return nil
		}

		q.logger.Debug("qemu.qga.cloud-init.not-ready", "exited", result.Exited, "exit_code", result.ExitCode)
		time.Sleep(3 * time.Second)
	}
}

// establishStableQGA connects to QGA and verifies a full exec cycle.
// Returns a connected client or an error on timeout/cancel.
func (q *QEMU) establishStableQGA(ctx context.Context, addr string, deadline <-chan time.Time) (*QGAClient, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, errors.New("timeout waiting for stable QGA connection")
		default:
		}

		client, err := NewQGAClient("tcp", addr)
		if err != nil {
			q.logger.Debug("qemu.qga.final-connect.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		// Verify with a full exec + exec-status cycle
		pid, err := client.Exec("/bin/echo", []string{"ready"}, nil, nil)
		if err != nil {
			_ = client.Close()
			q.logger.Debug("qemu.qga.final-exec.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		time.Sleep(time.Second)

		result, err := client.ExecStatus(pid)
		if err != nil {
			_ = client.Close()
			q.logger.Debug("qemu.qga.final-status.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		if !result.Exited || result.ExitCode != 0 {
			_ = client.Close()
			q.logger.Debug("qemu.qga.final-check.failed", "exited", result.Exited, "exit_code", result.ExitCode)
			time.Sleep(3 * time.Second)

			continue
		}

		return client, nil
	}
}

// connectQGA connects to the QGA TCP socket with retries.
// Waits for the guest to boot, cloud-init to finish, and the guest agent to stabilize.
func (q *QEMU) connectQGA(ctx context.Context, addr string) error {
	deadline := time.After(300 * time.Second)

	// Phase 1: Wait for cloud-init to finish by polling boot-finished file.
	q.logger.Debug("qemu.qga.waiting-for-cloud-init")

	if err := q.waitForCloudInit(ctx, addr, deadline); err != nil {
		return err
	}

	// Phase 2: Establish stable connection with full exec cycle verification
	q.logger.Debug("qemu.qga.connecting-final")

	client, err := q.establishStableQGA(ctx, addr, deadline)
	if err != nil {
		return err
	}

	q.qga = client

	return nil
}

// mountVolumes mounts the 9p shared directory inside the guest.
func (q *QEMU) mountVolumes() error {
	// Create mount point and mount the 9p filesystem
	pid, err := q.qga.Exec("/bin/sh", []string{"-c",
		"mkdir -p /mnt/volumes && " +
			"(mountpoint -q /mnt/volumes || mount -t 9p -o trans=virtio,version=9p2000.L,msize=104857600 volumes /mnt/volumes)",
	}, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to start mount command: %w", err)
	}

	// Wait for the mount command to complete
	for range 30 {
		result, err := q.qga.ExecStatus(pid)
		if err != nil {
			return fmt.Errorf("failed to check mount status: %w", err)
		}

		if result.Exited {
			if result.ExitCode != 0 {
				return fmt.Errorf("mount command exited with code %d", result.ExitCode)
			}

			return nil
		}

		time.Sleep(time.Second)
	}

	return errors.New("timeout waiting for mount command")
}

// bindMountVolumes creates host dirs and bind-mounts them inside the QEMU guest.
func (q *QEMU) bindMountVolumes(mounts []orchestra.Mount) error {
	for _, mount := range mounts {
		hostPath := filepath.Join(q.volumesDir, mount.Name)

		if err := os.MkdirAll(hostPath, 0o755); err != nil {
			return fmt.Errorf("failed to create volume dir: %w", err)
		}

		mountCmd := fmt.Sprintf(
			"mkdir -p %s && (mountpoint -q %s || mount --bind /mnt/volumes/%s %s)",
			mount.Path, mount.Path, mount.Name, mount.Path,
		)

		pid, err := q.qga.Exec("/bin/sh", []string{"-c", mountCmd}, nil, nil)
		if err != nil {
			return fmt.Errorf("failed to mount volume: %w", err)
		}

		for range 15 {
			result, err := q.qga.ExecStatus(pid)
			if err != nil {
				return fmt.Errorf("failed to check mount status: %w", err)
			}

			if result.Exited {
				if result.ExitCode != 0 {
					q.logger.Warn("qemu.mount.failed", "name", mount.Name, "exit_code", result.ExitCode)
				}

				break
			}

			time.Sleep(500 * time.Millisecond)
		}
	}

	return nil
}

// RunContainer executes a command inside the QEMU guest via QGA.
func (q *QEMU) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	if err := q.ensureVM(ctx); err != nil {
		return nil, fmt.Errorf("failed to ensure VM: %w", err)
	}

	q.mu.Lock()

	// Idempotency: return existing container if same task ID
	if existing, ok := q.containers[task.ID]; ok {
		q.mu.Unlock()

		return existing, nil
	}

	q.mu.Unlock()

	// Handle mounts: create directories on host and bind-mount inside guest
	if err := q.bindMountVolumes(task.Mounts); err != nil {
		return nil, err
	}

	// Build environment variables
	var env []string
	for k, v := range task.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	if task.Image != "" {
		q.logger.Debug("qemu.image.ignored", "image", task.Image, "msg", "QEMU driver runs commands directly in the guest OS")
	}

	// Read stdin if provided
	var stdinData []byte
	if task.Stdin != nil {
		var err error

		stdinData, err = io.ReadAll(task.Stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", err)
		}
	}

	// Execute the command via QGA
	execCommand := task.Command
	if task.WorkDir != "" {
		execCommand = []string{"/bin/sh", "-c", "cd " + task.WorkDir + " && exec " + strings.Join(task.Command, " ")}
	}

	pid, err := q.qga.Exec(execCommand[0], execCommand[1:], env, stdinData)
	if err != nil {
		return nil, fmt.Errorf("failed to exec command: %w", err)
	}

	container := &Container{
		qga:    q.qga,
		pid:    pid,
		taskID: task.ID,
	}

	q.mu.Lock()
	q.containers[task.ID] = container
	q.mu.Unlock()

	return container, nil
}

// CreateVolume creates a shared directory accessible to the guest.
func (q *QEMU) CreateVolume(ctx context.Context, name string, _ int) (orchestra.Volume, error) {
	err := q.ensureVM(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure VM: %w", err)
	}

	hostPath := filepath.Join(q.volumesDir, name)

	err = os.MkdirAll(hostPath, 0o755)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume dir: %w", err)
	}

	return &Volume{
		name:     name,
		hostPath: hostPath,
	}, nil
}

// GetContainer returns an existing container by task ID.
func (q *QEMU) GetContainer(_ context.Context, containerID string) (orchestra.Container, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if container, ok := q.containers[containerID]; ok {
		return container, nil
	}

	return nil, orchestra.ErrContainerNotFound
}

// Close shuts down the QEMU VM and cleans up all resources.
func (q *QEMU) Close() error {
	var errs []error

	// Send quit command via QMP
	if q.monitor != nil {
		_, _ = q.monitor.Run([]byte(`{"execute":"quit"}`))
		_ = q.monitor.Disconnect()
	}

	// Close QGA connection
	if q.qga != nil {
		_ = q.qga.Close()
	}

	// Wait for QEMU process to exit
	if q.cmd != nil && q.cmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- q.cmd.Wait()
		}()

		select {
		case <-done:
			// Process exited
		case <-time.After(10 * time.Second):
			// Force kill if it doesn't stop
			_ = q.cmd.Process.Kill()
			<-done
		}
	}

	// Clean up temp directory
	if q.tempDir != "" {
		err := os.RemoveAll(q.tempDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to remove temp dir: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}

	return nil
}

// qemuImgCommand creates an exec.Cmd for qemu-img.
func qemuImgCommand(args ...string) *exec.Cmd {
	return exec.Command("qemu-img", args...) //nolint:gosec
}

// findFreePort asks the OS for a free TCP port.
func findFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	port := listener.Addr().(*net.TCPAddr).Port

	_ = listener.Close()

	return port, nil
}

var (
	_ orchestra.Driver          = (*QEMU)(nil)
	_ orchestra.Container       = (*Container)(nil)
	_ orchestra.ContainerStatus = (*Status)(nil)
	_ orchestra.Volume          = (*Volume)(nil)
)
