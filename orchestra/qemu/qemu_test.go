package qemu_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/qemu"
	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
)

var testDriver orchestra.Driver

func TestMain(m *testing.M) {
	// Skip all tests if QEMU is not installed
	binary := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		binary = "qemu-system-aarch64"
	}

	if _, err := exec.LookPath(binary); err != nil {
		fmt.Fprintf(os.Stderr, "QEMU not available (%s), skipping integration tests\n", binary)
		os.Exit(0)
	}

	if _, err := exec.LookPath("qemu-img"); err != nil {
		fmt.Fprintf(os.Stderr, "qemu-img not available, skipping integration tests\n")
		os.Exit(0)
	}

	namespace := "test-qemu-" + gonanoid.Must(6)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var err error
	testDriver, err = qemu.New(context.Background(), qemu.Config{
		ServerConfig: qemu.ServerConfig{
			Memory: "2048",
			CPUs:   "2",
		},
		Namespace: namespace,
	}, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create QEMU driver: %v\n", err)
		os.Exit(1)
	}

	// Pre-boot the VM by running a simple command
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	container, err := testDriver.RunContainer(ctx, orchestra.Task{
		ID:      "boot-check-" + gonanoid.Must(6),
		Image:   "busybox",
		Command: []string{"/bin/echo", "boot-ok"},
	})
	cancel()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to boot QEMU VM: %v\n", err)
		_ = testDriver.Close()
		os.Exit(1)
	}

	// Wait for the boot check to complete
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer bootCancel()

	for {
		status, err := container.Status(bootCtx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to check boot status: %v\n", err)
			_ = testDriver.Close()
			os.Exit(1)
		}

		if status.IsDone() {
			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	code := m.Run()
	_ = testDriver.Close()
	os.Exit(code)
}

func TestQEMU_HappyPath(t *testing.T) {
	assert := NewGomegaWithT(t)

	taskID := "happy-" + gonanoid.Must(6)

	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/bin/echo", "hello"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	// Wait for completion
	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}, "30s").Should(BeTrue())

	// Check stdout
	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)
		return strings.Contains(stdout.String(), "hello")
	}, "10s").Should(BeTrue())

	// Idempotency: running same task ID should return same container
	container2, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/bin/echo", "hello"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container2.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}).Should(BeTrue())

	assert.Eventually(func() bool {
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		err := container2.Logs(context.Background(), stdout, stderr, false)
		assert.Expect(err).NotTo(HaveOccurred())
		return strings.Contains(stdout.String(), "hello")
	}).Should(BeTrue())

	err = container.Cleanup(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestQEMU_ExitCodeFailed(t *testing.T) {
	assert := NewGomegaWithT(t)

	taskID := "fail-" + gonanoid.Must(6)

	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/bin/sh", "-c", "exit 1"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 1
	}, "30s").Should(BeTrue())

	assert.Consistently(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 1
	}).Should(BeTrue())
}

func TestQEMU_WithStdin(t *testing.T) {
	assert := NewGomegaWithT(t)

	taskID := "stdin-" + gonanoid.Must(6)

	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/bin/sh", "-c", "cat < /dev/stdin"},
			Stdin:   strings.NewReader("hello from stdin"),
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}, "30s").Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)
		return strings.Contains(stdout.String(), "hello from stdin")
	}, "10s").Should(BeTrue())
}

func TestQEMU_Volume(t *testing.T) {
	assert := NewGomegaWithT(t)

	volumeName := "testvol-" + gonanoid.Must(6)

	// Write to a volume in one container
	writeTaskID := "vol-write-" + gonanoid.Must(6)
	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      writeTaskID,
			Image:   "busybox",
			Command: []string{"/bin/sh", "-c", "echo world > /testvol/hello"},
			Mounts: orchestra.Mounts{
				{Name: volumeName, Path: "/testvol"},
			},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}, "30s").Should(BeTrue())

	// Read from the same volume in a different container
	readTaskID := "vol-read-" + gonanoid.Must(6)
	container2, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      readTaskID,
			Image:   "busybox",
			Command: []string{"/bin/cat", "/testvol/hello"},
			Mounts: orchestra.Mounts{
				{Name: volumeName, Path: "/testvol"},
			},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container2.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}, "30s").Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container2.Logs(ctx, stdout, stderr, false)
		return strings.Contains(stdout.String(), "world")
	}, "10s").Should(BeTrue())
}

func TestQEMU_EnvironmentVariables(t *testing.T) {
	assert := NewGomegaWithT(t)

	assert.Expect(os.Setenv("IGNORE", "ME")).NotTo(HaveOccurred()) //nolint:usetesting

	taskID := "env-" + gonanoid.Must(6)

	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/usr/bin/env"},
			Env:     map[string]string{"HELLO": "WORLD"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}, "30s").Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)
		return strings.Contains(stdout.String(), "HELLO=WORLD") && !strings.Contains(stdout.String(), "IGNORE")
	}, "10s").Should(BeTrue())
}

func TestQEMU_StreamingLogsWithFollow(t *testing.T) {
	assert := NewGomegaWithT(t)

	taskID := "follow-" + gonanoid.Must(6)

	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/bin/sh", "-c", "echo line1; sleep 0.1; echo line2; sleep 0.1; echo line3"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	// Start streaming logs before container finishes
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	streamCtx, streamCancel := context.WithCancel(context.Background())

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- container.Logs(streamCtx, stdout, stderr, true)
	}()

	// Wait for container to complete
	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
		return status.IsDone() && status.ExitCode() == 0
	}, "30s").Should(BeTrue())

	// Cancel the stream context after container is done
	streamCancel()

	// Wait for stream goroutine to finish
	select {
	case <-streamDone:
		// Stream finished
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not finish in time")
	}

	// Verify all lines were captured
	output := stdout.String()
	assert.Expect(output).To(ContainSubstring("line1"))
	assert.Expect(output).To(ContainSubstring("line2"))
	assert.Expect(output).To(ContainSubstring("line3"))
}

func TestQEMU_GetContainer(t *testing.T) {
	assert := NewGomegaWithT(t)

	// Non-existent container
	_, err := testDriver.GetContainer(context.Background(), "nonexistent")
	assert.Expect(err).To(Equal(orchestra.ErrContainerNotFound))

	// Existing container
	taskID := "getc-" + gonanoid.Must(6)
	container, err := testDriver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"/bin/echo", "found"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	found, err := testDriver.GetContainer(context.Background(), taskID)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(found.ID()).To(Equal(container.ID()))
}

func TestQEMU_DriverName(t *testing.T) {
	assert := NewGomegaWithT(t)
	assert.Expect(testDriver.(*qemu.QEMU).Name()).To(Equal("qemu"))
}
