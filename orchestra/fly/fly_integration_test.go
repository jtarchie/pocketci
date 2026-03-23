package fly_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	gonanoid "github.com/matoous/go-nanoid/v2"
	. "github.com/onsi/gomega"
	flygo "github.com/superfly/fly-go"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/fly"
)

// TestFlyCleanup_LaunchErrorRecoveryTracksExistingMachine verifies that when
// RunContainer's Launch call fails because a machine with the same name already
// exists (idempotency / retry scenario), the recovered machine is added to
// machineIDs so that Close() can destroy it.
//
// This is a regression test for the bug where the recovery path in RunContainer
// returned the existing machine without calling trackMachine, leaving it
// orphaned after driver shutdown.
func TestFlyCleanup_LaunchErrorRecoveryTracksExistingMachine(t *testing.T) {
	token := os.Getenv("FLY_API_TOKEN")
	if token == "" {
		t.Skip("FLY_API_TOKEN not set, skipping Fly integration tests")
	}

	assert := NewGomegaWithT(t)

	namespace := "test-" + gonanoid.Must()
	taskID := gonanoid.Must()

	driver, err := fly.New(context.Background(), fly.Config{ServerConfig: fly.ServerConfig{Token: token}, Namespace: namespace}, slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())

	f := driver.(*fly.Fly)

	defer func() { _ = driver.Close() }()

	// Derive the exact machine name that RunContainer will use for this taskID.
	machineName := fly.SanitizeAppName(fmt.Sprintf("%s-%s", namespace, taskID))

	// Pre-create the machine directly via the low-level client, bypassing
	// trackMachine. This simulates a machine that already exists from a prior
	// partial or failed launch (e.g. "insufficient resources available" retry).
	existingMachine, err := f.Client().Launch(context.Background(), f.AppName(), flygo.LaunchMachineInput{
		Config: &flygo.MachineConfig{
			Image: "alpine:latest",
			Init:  flygo.MachineInit{Exec: []string{"/bin/sleep", "60"}},
			Metadata: map[string]string{
				"orchestra.namespace": namespace,
				"orchestra.task":      taskID,
			},
		},
		Name: machineName,
	})
	assert.Expect(err).NotTo(HaveOccurred())

	existingMachineID := existingMachine.ID

	// The machine was NOT registered through RunContainer so it must be absent
	// from machineIDs at this point.
	assert.Expect(f.IsTrackedMachine(existingMachineID)).To(BeFalse())

	// RunContainer with the same taskID triggers a second Launch attempt.
	// Fly returns an error because the machine name is already taken; the
	// recovery path finds the existing machine and — after our fix — calls
	// trackMachine so that Close() will destroy it.
	container, err := f.RunContainer(context.Background(), orchestra.Task{
		ID:      taskID,
		Image:   "alpine:latest",
		Command: []string{"/bin/sleep", "60"},
	})
	assert.Expect(err).NotTo(HaveOccurred())

	// The returned container must wrap the pre-existing machine, not a new one.
	assert.Expect(container.ID()).To(Equal(existingMachineID))

	// The recovered machine must now be tracked so that Close() cleans it up.
	assert.Expect(f.IsTrackedMachine(existingMachineID)).To(BeTrue())
}

// TestFlyCleanup_SweepDestroysUntrackedNamespaceMachines verifies that Close()
// destroys machines belonging to the namespace even when they were never added
// to machineIDs (e.g. a crash happened between Launch and trackMachine).
//
// This is a regression test for the bug where Close() only iterated over
// machineIDs and ignored machines that had the correct namespace metadata but
// were never explicitly tracked.
func TestFlyCleanup_SweepDestroysUntrackedNamespaceMachines(t *testing.T) {
	token := os.Getenv("FLY_API_TOKEN")
	if token == "" {
		t.Skip("FLY_API_TOKEN not set, skipping Fly integration tests")
	}

	assert := NewGomegaWithT(t)

	namespace := "test-" + gonanoid.Must()

	driver, err := fly.New(context.Background(), fly.Config{ServerConfig: fly.ServerConfig{Token: token}, Namespace: namespace}, slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())

	f := driver.(*fly.Fly)

	// Launch a machine directly without going through RunContainer, so
	// trackMachine is never called. This is the "orphaned machine" scenario.
	machineName := fly.SanitizeAppName(namespace + "-orphaned")

	orphan, err := f.Client().Launch(context.Background(), f.AppName(), flygo.LaunchMachineInput{
		Config: &flygo.MachineConfig{
			Image: "alpine:latest",
			Init:  flygo.MachineInit{Exec: []string{"/bin/sleep", "60"}},
			Metadata: map[string]string{
				"orchestra.namespace": namespace,
			},
		},
		Name: machineName,
	})
	assert.Expect(err).NotTo(HaveOccurred())

	orphanID := orphan.ID

	// Confirm the machine is not tracked.
	assert.Expect(f.IsTrackedMachine(orphanID)).To(BeFalse())

	// Close must not error even though the machine was never tracked.
	// The namespace sweep in Close() locates and destroys it.
	err = driver.Close()
	assert.Expect(err).NotTo(HaveOccurred())

	// The ephemeral app is deleted by Close(), so we cannot query the machine
	// state afterward. A successful Close() without error confirms the sweep ran.
}
