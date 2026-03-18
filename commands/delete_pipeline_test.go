package commands_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/commands"
	"github.com/jtarchie/pocketci/server"
	. "github.com/onsi/gomega"
)

func TestDeletePipeline(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("deletes a pipeline by name", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			client, ts := newTestServer(t, server.RouterOptions{})

			_, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			cmd := commands.DeletePipeline{
				Name:      "my-pipeline",
				ServerURL: ts.URL,
			}

			err = cmd.Run(slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			result, err := client.SearchPipelines(context.Background(), "", 1, 100)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(result.Items).To(BeEmpty())
		})

		t.Run("deletes a pipeline by ID", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			client, ts := newTestServer(t, server.RouterOptions{})

			saved, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			cmd := commands.DeletePipeline{
				Name:      saved.ID,
				ServerURL: ts.URL,
			}

			err = cmd.Run(slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			result, err := client.SearchPipelines(context.Background(), "", 1, 100)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(result.Items).To(BeEmpty())
		})

		t.Run("returns error when pipeline not found", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			_, ts := newTestServer(t, server.RouterOptions{})

			cmd := commands.DeletePipeline{
				Name:      "non-existent",
				ServerURL: ts.URL,
			}

			err := cmd.Run(slog.Default())
			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("no pipeline found"))
		})

		t.Run("does not affect other pipelines when deleting by name", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			client, ts := newTestServer(t, server.RouterOptions{})

			_, err := client.SavePipeline(context.Background(), "to-delete", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())
			_, err = client.SavePipeline(context.Background(), "keep-me", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			cmd := commands.DeletePipeline{
				Name:      "to-delete",
				ServerURL: ts.URL,
			}

			err = cmd.Run(slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			result, err := client.SearchPipelines(context.Background(), "", 1, 100)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(result.Items).To(HaveLen(1))
			assert.Expect(result.Items[0].Name).To(Equal("keep-me"))
		})
	})
}
