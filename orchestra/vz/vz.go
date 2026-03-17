//go:build darwin

package vz

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/vz/agent"
)

// Config holds configuration for the VZ (Apple Virtualization) driver.
type Config struct {
	Namespace string // Per-execution namespace identifier
	Memory    string // VM memory in MB (default: "2048")
	CPUs      string // VM CPU count (default: "2")
	CacheDir  string // Directory for image cache
	Image     string // Boot image path (optional; downloaded if empty)
}

// VZ implements orchestra.Driver using Apple's Virtualization.framework.
// Commands are executed inside the guest via a vsock-based agent.
// Volumes are shared between host and guest via virtiofs.
type VZ struct {
	vm           *vz.VirtualMachine
	socketDevice *vz.VirtioSocketDevice
	agent        *AgentClient
	namespace    string
	logger       *slog.Logger
	tempDir      string
	volumesDir   string
	image        string

	bootOnce sync.Once
	bootErr  error

	mu         sync.Mutex
	containers map[string]*Container

	memory   uint64
	cpus     uint
	cacheDir string
}

// Name returns the driver name.
func (v *VZ) Name() string {
	return "vz"
}

// New creates a new Apple Virtualization framework driver.
func New(cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	homeDir, _ := os.UserHomeDir()
	defaultCacheDir := filepath.Join(homeDir, ".cache", "pocketci", "vz")

	memoryStr := cfg.Memory
	if memoryStr == "" {
		if v := os.Getenv("VZ_MEMORY"); v != "" {
			memoryStr = v
		} else {
			memoryStr = "2048"
		}
	}

	cpusStr := cfg.CPUs
	if cpusStr == "" {
		if v := os.Getenv("VZ_CPUS"); v != "" {
			cpusStr = v
		} else {
			cpusStr = "2"
		}
	}

	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		if v := os.Getenv("VZ_CACHE_DIR"); v != "" {
			cacheDir = v
		} else {
			cacheDir = defaultCacheDir
		}
	}

	memory, err := strconv.ParseUint(memoryStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid memory value %q: %w", memoryStr, err)
	}

	cpus, err := strconv.ParseUint(cpusStr, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid cpus value %q: %w", cpusStr, err)
	}

	d := &VZ{
		namespace:  cfg.Namespace,
		logger:     logger,
		image:      cfg.Image,
		containers: make(map[string]*Container),
		memory:     memory * 1024 * 1024, // convert MB to bytes
		cpus:       uint(cpus),
		cacheDir:   cacheDir,
	}

	return d, nil
}

// ensureVM lazily boots the VM on first use. Idempotent.
func (v *VZ) ensureVM(ctx context.Context) error {
	v.bootOnce.Do(func() {
		v.bootErr = v.bootVM(ctx)
	})

	return v.bootErr
}

// bootVM performs the actual VM boot. Called once by ensureVM.
func (v *VZ) bootVM(ctx context.Context) error {
	v.logger.Info("vz.vm.starting", "namespace", v.namespace)

	// Create temp dir for runtime files
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("ci-vz-%s-", v.namespace))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}

	v.tempDir = tempDir

	// Create volumes directory inside temp dir
	v.volumesDir = filepath.Join(tempDir, "volumes")
	if err := os.MkdirAll(v.volumesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create volumes dir: %w", err)
	}

	// Prepare the disk image
	imagePath := v.image
	if imagePath == "" {
		imagePath = os.Getenv("VZ_IMAGE")
	}

	if imagePath == "" {
		v.logger.Info("vz.image.downloading", "cache_dir", v.cacheDir)

		imagePath, err = downloadImage(v.cacheDir)
		if err != nil {
			return fmt.Errorf("failed to download image: %w", err)
		}

		v.logger.Info("vz.image.ready", "path", imagePath)
	}

	// Create a writable copy of the base image
	diskPath := filepath.Join(tempDir, "disk.raw")
	if err := createDiskCopy(imagePath, diskPath); err != nil {
		return fmt.Errorf("failed to create disk copy: %w", err)
	}

	// Create cloud-init seed ISO
	seedPath := filepath.Join(tempDir, "seed.iso")
	if err := createSeedISO(seedPath, v.namespace); err != nil {
		return fmt.Errorf("failed to create seed ISO: %w", err)
	}

	// Build the agent binary and place it in the volumes dir so cloud-init can find it
	if err := v.buildAgent(); err != nil {
		v.logger.Warn("vz.agent.build.failed", "err", err, "msg", "will fall back to cloud-init agent startup")
	}

	// Configure the virtual machine
	config, socketDevice, err := v.buildVMConfig(diskPath, seedPath)
	if err != nil {
		return fmt.Errorf("failed to build VM config: %w", err)
	}

	valid, err := config.Validate()
	if err != nil {
		return fmt.Errorf("failed to validate VM config: %w", err)
	}

	if !valid {
		return fmt.Errorf("VM configuration is not valid")
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	v.vm = vm
	v.socketDevice = socketDevice

	// Start the VM
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	v.logger.Info("vz.vm.started")

	// Wait for VM to reach running state
	if err := v.waitForVMState(ctx, vz.VirtualMachineStateRunning, 30*time.Second); err != nil {
		return fmt.Errorf("VM failed to reach running state: %w", err)
	}

	v.logger.Info("vz.vm.running")

	// Connect to the agent via vsock
	if err := v.connectAgent(ctx); err != nil {
		return fmt.Errorf("failed to connect to agent: %w", err)
	}

	v.logger.Info("vz.agent.connected")

	// Mount virtiofs inside guest
	if err := v.mountVolumes(); err != nil {
		v.logger.Warn("vz.volumes.mount.failed", "err", err)
		// Non-fatal — volumes may mount later on demand
	}

	return nil
}

// buildVMConfig creates the VirtualMachineConfiguration for the VM.
func (v *VZ) buildVMConfig(diskPath, seedPath string) (*vz.VirtualMachineConfiguration, *vz.VirtioSocketDevice, error) {
	// Use EFI boot loader (requires macOS 13+)
	efiStorePath := filepath.Join(v.tempDir, "efi-variable-store")

	efiVariableStore, err := vz.NewEFIVariableStore(efiStorePath, vz.WithCreatingEFIVariableStore())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create EFI variable store: %w", err)
	}

	bootLoader, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(efiVariableStore))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create EFI boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, v.cpus, v.memory)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create VM configuration: %w", err)
	}

	// Root disk
	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create disk attachment: %w", err)
	}

	blockDevice, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create block device: %w", err)
	}

	// Cloud-init seed ISO (read-only)
	seedAttachment, err := vz.NewDiskImageStorageDeviceAttachment(seedPath, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create seed attachment: %w", err)
	}

	seedDevice, err := vz.NewVirtioBlockDeviceConfiguration(seedAttachment)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create seed device: %w", err)
	}

	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{
		blockDevice,
		seedDevice,
	})

	// NAT networking
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create NAT attachment: %w", err)
	}

	networkConfig, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create network config: %w", err)
	}

	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{
		networkConfig,
	})

	// Virtiofs shared directory for volumes
	sharedDir, err := vz.NewSharedDirectory(v.volumesDir, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create shared directory: %w", err)
	}

	share, err := vz.NewSingleDirectoryShare(sharedDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create directory share: %w", err)
	}

	fsConfig, err := vz.NewVirtioFileSystemDeviceConfiguration("volumes")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create filesystem config: %w", err)
	}

	fsConfig.SetDirectoryShare(share)

	config.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{
		fsConfig,
	})

	// Vsock device for host↔guest communication
	vsockConfig, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create vsock config: %w", err)
	}

	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{
		vsockConfig,
	})

	// Entropy device (speeds up boot)
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create entropy config: %w", err)
	}

	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{
		entropyConfig,
	})

	// Serial console for debugging
	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stderr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create serial port attachment: %w", err)
	}

	serialPort, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create serial port: %w", err)
	}

	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{
		serialPort,
	})

	// We'll get the socket device after VM creation
	return config, nil, nil
}

// waitForVMState waits until the VM reaches the desired state or timeout.
func (v *VZ) waitForVMState(ctx context.Context, state vz.VirtualMachineState, timeout time.Duration) error {
	deadline := time.After(timeout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for VM state %d", state)
		default:
		}

		if v.vm.State() == state {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// connectAgent connects to the vsock guest agent with retries.
func (v *VZ) connectAgent(ctx context.Context) error {
	deadline := time.After(300 * time.Second)

	v.logger.Debug("vz.agent.waiting-for-cloud-init")

	// Get the vsock device from the VM
	socketDevices := v.vm.SocketDevices()
	if len(socketDevices) == 0 {
		return fmt.Errorf("no vsock devices found on VM")
	}

	v.socketDevice = socketDevices[0]

	// Phase 1: Wait for cloud-init to complete and agent to become available
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for guest agent")
		default:
		}

		conn, err := v.socketDevice.Connect(uint32(agent.VsockPort))
		if err != nil {
			v.logger.Debug("vz.agent.connect.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		connectFn := func() (net.Conn, error) {
			return v.socketDevice.Connect(uint32(agent.VsockPort))
		}

		client := NewAgentClient(conn, connectFn)

		if err := client.Ping(); err != nil {
			_ = client.Close()
			v.logger.Debug("vz.agent.ping.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		// Verify with a full exec cycle
		pid, err := client.Exec("/bin/echo", []string{"ready"}, nil, nil)
		if err != nil {
			_ = client.Close()
			v.logger.Debug("vz.agent.exec.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		time.Sleep(time.Second)

		result, err := client.ExecStatus(pid)
		if err != nil {
			_ = client.Close()
			v.logger.Debug("vz.agent.status.retry", "err", err)
			time.Sleep(3 * time.Second)

			continue
		}

		if !result.Exited || result.ExitCode != 0 {
			_ = client.Close()
			v.logger.Debug("vz.agent.check.failed", "exited", result.Exited, "exit_code", result.ExitCode)
			time.Sleep(3 * time.Second)

			continue
		}

		v.agent = client

		return nil
	}
}

// mountVolumes mounts the virtiofs shared directory inside the guest.
func (v *VZ) mountVolumes() error {
	pid, err := v.agent.Exec("/bin/sh", []string{"-c",
		"mkdir -p /mnt/volumes && " +
			"(mountpoint -q /mnt/volumes || mount -t virtiofs volumes /mnt/volumes)",
	}, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to start mount command: %w", err)
	}

	// Wait for the mount command to complete
	for range 30 {
		result, err := v.agent.ExecStatus(pid)
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

	return fmt.Errorf("timeout waiting for mount command")
}

// RunContainer executes a command inside the VZ guest via the vsock agent.
func (v *VZ) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	if err := v.ensureVM(ctx); err != nil {
		return nil, fmt.Errorf("failed to ensure VM: %w", err)
	}

	v.mu.Lock()

	// Idempotency: return existing container if same task ID
	if existing, ok := v.containers[task.ID]; ok {
		v.mu.Unlock()

		return existing, nil
	}

	v.mu.Unlock()

	// Handle mounts: create directories on host and bind-mount inside guest
	for _, mount := range task.Mounts {
		hostPath := filepath.Join(v.volumesDir, mount.Name)

		if err := os.MkdirAll(hostPath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create volume dir: %w", err)
		}

		// Create the mount path inside guest and bind-mount from virtiofs share
		mountCmd := fmt.Sprintf(
			"mkdir -p %s && (mountpoint -q %s || mount --bind /mnt/volumes/%s %s)",
			mount.Path, mount.Path, mount.Name, mount.Path,
		)

		pid, err := v.agent.Exec("/bin/sh", []string{"-c", mountCmd}, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to mount volume: %w", err)
		}

		// Wait for mount to complete
		for range 15 {
			result, err := v.agent.ExecStatus(pid)
			if err != nil {
				return nil, fmt.Errorf("failed to check mount status: %w", err)
			}

			if result.Exited {
				if result.ExitCode != 0 {
					v.logger.Warn("vz.mount.failed", "name", mount.Name, "exit_code", result.ExitCode)
				}

				break
			}

			time.Sleep(500 * time.Millisecond)
		}
	}

	// Build environment variables
	var env []string
	for k, val := range task.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, val))
	}

	if task.Image != "" {
		v.logger.Debug("vz.image.ignored", "image", task.Image, "msg", "VZ driver runs commands directly in the guest OS")
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

	// Execute the command via agent
	execCommand := task.Command
	if task.WorkDir != "" {
		execCommand = []string{"/bin/sh", "-c", "cd " + task.WorkDir + " && exec " + strings.Join(task.Command, " ")}
	}

	pid, err := v.agent.Exec(execCommand[0], execCommand[1:], env, stdinData)
	if err != nil {
		return nil, fmt.Errorf("failed to exec command: %w", err)
	}

	container := &Container{
		agent:  v.agent,
		pid:    pid,
		taskID: task.ID,
	}

	v.mu.Lock()
	v.containers[task.ID] = container
	v.mu.Unlock()

	return container, nil
}

// CreateVolume creates a shared directory accessible to the guest.
func (v *VZ) CreateVolume(ctx context.Context, name string, _ int) (orchestra.Volume, error) {
	if err := v.ensureVM(ctx); err != nil {
		return nil, fmt.Errorf("failed to ensure VM: %w", err)
	}

	hostPath := filepath.Join(v.volumesDir, name)

	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create volume dir: %w", err)
	}

	return &Volume{
		name:     name,
		hostPath: hostPath,
	}, nil
}

// GetContainer returns an existing container by task ID.
func (v *VZ) GetContainer(_ context.Context, containerID string) (orchestra.Container, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if container, ok := v.containers[containerID]; ok {
		return container, nil
	}

	return nil, orchestra.ErrContainerNotFound
}

// Close shuts down the Apple VZ VM and cleans up all resources.
func (v *VZ) Close() error {
	var errs []error

	// Close agent connection
	if v.agent != nil {
		_ = v.agent.Close()
	}

	// Stop the VM
	if v.vm != nil {
		canStop := v.vm.CanRequestStop()
		if canStop {
			stopped, err := v.vm.RequestStop()
			if err != nil || !stopped {
				// Force stop if graceful stop fails
				if err := v.vm.Stop(); err != nil {
					errs = append(errs, fmt.Errorf("failed to stop VM: %w", err))
				}
			} else {
				// Wait for VM to stop
				deadline := time.After(10 * time.Second)
			stopLoop:
				for v.vm.State() != vz.VirtualMachineStateStopped {

					select {
					case <-deadline:
						// Force stop on timeout
						_ = v.vm.Stop()

						break stopLoop
					default:
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
		} else {
			if err := v.vm.Stop(); err != nil {
				errs = append(errs, fmt.Errorf("failed to stop VM: %w", err))
			}
		}
	}

	// Clean up temp directory
	if v.tempDir != "" {
		if err := os.RemoveAll(v.tempDir); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove temp dir: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}

	return nil
}

// buildAgent cross-compiles the vsock agent for linux and places it in the volumes dir.
func (v *VZ) buildAgent() error {
	agentPath := filepath.Join(v.volumesDir, ".ci-agent")

	// Cross-compile for linux/arm64 (Apple Silicon VMs run arm64 Linux)
	// Use the full module path for cross-compilation
	cmd := execCommand("go", "build", "-o", agentPath, "github.com/jtarchie/pocketci/orchestra/vz/agent/cmd")
	cmd.Env = append(os.Environ(),
		"GOOS=linux",
		"GOARCH=arm64",
		"CGO_ENABLED=0",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to build agent: %w: %s", err, string(output))
	}

	// Make agent executable
	if err := os.Chmod(agentPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod agent: %w", err)
	}

	// Verify the file exists and has content
	info, err := os.Stat(agentPath)
	if err != nil {
		return fmt.Errorf("agent binary not found after build: %w", err)
	}

	if info.Size() == 0 {
		return fmt.Errorf("agent binary is empty")
	}

	v.logger.Info("vz.agent.built", "path", agentPath, "size", info.Size())

	return nil
}

// execCommand wraps exec.Command for testability.
func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...) //nolint:gosec
}

var (
	_ orchestra.Driver          = (*VZ)(nil)
	_ orchestra.Container       = (*Container)(nil)
	_ orchestra.ContainerStatus = (*Status)(nil)
	_ orchestra.Volume          = (*Volume)(nil)
)
