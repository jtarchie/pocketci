package runner_test

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/testhelpers"
)

// TestCacheOpRoundTrip is the end-to-end smoke test: it persists a
// synthetic volume to MinIO via CachePersist, then restores it into a
// fresh volume via CacheRestore, and checks that the contents survived
// the round-trip byte-for-byte.
//
// Skipped when docker or minio aren't on PATH.
func TestCacheOpRoundTrip(t *testing.T) {
	_, dockerErr := exec.LookPath("docker")
	if dockerErr != nil {
		t.Skip("docker not installed; skipping integration test")
	}

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mc := testhelpers.StartDockerMinIO(t)

	driver, err := docker.New(ctx, docker.Config{Namespace: "pocketci-cacheop-test"}, slog.Default())
	g.Expect(err).NotTo(HaveOccurred())

	t.Cleanup(func() { _ = driver.Close() })

	store := newIntegrationStorage(t)

	pr := runner.NewPipelineRunner(ctx, driver, store, slog.Default(), "cache-op-test", "test-run-1")
	t.Cleanup(func() { _ = pr.CleanupVolumes() })

	// Step 1: create a "source" volume and seed it with a fixed payload via
	// a regular task. The cache op script will tar this directory to S3.
	srcVol, err := pr.CreateVolume(runner.VolumeInput{Name: "cachetest-src"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(srcVol).NotTo(BeNil())

	const expected = "the cache survived the round trip"

	seed := runner.RunInput{
		Name:  "seed",
		Image: "busybox:latest",
		Mounts: map[string]runner.VolumeResult{
			"cachetest-src": *srcVol,
		},
	}
	seed.Command.Path = "sh"
	seed.Command.Args = []string{"-c", fmt.Sprintf(
		"mkdir -p cachetest-src/sub && printf %q > cachetest-src/sub/payload.txt && ls -la cachetest-src/sub/",
		expected,
	)}

	seedResult, err := pr.Run(seed)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(seedResult.Code).To(Equal(0), "seed: stdout=%s stderr=%s", seedResult.Stdout, seedResult.Stderr)

	// Step 2: persist the volume to MinIO via amazon/aws-cli.
	persistInput := runner.CacheOpInput{
		Name:            "cachetest-src",
		Volume:          *srcVol,
		Endpoint:        mc.Endpoint(),
		Bucket:          mc.Bucket(),
		Key:             "cachetest/src.tar.gz",
		AccessKeyID:     mc.AccessKeyID(),
		SecretAccessKey: mc.SecretAccessKey(),
		Region:          "us-east-1",
	}

	persistResult, err := runner.CachePersist(pr, persistInput)
	g.Expect(err).NotTo(HaveOccurred(),
		"persist failed; stdout=%s stderr=%s",
		debugRunStdout(persistResult), debugRunStderr2(persistResult))
	g.Expect(persistResult).NotTo(BeNil())
	g.Expect(persistResult.Code).To(Equal(0))
	g.Expect(persistResult.Stdout + persistResult.Stderr).To(ContainSubstring("persist complete"))

	// Step 3: create a fresh volume and restore into it.
	restoreVol, err := pr.CreateVolume(runner.VolumeInput{Name: "cachetest-dst"})
	g.Expect(err).NotTo(HaveOccurred())

	restoreInput := persistInput
	restoreInput.Volume = *restoreVol

	restoreResult, err := runner.CacheRestore(pr, restoreInput)
	g.Expect(err).NotTo(HaveOccurred(),
		"restore failed; stdout=%s stderr=%s",
		debugRunStdout(restoreResult), debugRunStderr2(restoreResult))
	g.Expect(restoreResult.Code).To(Equal(0))
	g.Expect(restoreResult.Stdout + restoreResult.Stderr).To(ContainSubstring("restore complete"))

	// Step 4: read back the payload via a regular task and check it.
	verify := runner.RunInput{
		Name:  "verify",
		Image: "busybox:latest",
		Mounts: map[string]runner.VolumeResult{
			"cachetest-dst": *restoreVol,
		},
	}
	verify.Command.Path = "sh"
	verify.Command.Args = []string{"-c", "cat cachetest-dst/sub/payload.txt"}

	verifyResult, err := pr.Run(verify)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(verifyResult.Code).To(Equal(0), "verify: stdout=%s stderr=%s", verifyResult.Stdout, verifyResult.Stderr)
	g.Expect(strings.TrimSpace(verifyResult.Stdout)).To(Equal(expected))
}

// TestCacheOpRestoreMiss verifies that a restore against a nonexistent S3
// key exits cleanly (downstream tasks see an empty cache directory) rather
// than failing the task.
func TestCacheOpRestoreMiss(t *testing.T) {
	_, dockerErr := exec.LookPath("docker")
	if dockerErr != nil {
		t.Skip("docker not installed; skipping integration test")
	}

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mc := testhelpers.StartDockerMinIO(t)

	driver, err := docker.New(ctx, docker.Config{Namespace: "pocketci-cacheop-miss"}, slog.Default())
	g.Expect(err).NotTo(HaveOccurred())

	t.Cleanup(func() { _ = driver.Close() })

	store := newIntegrationStorage(t)

	pr := runner.NewPipelineRunner(ctx, driver, store, slog.Default(), "cache-op-miss", "test-run-1")
	t.Cleanup(func() { _ = pr.CleanupVolumes() })

	vol, err := pr.CreateVolume(runner.VolumeInput{Name: "cachetest-empty"})
	g.Expect(err).NotTo(HaveOccurred())

	result, err := runner.CacheRestore(pr, runner.CacheOpInput{
		Name:            "cachetest-empty",
		Volume:          *vol,
		Endpoint:        mc.Endpoint(),
		Bucket:          mc.Bucket(),
		Key:             "definitely-not-there.tar.gz",
		AccessKeyID:     mc.AccessKeyID(),
		SecretAccessKey: mc.SecretAccessKey(),
		Region:          "us-east-1",
	})
	g.Expect(err).NotTo(HaveOccurred(),
		"miss should not error; stdout=%s stderr=%s",
		debugRunStdout(result), debugRunStderr2(result))
	g.Expect(result.Code).To(Equal(0))
	g.Expect(result.Stdout + result.Stderr).To(ContainSubstring("miss (no prior data)"))
}

func debugRunStdout(r *runner.RunResult) string {
	if r == nil {
		return ""
	}

	return r.Stdout
}

func debugRunStderr2(r *runner.RunResult) string {
	if r == nil {
		return ""
	}

	return r.Stderr
}
