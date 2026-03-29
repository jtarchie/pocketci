package orchestra_test

import (
	"archive/tar"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/native"
	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
)

func TestDrivers(t *testing.T) {
	t.Parallel()

	type driverEntry struct {
		name      string
		newDriver func(namespace string) (orchestra.Driver, error)
	}

	entries := []driverEntry{
		{"docker", func(ns string) (orchestra.Driver, error) {
			return docker.New(context.Background(), docker.Config{Namespace: ns}, slog.Default())
		}},
		{"native", func(ns string) (orchestra.Driver, error) {
			return native.New(context.Background(), native.Config{Namespace: ns}, slog.Default())
		}},
		{"k8s", func(ns string) (orchestra.Driver, error) {
			return k8s.New(context.Background(), k8s.Config{Namespace: ns}, slog.Default())
		}},
		{"fly", func(ns string) (orchestra.Driver, error) {
			return fly.New(context.Background(), fly.Config{ServerConfig: fly.ServerConfig{Token: os.Getenv("FLY_API_TOKEN")}, Namespace: ns}, slog.Default())
		}},
	}

	for _, tc := range entries {
		t.Run(tc.name, func(t *testing.T) {
			runDriverTests(t, tc.name, tc.newDriver)
		})
	}
}

type driverTestCtx struct {
	name           string
	init           func(namespace string) (orchestra.Driver, error)
	statusTimeout  string
	statusInterval string
	logsTimeout    string
	logsInterval   string
}

func runDriverTests(t *testing.T, name string, newDriver func(namespace string) (orchestra.Driver, error)) {
	t.Helper()

	if name == "native" {
		t.Parallel()
	}

	if name == "k8s" && !k8s.IsAvailable() {
		t.Skip("Kubernetes cluster not available")
	}

	if name == "fly" && os.Getenv("FLY_API_TOKEN") == "" {
		t.Skip("FLY_API_TOKEN not set, skipping Fly integration tests")
	}

	dtc := driverTestCtx{
		name:           name,
		init:           newDriver,
		statusTimeout:  "10s",
		statusInterval: "100ms",
		logsTimeout:    "10s",
		logsInterval:   "100ms",
	}
	if name == "fly" {
		dtc.statusTimeout = "2m"
		dtc.statusInterval = "1s"
		dtc.logsTimeout = "30s"
		dtc.logsInterval = "2s"
	}

	t.Run("with stdin", func(t *testing.T) { testDriverStdin(t, dtc) })
	t.Run("exit code failed", func(t *testing.T) { testDriverExitCodeFailed(t, dtc) })
	t.Run("happy path", func(t *testing.T) { testDriverHappyPath(t, dtc) })
	t.Run("volume", func(t *testing.T) { testDriverVolume(t, dtc) })
	t.Run("read files from volume", func(t *testing.T) { testDriverReadFiles(t, dtc) })
	t.Run("environment variables", func(t *testing.T) { testDriverEnvVars(t, dtc) })
	t.Run("streaming logs with follow", func(t *testing.T) { testDriverStreamingLogs(t, dtc) })
}

func testDriverStdin(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	// Fly machines don't support piping stdin from the client
	if dtc.name == "fly" {
		t.Skip("Fly machines do not support stdin")
	}

	assert := NewGomegaWithT(t)

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	taskID := gonanoid.Must()

	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"sh", "-c", "cat < /dev/stdin"},
			Stdin:   strings.NewReader("hello"),
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)

		return strings.Contains(stdout.String(), "hello")
	}, dtc.logsTimeout, dtc.logsInterval).Should(BeTrue())

	err = client.Close()
	assert.Expect(err).NotTo(HaveOccurred())
}

func testDriverExitCodeFailed(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	assert := NewGomegaWithT(t)

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	taskID := gonanoid.Must()

	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"sh", "-c", "exit 1"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 1
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())
	assert.Consistently(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 1
	}).Should(BeTrue())

	err = client.Close()
	assert.Expect(err).NotTo(HaveOccurred())
}

func testDriverHappyPath(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	assert := NewGomegaWithT(t)

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	taskID := gonanoid.Must()

	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"echo", "hello"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)
		// assert.Expect(err).NotTo(HaveOccurred())

		return strings.Contains(stdout.String(), "hello")
	}, dtc.logsTimeout, dtc.logsInterval).Should(BeTrue())
	// running a container should be deterministic and idempotent
	container, err = client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"echo", "hello"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}).Should(BeTrue())

	assert.Eventually(func() bool {
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		err := container.Logs(context.Background(), stdout, stderr, false)
		assert.Expect(err).NotTo(HaveOccurred())

		return strings.Contains(stdout.String(), "hello")
	}).Should(BeTrue())

	err = container.Cleanup(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())

	err = client.Close()
	assert.Expect(err).NotTo(HaveOccurred())
}

func testDriverVolume(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	assert := NewGomegaWithT(t)

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	taskID := gonanoid.Must()

	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"sh", "-c", "echo world > ./test/hello"},
			Mounts: orchestra.Mounts{
				{Name: "test", Path: "/test"},
			},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())

	container, err = client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID + "-2",
			Image:   "busybox",
			Command: []string{"cat", "./test/hello"},
			Mounts: orchestra.Mounts{
				{Name: "test", Path: "/test"},
			},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)

		return strings.Contains(stdout.String(), "world")
	}, dtc.logsTimeout, dtc.logsInterval).Should(BeTrue())
	err = client.Close()
	assert.Expect(err).NotTo(HaveOccurred())
}

func testDriverReadFiles(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	assert := NewGomegaWithT(t)

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	accessor, ok := client.(cache.VolumeDataAccessor)
	if !ok {
		t.Skip("driver does not implement VolumeDataAccessor")
	}

	taskID := gonanoid.Must()

	// Write two files into a volume via a container
	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"sh", "-c", "echo file-a-content > ./data/a.txt && mkdir -p ./data/sub && echo file-b-content > ./data/sub/b.txt"},
			Mounts: orchestra.Mounts{
				{Name: "data", Path: "/data"},
			},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())

	// Read a single file
	reader, err := accessor.ReadFilesFromVolume(context.Background(), "data", "a.txt")
	assert.Expect(err).NotTo(HaveOccurred())

	files := extractTarFiles(t, reader)
	assert.Expect(files).To(HaveKey("a.txt"))
	assert.Expect(files["a.txt"]).To(ContainSubstring("file-a-content"))

	// Read a subdirectory
	reader, err = accessor.ReadFilesFromVolume(context.Background(), "data", "sub")
	assert.Expect(err).NotTo(HaveOccurred())

	files = extractTarFiles(t, reader)
	// The tar should contain the file inside sub/
	found := false
	for path, content := range files {
		if strings.HasSuffix(path, "b.txt") {
			assert.Expect(content).To(ContainSubstring("file-b-content"))
			found = true
		}
	}
	assert.Expect(found).To(BeTrue(), "expected sub/b.txt in tar")
}

func testDriverEnvVars(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	assert := NewGomegaWithT(t)

	t.Setenv("IGNORE", "ME")

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	taskID := gonanoid.Must()

	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"env"},
			Env:     map[string]string{"HELLO": "WORLD"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())

	assert.Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		_ = container.Logs(ctx, stdout, stderr, false)

		return strings.Contains(stdout.String(), "HELLO=WORLD\n") && !strings.Contains(stdout.String(), "IGNORE")
	}, dtc.logsTimeout, dtc.logsInterval).Should(BeTrue())
}

func testDriverStreamingLogs(t *testing.T, dtc driverTestCtx) {
	if dtc.name == "native" {
		t.Parallel()
	}

	assert := NewGomegaWithT(t)

	client, err := dtc.init("test-" + gonanoid.Must())
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = client.Close() }()

	taskID := gonanoid.Must()

	// Use a command that outputs multiple lines with delays
	container, err := client.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      taskID,
			Image:   "busybox",
			Command: []string{"sh", "-c", "echo line1; sleep 0.1; echo line2; sleep 0.1; echo line3"},
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
	}, dtc.statusTimeout, dtc.statusInterval).Should(BeTrue())
	// Cancel the stream context after container is done
	streamCancel()

	// Wait for stream goroutine to finish
	streamWaitTime := 5 * time.Second
	if dtc.name == "fly" {
		streamWaitTime = 30 * time.Second
	}

	select {
	case <-streamDone:
		// Stream finished
	case <-time.After(streamWaitTime):
		t.Fatal("stream did not finish in time")
	}

	// Verify all lines were captured
	output := stdout.String()
	assert.Expect(output).To(ContainSubstring("line1"))
	assert.Expect(output).To(ContainSubstring("line2"))
	assert.Expect(output).To(ContainSubstring("line3"))

	err = client.Close()
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestSandboxDrivers(t *testing.T) {
	t.Parallel()

	type driverEntry struct {
		name      string
		newDriver func(namespace string) (orchestra.Driver, error)
	}

	entries := []driverEntry{
		{"docker", func(ns string) (orchestra.Driver, error) {
			return docker.New(context.Background(), docker.Config{Namespace: ns}, slog.Default())
		}},
		{"native", func(ns string) (orchestra.Driver, error) {
			return native.New(context.Background(), native.Config{Namespace: ns}, slog.Default())
		}},
		{"k8s", func(ns string) (orchestra.Driver, error) {
			return k8s.New(context.Background(), k8s.Config{Namespace: ns}, slog.Default())
		}},
		{"fly", func(ns string) (orchestra.Driver, error) {
			return fly.New(context.Background(), fly.Config{ServerConfig: fly.ServerConfig{Token: os.Getenv("FLY_API_TOKEN")}, Namespace: ns}, slog.Default())
		}},
	}

	for _, tc := range entries {
		t.Run(tc.name, func(t *testing.T) {
			runSandboxDriverTests(t, tc.name, tc.newDriver)
		})
	}
}

func runSandboxDriverTests(t *testing.T, name string, newDriver func(namespace string) (orchestra.Driver, error)) {
	t.Helper()

	if name == "native" {
		t.Parallel()
	}

	// Skip k8s tests if cluster is not available.
	if name == "k8s" && !k8s.IsAvailable() {
		t.Skip("Kubernetes cluster not available")
	}

	// Skip fly tests if token is not available.
	if name == "fly" && os.Getenv("FLY_API_TOKEN") == "" {
		t.Skip("FLY_API_TOKEN not set, skipping Fly integration tests")
	}

	// Check driver supports SandboxDriver interface using a probe client.
	probeClient, err := newDriver("test-" + gonanoid.Must())
	if err != nil {
		t.Skipf("driver init failed: %v", err)
	}

	_, isSandbox := probeClient.(orchestra.SandboxDriver)
	_ = probeClient.Close()

	if !isSandbox {
		t.Skipf("driver %q does not implement SandboxDriver", name)
	}

	newSandboxDriver := func(t *testing.T) orchestra.SandboxDriver {
		t.Helper()

		client, err := newDriver("test-" + gonanoid.Must())
		if err != nil {
			t.Fatalf("driver init failed: %v", err)
		}

		t.Cleanup(func() { _ = client.Close() })

		return client.(orchestra.SandboxDriver)
	}

	t.Run("sequential commands share environment", func(t *testing.T) {
		if name == "native" {
			t.Parallel()
		}

		assert := NewGomegaWithT(t)

		sandboxDriver := newSandboxDriver(t)

		sandbox, err := sandboxDriver.StartSandbox(context.Background(), orchestra.Task{
			ID:    gonanoid.Must(),
			Image: "busybox",
		})
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = sandbox.Cleanup(context.Background()) }()

		// First exec: write a file to the working directory.
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		status, err := sandbox.Exec(context.Background(),
			[]string{"sh", "-c", "echo hello-from-sandbox > /tmp/sandbox-test.txt"},
			nil, "", nil, stdout, stderr)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status.ExitCode()).To(Equal(0))

		// Second exec: read that file back.
		stdout.Reset()
		stderr.Reset()
		status, err = sandbox.Exec(context.Background(),
			[]string{"cat", "/tmp/sandbox-test.txt"},
			nil, "", nil, stdout, stderr)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status.ExitCode()).To(Equal(0))
		assert.Expect(stdout.String()).To(ContainSubstring("hello-from-sandbox"))
	})

	t.Run("exec respects env and workdir", func(t *testing.T) {
		if name == "native" {
			t.Parallel()
		}

		assert := NewGomegaWithT(t)

		sandboxDriver := newSandboxDriver(t)

		sandbox, err := sandboxDriver.StartSandbox(context.Background(), orchestra.Task{
			ID:    gonanoid.Must(),
			Image: "busybox",
		})
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = sandbox.Cleanup(context.Background()) }()

		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		status, err := sandbox.Exec(context.Background(),
			[]string{"sh", "-c", "echo $GREET && pwd"},
			map[string]string{"GREET": "hey-sandbox"},
			"/tmp",
			nil, stdout, stderr)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status.ExitCode()).To(Equal(0))
		assert.Expect(stdout.String()).To(ContainSubstring("hey-sandbox"))
		assert.Expect(stdout.String()).To(ContainSubstring("/tmp"))
	})

	t.Run("exec captures non-zero exit code", func(t *testing.T) {
		if name == "native" {
			t.Parallel()
		}

		assert := NewGomegaWithT(t)

		sandboxDriver := newSandboxDriver(t)

		sandbox, err := sandboxDriver.StartSandbox(context.Background(), orchestra.Task{
			ID:    gonanoid.Must(),
			Image: "busybox",
		})
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = sandbox.Cleanup(context.Background()) }()

		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		status, err := sandbox.Exec(context.Background(),
			[]string{"sh", "-c", "exit 42"},
			nil, "", nil, stdout, stderr)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(status.ExitCode()).To(Equal(42))
	})

	t.Run("cleanup removes sandbox", func(t *testing.T) {
		if name == "native" {
			t.Parallel()
		}

		assert := NewGomegaWithT(t)

		sandboxDriver := newSandboxDriver(t)

		sandbox, err := sandboxDriver.StartSandbox(context.Background(), orchestra.Task{
			ID:    gonanoid.Must(),
			Image: "busybox",
		})
		assert.Expect(err).NotTo(HaveOccurred())

		err = sandbox.Cleanup(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())
	})
}

func TestIsDriverAllowed(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Wildcard allows anything
	assert.Expect(orchestra.IsDriverAllowed("docker", []string{"*"})).NotTo(HaveOccurred())
	assert.Expect(orchestra.IsDriverAllowed("native", []string{"*"})).NotTo(HaveOccurred())

	// Specific driver in list
	assert.Expect(orchestra.IsDriverAllowed("docker", []string{"docker", "native"})).NotTo(HaveOccurred())
	assert.Expect(orchestra.IsDriverAllowed("native", []string{"docker", "native"})).NotTo(HaveOccurred())

	// Driver not in list
	assert.Expect(orchestra.IsDriverAllowed("fly", []string{"docker", "native"})).To(HaveOccurred())

	// Empty driver name
	assert.Expect(orchestra.IsDriverAllowed("", []string{"*"})).To(HaveOccurred())
}

// extractTarFiles reads a tar stream and returns a map of file path to contents.
func extractTarFiles(t *testing.T, rc io.ReadCloser) map[string]string {
	t.Helper()

	defer func() { _ = rc.Close() }()

	files := make(map[string]string)
	tr := tar.NewReader(rc)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			t.Fatalf("failed to read tar entry: %v", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		var buf strings.Builder

		if _, err := io.Copy(&buf, tr); err != nil {
			t.Fatalf("failed to read file %q from tar: %v", header.Name, err)
		}

		files[header.Name] = buf.String()
	}

	return files
}
