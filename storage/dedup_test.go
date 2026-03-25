package storage_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

func TestWebhookDedup(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			if df.name != "sqlite" {
				t.Skip("webhook dedup only supported on sqlite")
			}
			t.Run("CheckWebhookDedup returns false for unseen key", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "dedup-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				found, err := client.CheckWebhookDedup(ctx, pipeline.ID, []byte("unseen-key-hash!"))
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeFalse())
			})

			t.Run("SaveWebhookDedup then CheckWebhookDedup returns true", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "dedup-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				keyHash := []byte("0123456789abcdef")
				err = client.SaveWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())

				found, err := client.CheckWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeTrue())
			})

			t.Run("SaveWebhookDedup is idempotent", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "dedup-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				keyHash := []byte("0123456789abcdef")
				err = client.SaveWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.SaveWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
			})

			t.Run("different keys are independent", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "dedup-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				key1 := []byte("aaaaaaaaaaaaaaaa")
				key2 := []byte("bbbbbbbbbbbbbbbb")

				err = client.SaveWebhookDedup(ctx, pipeline.ID, key1)
				assert.Expect(err).NotTo(HaveOccurred())

				found, err := client.CheckWebhookDedup(ctx, pipeline.ID, key1)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeTrue())

				found, err = client.CheckWebhookDedup(ctx, pipeline.ID, key2)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeFalse())
			})

			t.Run("different pipelines have independent dedup", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				p1, err := client.SavePipeline(ctx, "pipeline-1", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())
				p2, err := client.SavePipeline(ctx, "pipeline-2", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				keyHash := []byte("0123456789abcdef")
				err = client.SaveWebhookDedup(ctx, p1.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())

				found, err := client.CheckWebhookDedup(ctx, p1.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeTrue())

				found, err = client.CheckWebhookDedup(ctx, p2.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeFalse())
			})

			t.Run("PruneWebhookDedup removes old entries", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "dedup-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				keyHash := []byte("0123456789abcdef")
				err = client.SaveWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())

				// Prune with a future cutoff should remove it
				pruned, err := client.PruneWebhookDedup(ctx, time.Now().Add(time.Hour))
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(pruned).To(BeNumerically(">=", 1))

				found, err := client.CheckWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeFalse())
			})

			t.Run("PruneWebhookDedup keeps recent entries", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "dedup-test", "content", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				keyHash := []byte("0123456789abcdef")
				err = client.SaveWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())

				// Prune with a past cutoff should keep it
				pruned, err := client.PruneWebhookDedup(ctx, time.Now().Add(-time.Hour))
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(pruned).To(BeNumerically("==", 0))

				found, err := client.CheckWebhookDedup(ctx, pipeline.ID, keyHash)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(found).To(BeTrue())
			})
		})
	}
}
