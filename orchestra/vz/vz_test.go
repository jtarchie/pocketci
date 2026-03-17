//go:build darwin

package vz_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	vzdriver "github.com/jtarchie/pocketci/orchestra/vz"
	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
)

var testDriver orchestra.Driver

func TestMain(m *testing.M) {
	namespace := "test-vz-" + gonanoid.Must(6)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var err error
	testDriver, err = vzdriver.New(vzdriver.Config{
		Namespace: namespace,
		Memory:    "2048",
		CPUs:      "2",
	}, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create VZ driver: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	container, err := testDriver.RunContainer(ctx, orchestra.Task{
		ID:      "boot-check-" + gonanoid.Must(6),
		Image:   "busybox",
		Command: []string{"/bin/echo", "boot-ok"},
	})
	cancel()

	if err != nil {
		// Check if error is due to missing entitlements
		if strings.Contains(err.Error(), "com.apple.security.virtualization") {
			fmt.Fprintf(os.Stderr, "Skipping VZ tests: binary not codesigned with virtualization entitlement\n")
			fmt.Fprintf(os.Stderr, "To run VZ tests, codesign the test binary with: codesign -s - -f --entitlements <entitlements.plist> <binary>\n")
			_ = testDriver.Close()
			os.Exit(0)
		}

		fmt.Fprintf(os.Stderr, "Failed to boot VZ VM: %v\n", err)
		_ = testDriver.Close()
		os.Exit(1)
	}

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

func TestVZ_HappyPath(t *testing.T) {
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

		return strings.Contains(stdout.String(), "hello")
	}, "10s").Should(BeTrue())

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

	err = container.Cleanup(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestVZ_ExitCodeFailed(t *testing.T) {
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
}

func TestVZ_WithStdin(t *testing.T) {
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

func TestVZ_Volume(t *testing.T) {
	assert := NewGomegaWithT(t)

	volumeName := "testvol-" + gonanoid.Must(6)

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

func TestVZ_EnvironmentVariables(t *testing.T) {
	assert := NewGomegaWithT(t)

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

		return strings.Contains(stdout.String(), "HELLO=WORLD")
	}, "10s").Should(BeTrue())
}

func TestVZ_GetContainer(t *testing.T) {
	assert := NewGomegaWithT(t)

	_, err := testDriver.GetContainer(context.Background(), "nonexistent")
	assert.Expect(err).To(Equal(orchestra.ErrContainerNotFound))

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

func TestVZ_DriverName(t *testing.T) {
	assert := NewGomegaWithT(t)
	assert.Expect(testDriver.(*vzdriver.VZ).Name()).To(Equal("vz"))
}
