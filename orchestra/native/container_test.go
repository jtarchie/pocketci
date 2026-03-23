package native_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/native"
	. "github.com/onsi/gomega"
)

func TestNativeLogsAreSafeWhileProcessIsRunning(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	driver, err := native.New(context.Background(), native.Config{Namespace: "native-race-test"}, slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = driver.Close() }()

	container, err := driver.RunContainer(
		context.Background(),
		orchestra.Task{
			ID:      "native-log-race",
			Command: []string{"sh", "-c", "for i in 1 2 3 4 5; do echo line$i; sleep 0.05; done"},
		},
	)
	assert.Expect(err).NotTo(HaveOccurred())

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	streamStdout, streamStderr := &strings.Builder{}, &strings.Builder{}
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- container.Logs(streamCtx, streamStdout, streamStderr, true)
	}()

	var snapshotWG sync.WaitGroup
	for range 8 {
		snapshotWG.Add(1)

		go func() {
			defer snapshotWG.Done()

			deadline := time.Now().Add(350 * time.Millisecond)
			for time.Now().Before(deadline) {
				stdout, stderr := &strings.Builder{}, &strings.Builder{}
				_ = container.Logs(context.Background(), stdout, stderr, false)
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	assert.Eventually(func() bool {
		status, err := container.Status(context.Background())
		assert.Expect(err).NotTo(HaveOccurred())

		return status.IsDone() && status.ExitCode() == 0
	}, "5s", "20ms").Should(BeTrue())

	snapshotWG.Wait()
	cancelStream()

	select {
	case err := <-streamDone:
		assert.Expect(err).NotTo(HaveOccurred())
	case <-time.After(5 * time.Second):
		t.Fatal("stream logs did not finish in time")
	}

	assert.Expect(streamStderr.String()).To(BeEmpty())
	assert.Expect(streamStdout.String()).To(ContainSubstring("line1"))
	assert.Expect(streamStdout.String()).To(ContainSubstring("line5"))

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	err = container.Logs(context.Background(), stdout, stderr, false)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(stderr.String()).To(BeEmpty())
	assert.Expect(stdout.String()).To(ContainSubstring("line1"))
	assert.Expect(stdout.String()).To(ContainSubstring("line5"))
}
