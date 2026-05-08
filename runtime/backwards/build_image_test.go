package backwards_test

import (
	"testing"

	. "github.com/onsi/gomega"

	configpkg "github.com/jtarchie/pocketci/backwards"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
)

func TestBuildImageStepParsing(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cfg := loadConfig(t, "testdata/build_image.yml")

	g.Expect(cfg.Jobs).To(HaveLen(1))

	job := cfg.Jobs[0]
	g.Expect(job.Plan).To(HaveLen(2))

	taskStep := job.Plan[0]
	g.Expect(taskStep.Task).To(Equal("seed-context"))
	g.Expect(taskStep.BuildImage).To(BeNil())

	buildStep := job.Plan[1]
	g.Expect(buildStep.Task).To(BeEmpty())
	g.Expect(buildStep.BuildImage).NotTo(BeNil())

	bi := buildStep.BuildImage
	g.Expect(bi.Name).To(Equal("build-app"))
	g.Expect(bi.Context).To(Equal("source"))
	g.Expect(bi.Dockerfile).To(Equal("Dockerfile"))
	g.Expect(bi.Tag).To(Equal("registry.example.com/myapp:latest"))
	g.Expect(bi.Push).To(BeFalse())
	g.Expect(bi.BuildArgs).To(HaveKeyWithValue("EXTRA_TAG", "dev"))
	g.Expect(bi.Inputs).To(HaveLen(1))
	g.Expect(bi.Inputs[0].Name).To(Equal("source"))
	g.Expect(bi.Caches).To(HaveLen(1))
	g.Expect(bi.Caches[0].Path).To(Equal("/var/lib/buildkit"))
	g.Expect(bi.Registry).NotTo(BeNil())
	g.Expect(bi.Registry.Hostname).To(Equal("registry.example.com"))
	g.Expect(bi.Registry.Username).To(Equal("((registry_user))"))
	g.Expect(bi.Registry.Password).To(Equal("((registry_password))"))
	g.Expect(bi.Registry.Insecure).To(BeFalse())
}

// TestIdentifyBuildImageStep verifies the discriminator wiring in job.go
// recognizes a build_image step. We touch this through a parsed pipeline
// rather than calling the unexported identifyStepType directly.
func TestIdentifyBuildImageStep(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	cfg := loadConfig(t, "testdata/build_image.yml")

	step := cfg.Jobs[0].Plan[1]
	g.Expect(backwards.StepKind(&step)).To(Equal("build_image"))
	g.Expect(backwards.StepStorageID(&step)).To(Equal("build_image/build-app"))
}

// TestStepStorageIDFallback verifies that when no Name is provided, the
// step storage identifier falls back to a sanitized form of Tag.
func TestBuildImageStorageIDFallback(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	step := configpkg.Step{
		BuildImage: &configpkg.BuildImageConfig{
			Tag: "registry.example.com/foo:latest",
		},
	}

	g.Expect(backwards.StepStorageID(&step)).To(Equal("build_image/registry-example-com-foo-latest"))
}
