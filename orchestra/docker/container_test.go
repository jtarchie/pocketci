package docker_test

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
)

func TestDocker(t *testing.T) {
	t.Run("with a user", func(t *testing.T) {

		assert := NewGomegaWithT(t)

		client, err := docker.New(context.Background(), docker.Config{Namespace: "test-" + gonanoid.Must()}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = client.Close() }()

		taskID := gonanoid.Must()

		container, err := client.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:      taskID,
				Image:   "busybox",
				Command: []string{"whoami"},
				User:    "nobody",
			},
		)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			status, err := container.Status(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			return status.IsDone() && status.ExitCode() == 0
		}, "10s").Should(BeTrue())

		assert.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			_ = container.Logs(ctx, stdout, stderr, false)

			return strings.Contains(stdout.String(), "nobody")
		}, "1s").Should(BeTrue())

		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("with privileged", func(t *testing.T) {

		assert := NewGomegaWithT(t)

		client, err := docker.New(context.Background(), docker.Config{Namespace: "test-" + gonanoid.Must()}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = client.Close() }()

		taskID := gonanoid.Must()

		container, err := client.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:         taskID,
				Image:      "busybox",
				Command:    []string{"ls", "-l", "/dev/kmsg"},
				Privileged: true,
			},
		)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			status, err := container.Status(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			return status.IsDone() && status.ExitCode() == 0
		}, "10s").Should(BeTrue())

		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("with container limits", func(t *testing.T) {

		assert := NewGomegaWithT(t)

		client, err := docker.New(context.Background(), docker.Config{Namespace: "test-" + gonanoid.Must()}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = client.Close() }()

		t.Run("cpu limit", func(t *testing.T) {
			taskID := gonanoid.Must()

			// For cgroup v1: /sys/fs/cgroup/cpu/cpu.shares shows 512
			// For cgroup v2: /sys/fs/cgroup/cpu.weight shows converted value
			// Different Docker/kernel versions may convert shares differently
			container, err := client.RunContainer(
				context.Background(),
				orchestra.Task{
					ID:    taskID,
					Image: "busybox",
					Command: []string{
						"sh", "-c",
						"cat /sys/fs/cgroup/cpu/cpu.shares 2>/dev/null || cat /sys/fs/cgroup/cpu.weight 2>/dev/null",
					},
					ContainerLimits: orchestra.ContainerLimits{
						CPU: 512, // 512 CPU shares
					},
				},
			)
			assert.Expect(err).NotTo(HaveOccurred())

			assert.Eventually(func() bool {
				status, err := container.Status(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				return status.IsDone() && status.ExitCode() == 0
			}, "10s").Should(BeTrue())

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			err = container.Logs(ctx, stdout, stderr, false)
			assert.Expect(err).NotTo(HaveOccurred())

			output := strings.TrimSpace(stdout.String())
			// Should match any positive integer (CPU shares in cgroup v1 or weight in cgroup v2)
			// Expected: values like 512 (cgroup v1), 20, 59, etc. (cgroup v2)
			cpuLimitPattern := regexp.MustCompile(`^\d+$`)
			assert.Expect(cpuLimitPattern.MatchString(output)).To(BeTrue(), "CPU limit not a valid number. stdout: %q, stderr: %q", output, stderr.String())
		})

		t.Run("memory limit", func(t *testing.T) {
			taskID := gonanoid.Must()

			// Both cgroup v1 and v2 should show 134217728 bytes (128MB)
			container, err := client.RunContainer(
				context.Background(),
				orchestra.Task{
					ID:    taskID,
					Image: "busybox",
					Command: []string{
						"sh", "-c",
						"cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null || cat /sys/fs/cgroup/memory.max 2>/dev/null",
					},
					ContainerLimits: orchestra.ContainerLimits{
						Memory: 134217728, // 128MB in bytes
					},
				},
			)
			assert.Expect(err).NotTo(HaveOccurred())

			assert.Eventually(func() bool {
				status, err := container.Status(context.Background())
				assert.Expect(err).NotTo(HaveOccurred())

				return status.IsDone() && status.ExitCode() == 0
			}, "10s").Should(BeTrue())

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			err = container.Logs(ctx, stdout, stderr, false)
			assert.Expect(err).NotTo(HaveOccurred())

			output := stdout.String()
			hasMemoryLimit := strings.Contains(output, "134217728")
			assert.Expect(hasMemoryLimit).To(BeTrue(), "Memory limit not found. stdout: %q, stderr: %q", output, stderr.String())
		})

		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
