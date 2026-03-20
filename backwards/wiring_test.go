package backwards_test

import (
	"testing"

	"github.com/jtarchie/pocketci/backwards"
	. "github.com/onsi/gomega"
)

func TestValidateInputOutputWiring(t *testing.T) {
	t.Parallel()

	t.Run("task input satisfied by prior task output", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
jobs:
  - name: wiring-test
    plan:
      - task: build
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          outputs:
            - name: artifacts
          run:
            path: echo
            args: ["hello"]
      - task: deploy
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: artifacts
          run:
            path: echo
            args: ["deploy"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("task input not satisfied by any prior step", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
jobs:
  - name: wiring-test
    plan:
      - task: build
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          outputs:
            - name: artifacts
          run:
            path: echo
            args: ["hello"]
      - task: deploy
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: wrong-name
          run:
            path: echo
            args: ["deploy"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring(`input "wrong-name"`))
		assert.Expect(err.Error()).To(ContainSubstring(`step "deploy"`))
	})

	t.Run("get step output satisfies downstream input", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
resources:
  - name: my-repo
    type: registry-image
    source: { repository: alpine }
jobs:
  - name: wiring-test
    plan:
      - get: my-repo
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: my-repo
          run:
            path: echo
            args: ["test"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("agent auto-output satisfies downstream input", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
jobs:
  - name: wiring-test
    plan:
      - task: checkout
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          outputs:
            - name: repo
          run:
            path: echo
            args: ["checkout"]
      - agent: final-review
        prompt: Review the code
        model: test-model
        config:
          platform: linux
          image: alpine
      - task: post-comment
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: final-review
          run:
            path: echo
            args: ["post"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("agent auto-output name mismatch caught", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
jobs:
  - name: wiring-test
    plan:
      - agent: pr-reviewer
        prompt: Review the code
        model: test-model
        config:
          platform: linux
          image: alpine
      - task: post-comment
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: final-review
          run:
            path: echo
            args: ["post"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring(`input "final-review"`))
		assert.Expect(err.Error()).To(ContainSubstring(`step "post-comment"`))
	})

	t.Run("agent with explicit outputs", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
jobs:
  - name: wiring-test
    plan:
      - agent: my-agent
        prompt: Review the code
        model: test-model
        config:
          platform: linux
          image: alpine
          outputs:
            - name: review-output
      - task: use-output
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: review-output
          run:
            path: echo
            args: ["done"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("file-only step skipped for validation", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
jobs:
  - name: wiring-test
    plan:
      - task: checkout
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          outputs:
            - name: repo
          run:
            path: echo
            args: ["checkout"]
      - task: loaded-task
        file: repo/path/to/task.yml`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("put step output satisfies downstream input", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		content := `---
resources:
  - name: my-artifact
    type: registry-image
    source: { repository: alpine }
jobs:
  - name: wiring-test
    plan:
      - put: my-artifact
      - task: use-artifact
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: my-artifact
          run:
            path: echo
            args: ["done"]`

		err := backwards.ValidatePipeline([]byte(content))
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
