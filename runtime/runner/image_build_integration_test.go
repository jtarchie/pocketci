package runner_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/jtarchie/pocketci/testhelpers"
)

func newIntegrationStorage(t *testing.T) storage.Driver {
	t.Helper()

	g := NewWithT(t)

	st, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "", slog.Default())
	g.Expect(err).NotTo(HaveOccurred())

	t.Cleanup(func() { _ = st.Close() })

	return st
}

// TestBuildImagePushPull is the end-to-end smoke test: it builds a tiny image
// inside a moby/buildkit container, pushes it to a local registry:2 container,
// and verifies the manifest landed via the registry's HTTP API.
func TestBuildImagePushPull(t *testing.T) {
	_, dockerErr := exec.LookPath("docker")
	if dockerErr != nil {
		t.Skip("docker not installed; skipping integration test")
	}

	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := testhelpers.StartRegistry(t)

	driver, err := docker.New(ctx, docker.Config{Namespace: "pocketci-buildimage-test"}, slog.Default())
	g.Expect(err).NotTo(HaveOccurred())

	t.Cleanup(func() { _ = driver.Close() })

	store := newIntegrationStorage(t)

	pr := runner.NewPipelineRunner(ctx, driver, store, slog.Default(), "build-image-test", "test-run-1")
	t.Cleanup(func() { _ = pr.CleanupVolumes() })

	// Step 1: create an input volume named "source" and seed it with a Dockerfile.
	srcVol, err := pr.CreateVolume(runner.VolumeInput{Name: "source"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(srcVol).NotTo(BeNil())

	dockerfile := "FROM busybox\nRUN echo pocketci-built > /built.txt\n"

	setup := runner.RunInput{
		Name:  "seed-context",
		Image: "busybox:latest",
		Mounts: map[string]runner.VolumeResult{
			"source": *srcVol,
		},
	}
	setup.Command.Path = "sh"
	setup.Command.Args = []string{"-c", fmt.Sprintf("printf %q > source/Dockerfile && ls -la source/", dockerfile)}

	setupResult, err := pr.Run(setup)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(setupResult.Code).To(Equal(0), "seed-context: %s\n%s", setupResult.Stdout, setupResult.Stderr)

	// Step 2: build & push to the local registry.
	tag := fmt.Sprintf("%s/pocketci/built:%s", reg.Endpoint(), "v1")

	buildResult, err := runner.BuildImage(pr, runner.BuildImageInput{
		Name:       "build-app",
		Context:    "source",
		Dockerfile: "Dockerfile",
		Tag:        tag,
		Push:       true,
		Inputs: map[string]runner.VolumeResult{
			"source": *srcVol,
		},
		RegistryAuth: &runner.BuildImageRegistryAuth{
			Insecure: true,
		},
	})
	g.Expect(err).NotTo(HaveOccurred(),
		"build failed; stdout=%s stderr=%s",
		debugRunOutput(buildResult), debugRunStderr(buildResult))
	g.Expect(buildResult).NotTo(BeNil())
	g.Expect(buildResult.Ref).To(Equal(tag))
	g.Expect(buildResult.Digest).To(HavePrefix("sha256:"),
		"expected digest, got stdout=%s", buildResult.RunResult.Stdout)

	// Step 3: verify the registry has the manifest.
	manifestURL := reg.HostEndpoint() + "/v2/pocketci/built/manifests/v1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	g.Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")

	resp, err := http.DefaultClient.Do(req)
	g.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	g.Expect(resp.StatusCode).To(Equal(http.StatusOK), "manifest should be accessible at %s", manifestURL)
}

func debugRunOutput(r *runner.BuildImageResult) string {
	if r == nil || r.RunResult == nil {
		return ""
	}

	return truncate(r.RunResult.Stdout, 2000)
}

func debugRunStderr(r *runner.BuildImageResult) string {
	if r == nil || r.RunResult == nil {
		return ""
	}

	return truncate(r.RunResult.Stderr, 2000)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "…(truncated)"
}
