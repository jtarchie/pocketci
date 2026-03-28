package commands_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/commands"
	"github.com/jtarchie/pocketci/server"
	. "github.com/onsi/gomega"
)

func TestTriggerPipeline(t *testing.T) {
	t.Parallel()

	t.Run("triggers a pipeline by name", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		client, ts := newTestServer(t, server.RouterOptions{})

		_, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		cmd := commands.TriggerPipeline{
			Name:         "my-pipeline",
			ServerConfig: commands.ServerConfig{ServerURL: ts.URL},
		}

		err = cmd.Run(slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("triggers a pipeline by ID", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		client, ts := newTestServer(t, server.RouterOptions{})

		saved, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		cmd := commands.TriggerPipeline{
			Name:         saved.ID,
			ServerConfig: commands.ServerConfig{ServerURL: ts.URL},
		}

		err = cmd.Run(slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("triggers with args", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		client, ts := newTestServer(t, server.RouterOptions{})

		_, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		cmd := commands.TriggerPipeline{
			Name:         "my-pipeline",
			ServerConfig: commands.ServerConfig{ServerURL: ts.URL},
			Args:         []string{"--env=staging", "--verbose"},
		}

		err = cmd.Run(slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("triggers with webhook simulation", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		client, ts := newTestServer(t, server.RouterOptions{
			AllowedFeatures: "webhooks",
		})

		_, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		cmd := commands.TriggerPipeline{
			ServerConfig:  commands.ServerConfig{ServerURL: ts.URL},
			Name:          "my-pipeline",
			WebhookBody:   `{"event": "push"}`,
			WebhookMethod: "POST",
			WebhookHeader: []string{"X-GitHub-Event=push"},
		}

		err = cmd.Run(slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("returns error for non-existent pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		cmd := commands.TriggerPipeline{
			Name:         "non-existent",
			ServerConfig: commands.ServerConfig{ServerURL: ts.URL},
		}

		err := cmd.Run(slog.Default())
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("no pipeline found"))
	})

	t.Run("returns error for paused pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		client, ts := newTestServer(t, server.RouterOptions{})

		saved, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.UpdatePipelinePaused(context.Background(), saved.ID, true)
		assert.Expect(err).NotTo(HaveOccurred())

		cmd := commands.TriggerPipeline{
			Name:         "my-pipeline",
			ServerConfig: commands.ServerConfig{ServerURL: ts.URL},
		}

		err = cmd.Run(slog.Default())
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("paused"))
	})
}
