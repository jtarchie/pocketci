package storage_test

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
)

func TestAgentMemory(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			if df.name != "sqlite" {
				t.Skip("agent memory only supported on sqlite")
			}

			t.Run("save then recall returns the memory", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "mem-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				saved, deduped, err := client.SaveAgentMemory(
					ctx, pipeline.ID, "reviewer",
					"always run go fmt before building",
					[]string{"convention", "go"},
				)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(deduped).To(BeFalse())
				assert.Expect(saved.Content).To(Equal("always run go fmt before building"))
				assert.Expect(saved.Tags).To(ConsistOf("convention", "go"))
				assert.Expect(saved.RecallCount).To(BeNumerically("==", 0))

				memories, err := client.RecallAgentMemories(
					ctx, pipeline.ID, "reviewer", "go fmt", nil, 5,
				)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(memories).To(HaveLen(1))
				assert.Expect(memories[0].Content).To(Equal("always run go fmt before building"))
				assert.Expect(memories[0].RecallCount).To(BeNumerically("==", 1))
				assert.Expect(memories[0].LastRecalled).NotTo(BeNil())
			})

			t.Run("dedup on identical content", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "mem-dedup", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				first, deduped, err := client.SaveAgentMemory(
					ctx, pipeline.ID, "reviewer", "same lesson", nil,
				)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(deduped).To(BeFalse())

				second, deduped, err := client.SaveAgentMemory(
					ctx, pipeline.ID, "reviewer", "same lesson", nil,
				)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(deduped).To(BeTrue())
				assert.Expect(second.ID).To(Equal(first.ID))
			})

			t.Run("pipeline scoping isolates memories", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				p1, err := client.SavePipeline(ctx, "mem-p1", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())
				p2, err := client.SavePipeline(ctx, "mem-p2", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				_, _, err = client.SaveAgentMemory(ctx, p1.ID, "reviewer", "secret from p1", nil)
				assert.Expect(err).NotTo(HaveOccurred())

				memories, err := client.RecallAgentMemories(ctx, p2.ID, "reviewer", "secret", nil, 5)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(memories).To(BeEmpty())
			})

			t.Run("agent name scoping isolates memories", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "mem-agent-scope", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				_, _, err = client.SaveAgentMemory(ctx, pipeline.ID, "reviewer", "lesson", nil)
				assert.Expect(err).NotTo(HaveOccurred())

				memories, err := client.RecallAgentMemories(ctx, pipeline.ID, "other-agent", "lesson", nil, 5)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(memories).To(BeEmpty())
			})

			t.Run("empty query returns most recent", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "mem-recent", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				for _, c := range []string{"first", "second", "third"} {
					_, _, err := client.SaveAgentMemory(ctx, pipeline.ID, "r", c, nil)
					assert.Expect(err).NotTo(HaveOccurred())
				}

				memories, err := client.RecallAgentMemories(ctx, pipeline.ID, "r", "", nil, 2)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(memories).To(HaveLen(2))
				assert.Expect(memories[0].Content).To(Equal("third"))
				assert.Expect(memories[1].Content).To(Equal("second"))
			})

			t.Run("tag filter narrows results", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "mem-tags", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				_, _, err = client.SaveAgentMemory(ctx, pipeline.ID, "r", "go lesson", []string{"go"})
				assert.Expect(err).NotTo(HaveOccurred())
				_, _, err = client.SaveAgentMemory(ctx, pipeline.ID, "r", "rust lesson", []string{"rust"})
				assert.Expect(err).NotTo(HaveOccurred())

				memories, err := client.RecallAgentMemories(ctx, pipeline.ID, "r", "lesson", []string{"go"}, 5)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(memories).To(HaveLen(1))
				assert.Expect(memories[0].Content).To(Equal("go lesson"))
			})

			t.Run("cascade delete on pipeline removal", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "mem-cascade", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				_, _, err = client.SaveAgentMemory(ctx, pipeline.ID, "r", "will vanish", nil)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.DeletePipeline(ctx, pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Re-create same pipeline name to get a fresh ID.
				fresh, err := client.SavePipeline(ctx, "mem-cascade", "c", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				memories, err := client.RecallAgentMemories(ctx, fresh.ID, "r", "vanish", nil, 5)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(memories).To(BeEmpty())
			})

			t.Run("empty input validation", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				_, _, err := client.SaveAgentMemory(ctx, "", "r", "c", nil)
				assert.Expect(err).To(HaveOccurred())

				_, _, err = client.SaveAgentMemory(ctx, "p", "", "c", nil)
				assert.Expect(err).To(HaveOccurred())

				_, _, err = client.SaveAgentMemory(ctx, "p", "r", "", nil)
				assert.Expect(err).To(HaveOccurred())
			})
		})
	}
}
