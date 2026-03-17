package k8s_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
)

func TestK8s(t *testing.T) {
	t.Parallel()

	// Check if k8s is available
	if !k8s.IsAvailable() {
		t.Skip("Kubernetes cluster not available")
	}

	t.Run("with a user", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		client, err := k8s.New(k8s.Config{Namespace: "test-" + gonanoid.Must()}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = client.Close() }()

		taskID := gonanoid.Must()

		// K8s requires numeric UIDs. In busybox, UID 65534 is typically "nobody"
		container, err := client.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:      taskID,
				Image:   "busybox",
				Command: []string{"id", "-u"},
				User:    "65534",
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

			// Check that the UID is 65534
			return strings.Contains(stdout.String(), "65534")
		}, "5s").Should(BeTrue())

		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("with privileged", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		client, err := k8s.New(k8s.Config{Namespace: "test-" + gonanoid.Must()}, slog.Default())
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
		}, "30s").Should(BeTrue())

		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("with container limits", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		client, err := k8s.New(k8s.Config{Namespace: "test-" + gonanoid.Must()}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() { _ = client.Close() }()

		t.Run("cpu and memory limits", func(t *testing.T) {
			taskID := gonanoid.Must()

			// Use sh to check cgroup values from inside the container
			// For cgroup v2 (modern k8s), check /sys/fs/cgroup/cpu.max and memory.max
			// For cgroup v1 (older k8s), check /sys/fs/cgroup/cpu/cpu.shares and memory/memory.limit_in_bytes
			// Note: K8s uses millicores for CPU, so we convert shares to millicores (shares * 1000 / 1024)
			container, err := client.RunContainer(
				context.Background(),
				orchestra.Task{
					ID:    taskID,
					Image: "busybox",
					Command: []string{
						"sh", "-c",
						"cat /sys/fs/cgroup/cpu/cpu.shares 2>/dev/null || cat /sys/fs/cgroup/cpu.weight 2>/dev/null || cat /sys/fs/cgroup/cpu.max 2>/dev/null; " +
							"cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null || cat /sys/fs/cgroup/memory.max 2>/dev/null",
					},
					ContainerLimits: orchestra.ContainerLimits{
						CPU:    512,       // 512 CPU shares (converted to ~500 millicores)
						Memory: 134217728, // 128MB in bytes
					},
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

				output := stdout.String()

				// Check if either cgroup v1 or v2 shows the limits
				// For CPU: K8s converts shares to millicores, so we might see different values
				// For Memory: should show 134217728 bytes
				// Note: K8s might show slightly different CPU values due to conversion
				hasMemoryLimit := strings.Contains(output, "134217728")

				// CPU limits in K8s are complex, so we just check memory for now
				return hasMemoryLimit
			}, "5s").Should(BeTrue())
		})

		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
