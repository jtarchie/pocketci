package backwards_test

import (
	"testing"

	"github.com/jtarchie/pocketci/backwards"
	. "github.com/onsi/gomega"
)

func TestPreprocessYAML(t *testing.T) {
	t.Parallel()

	t.Run("non-opt-in YAML is returned unchanged", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		yaml := `---
jobs:
  - name: test
    plan:
      - task: sample
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: echo
            args: ["hello"]`

		err := backwards.ValidatePipeline([]byte(yaml))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("opt-in YAML with Sprig templates renders successfully", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Use Sprig's upper function to render a template.
		yaml := `# pocketci: template
---
jobs:
  - name: {{ upper "test" }}
    plan:
      - task: sample
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: echo
            args: ["hello"]`

		err := backwards.ValidatePipeline([]byte(yaml))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("opt-in with invalid template syntax fails validation", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		yaml := `# pocketci: template
---
jobs:
  - name: {{ unclosed template
    plan:
      - task: sample
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: echo`

		err := backwards.ValidatePipeline([]byte(yaml))
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("pipeline template parse failed"))
	})

	t.Run("opt-in with undefined template function fails validation", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		yaml := `# pocketci: template
---
jobs:
  - name: {{ undefinedFunc "value" }}
    plan:
      - task: sample
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: echo`

		err := backwards.ValidatePipeline([]byte(yaml))
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("pipeline template parse failed"))
	})

	t.Run("template opt-in marker renders correctly before parsing", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		yaml := `# pocketci: template
---
jobs:
  - name: {{ lower "TEST_JOB" }}
    plan:
      - task: sample
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: echo
            args:
              - '{{ upper "hello" }}'`

		// ValidatePipeline applies the template and parses — verifies rendering works
		err := backwards.ValidatePipeline([]byte(yaml))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("marker anywhere in first line activates templating", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Marker is present but not first token
		yaml := `# This is a pipeline with pocketci: template enabled
---
jobs:
  - name: {{ upper "sample" }}
    plan:
      - task: example
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: true`

		err := backwards.ValidatePipeline([]byte(yaml))
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("marker not on first line does not activate templating", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		// Marker is present but on second line — should not be treated as opt-in
		yaml := `---
# pocketci: template
jobs:
  - name: {{ upper "broken" }}
    plan: []`

		err := backwards.ValidatePipeline([]byte(yaml))
		// Should fail because {{ upper "broken" }} is not valid YAML when not templated
		assert.Expect(err).To(HaveOccurred())
	})
}
