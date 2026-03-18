package hetzner_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
)

func TestHetzner(t *testing.T) {
	token := os.Getenv("HETZNER_TOKEN")
	if token == "" {
		t.Skip("HETZNER_TOKEN not set, skipping Hetzner integration tests")
	}

	// Use a test-specific label to avoid cleaning up production resources
	const testLabel = "environment=test"

	// Clean up any orphaned resources from previous failed test runs (only those with test label)
	err := hetzner.CleanupOrphanedResources(context.Background(), token, slog.Default(), testLabel)
	if err != nil {
		t.Logf("Warning: failed to cleanup orphaned resources: %v", err)
	}

	// These tests are slow (server creation takes time) so do not run in parallel with other packages
	t.Run("basic container execution", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		namespace := "test-" + gonanoid.Must()
		client, err := hetzner.New(hetzner.Config{
			ServerConfig: hetzner.ServerConfig{
				Token:  token,
				Labels: testLabel,
			},
			Namespace: namespace,
		}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		// Always clean up the server, even if the test fails
		defer func() {
			closeErr := client.Close()
			assert.Expect(closeErr).NotTo(HaveOccurred())
		}()

		taskID := gonanoid.Must()

		container, err := client.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:      taskID,
				Image:   "busybox",
				Command: []string{"echo", "hello from hetzner"},
			},
		)
		assert.Expect(err).NotTo(HaveOccurred())

		// Server creation + container run can take a while
		assert.Eventually(func() bool {
			status, err := container.Status(context.Background())
			if err != nil {
				return false
			}

			return status.IsDone() && status.ExitCode() == 0
		}, "10m", "5s").Should(BeTrue())

		assert.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			_ = container.Logs(ctx, stdout, stderr, false)

			return strings.Contains(stdout.String(), "hello from hetzner")
		}, "30s", "2s").Should(BeTrue())
	})

	t.Run("with auto size", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		namespace := "test-" + gonanoid.Must()
		client, err := hetzner.New(hetzner.Config{
			ServerConfig: hetzner.ServerConfig{
				Token:      token,
				ServerType: "auto",
				Labels:     testLabel,
			},
			Namespace: namespace,
		}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		// Always clean up the server, even if the test fails
		defer func() {
			closeErr := client.Close()
			assert.Expect(closeErr).NotTo(HaveOccurred())
		}()

		taskID := gonanoid.Must()

		container, err := client.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:      taskID,
				Image:   "busybox",
				Command: []string{"sh", "-c", "cat /proc/meminfo | head -1"},
				ContainerLimits: orchestra.ContainerLimits{
					Memory: 2 * 1024 * 1024 * 1024, // 2GB - should trigger cx32 or larger
					CPU:    1024,
				},
			},
		)
		assert.Expect(err).NotTo(HaveOccurred())

		// Server creation + container run can take a while
		assert.Eventually(func() bool {
			status, err := container.Status(context.Background())
			if err != nil {
				return false
			}

			return status.IsDone() && status.ExitCode() == 0
		}, "10m", "5s").Should(BeTrue())

		assert.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			_ = container.Logs(ctx, stdout, stderr, false)

			return strings.Contains(stdout.String(), "MemTotal")
		}, "30s", "2s").Should(BeTrue())
	})

	t.Run("reuse_worker parks and reclaims machine", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		// Use a shared namespace so both drivers target the same worker pool
		namespace := "test-" + gonanoid.Must()
		cfg := hetzner.Config{
			ServerConfig: hetzner.ServerConfig{
				Token:       token,
				Labels:      testLabel,
				ReuseWorker: true,
				MaxWorkers:  1,
			},
			Namespace: namespace,
		}

		// First run: creates a new machine and parks it on close
		client1, err := hetzner.New(cfg, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		container1, err := client1.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:      gonanoid.Must(),
				Image:   "busybox",
				Command: []string{"echo", "run1"},
			},
		)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			status, err := container1.Status(context.Background())
			return err == nil && status.IsDone() && status.ExitCode() == 0
		}, "10m", "5s").Should(BeTrue())

		// Park the machine (reuse_worker=true means Close() parks instead of deletes)
		assert.Expect(client1.Close()).NotTo(HaveOccurred())

		// Second run: should claim the parked machine
		client2, err := hetzner.New(cfg, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		defer func() {
			// Final cleanup: park and then clean up idle machines manually
			_ = client2.Close()
			_ = hetzner.CleanupOrphanedResources(context.Background(), token, slog.Default(), testLabel)
		}()

		container2, err := client2.RunContainer(
			context.Background(),
			orchestra.Task{
				ID:      gonanoid.Must(),
				Image:   "busybox",
				Command: []string{"echo", "run2"},
			},
		)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			status, err := container2.Status(context.Background())
			return err == nil && status.IsDone() && status.ExitCode() == 0
		}, "10m", "5s").Should(BeTrue())
	})

	t.Run("max_workers limits concurrent machines", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		// Use a shared namespace so both drivers share the same worker pool
		namespace := "test-" + gonanoid.Must()
		cfg := hetzner.Config{
			ServerConfig: hetzner.ServerConfig{
				Token:        token,
				Labels:       testLabel,
				MaxWorkers:   1,
				PollInterval: orchestra.Duration(5 * time.Second),
				WaitTimeout:  orchestra.Duration(15 * time.Minute),
			},
			Namespace: namespace,
		}

		var (
			mu          sync.Mutex
			activeTasks []string
			overlap     bool
		)

		run := func(label string) error {
			client, err := hetzner.New(cfg, slog.Default())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			container, err := client.RunContainer(
				context.Background(),
				orchestra.Task{
					ID:      gonanoid.Must(),
					Image:   "busybox",
					Command: []string{"sleep", "5"},
				},
			)
			if err != nil {
				return err
			}

			mu.Lock()
			activeTasks = append(activeTasks, label)
			if len(activeTasks) > 1 {
				overlap = true
			}
			mu.Unlock()

			for {
				status, err := container.Status(context.Background())
				if err != nil {
					time.Sleep(5 * time.Second)
					continue
				}
				if status.IsDone() {
					break
				}
				time.Sleep(5 * time.Second)
			}

			mu.Lock()
			for i, a := range activeTasks {
				if a == label {
					activeTasks = append(activeTasks[:i], activeTasks[i+1:]...)
					break
				}
			}
			mu.Unlock()

			return nil
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)

		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = run("task-a") }()
		go func() { defer wg.Done(); errs[1] = run("task-b") }()
		wg.Wait()

		assert.Expect(errs[0]).NotTo(HaveOccurred())
		assert.Expect(errs[1]).NotTo(HaveOccurred())
		assert.Expect(overlap).To(BeFalse(), "max_workers=1 should prevent concurrent execution")
	})
}
