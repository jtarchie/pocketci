package client_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/client"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

// newTestServer spins up a real server.Router backed by a temporary SQLite
// database and returns the storage driver and a started httptest.Server.
func newTestServer(t *testing.T, opts server.RouterOptions) (storage.Driver, *httptest.Server) {
	t.Helper()
	assert := NewGomegaWithT(t)

	buildFile, err := os.CreateTemp(t.TempDir(), "*.db")
	assert.Expect(err).NotTo(HaveOccurred())
	_ = buildFile.Close()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "test", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = store.Close() })

	if opts.SecretsManager == nil {
		secretsManager, secretsErr := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
		assert.Expect(secretsErr).NotTo(HaveOccurred())
		t.Cleanup(func() { _ = secretsManager.Close() })
		opts.SecretsManager = secretsManager
	}

	router, err := server.NewRouter(slog.Default(), store, opts)
	assert.Expect(err).NotTo(HaveOccurred())

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	return store, ts
}

func TestIntegrationListPipelines(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})
		c := client.New(ts.URL)

		result, err := c.ListPipelines()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(BeEmpty())
	})

	t.Run("with data", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		_, err := store.SavePipeline(context.Background(), "alpha", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())
		_, err = store.SavePipeline(context.Background(), "beta", "content", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		result, err := c.ListPipelines()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(HaveLen(2))
	})
}

func TestIntegrationFindPipelineByNameOrID(t *testing.T) {
	t.Parallel()

	t.Run("by name", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		_, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		p, err := c.FindPipelineByNameOrID("my-pipeline")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(p.Name).To(Equal("my-pipeline"))
	})

	t.Run("by ID", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		p, err := c.FindPipelineByNameOrID(saved.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(p.ID).To(Equal(saved.ID))
		assert.Expect(p.Name).To(Equal("my-pipeline"))
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		_, err := c.FindPipelineByNameOrID("nope")
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("no pipeline found"))
	})
}

func TestIntegrationSetPipeline(t *testing.T) {
	t.Parallel()

	t.Run("creates a new pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		p, err := c.SetPipeline("my-pipeline", client.SetPipelineRequest{
			Content:     "const pipeline = async () => {}; export { pipeline };",
			ContentType: "js",
			Driver:      "docker",
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(p.Name).To(Equal("my-pipeline"))
		assert.Expect(p.ID).NotTo(BeEmpty())

		result, err := store.SearchPipelines(context.Background(), "", 1, 100)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(HaveLen(1))
		assert.Expect(result.Items[0].Name).To(Equal("my-pipeline"))
	})

	t.Run("idempotent update preserves ID", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		existing, err := store.SavePipeline(context.Background(), "my-pipeline", "old content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		p, err := c.SetPipeline("my-pipeline", client.SetPipelineRequest{
			Content:     "const pipeline = async () => {}; export { pipeline };",
			ContentType: "js",
			Driver:      "native",
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(p.ID).To(Equal(existing.ID))

		result, err := store.SearchPipelines(context.Background(), "", 1, 100)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(HaveLen(1))
	})
}

func TestIntegrationDeletePipeline(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		err = c.DeletePipeline(saved.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		result, err := store.SearchPipelines(context.Background(), "", 1, 100)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(BeEmpty())
	})

	t.Run("returns error for non-existent pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		err := c.DeletePipeline("non-existent-id")
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestIntegrationTriggerPipeline(t *testing.T) {
	t.Parallel()

	t.Run("triggers a pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		result, err := c.TriggerPipeline(saved.ID, client.TriggerRequest{})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.RunID).NotTo(BeEmpty())
	})

	t.Run("returns error for paused pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		paused := true
		err = store.UpdatePipeline(context.Background(), saved.ID, storage.PipelineUpdate{Paused: &paused})
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		_, err = c.TriggerPipeline(saved.ID, client.TriggerRequest{})
		assert.Expect(err).To(HaveOccurred())

		var pausedErr *client.PipelinePausedError
		assert.Expect(errors.As(err, &pausedErr)).To(BeTrue())
	})

	t.Run("returns error for non-existent pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		_, err := c.TriggerPipeline("non-existent-id", client.TriggerRequest{})
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestIntegrationPausePipeline(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	store, ts := newTestServer(t, server.RouterOptions{})

	saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
	assert.Expect(err).NotTo(HaveOccurred())

	c := client.New(ts.URL)
	err = c.PausePipeline(saved.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	p, err := store.GetPipeline(context.Background(), saved.ID)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Paused).To(BeTrue())
}

func TestIntegrationUnpausePipeline(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	store, ts := newTestServer(t, server.RouterOptions{})

	saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
	assert.Expect(err).NotTo(HaveOccurred())

	paused := true
	err = store.UpdatePipeline(context.Background(), saved.ID, storage.PipelineUpdate{Paused: &paused})
	assert.Expect(err).NotTo(HaveOccurred())

	c := client.New(ts.URL)
	err = c.UnpausePipeline(saved.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	p, err := store.GetPipeline(context.Background(), saved.ID)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Paused).To(BeFalse())
}

func TestIntegrationSeedJobPassed(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	store, ts := newTestServer(t, server.RouterOptions{})

	saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
	assert.Expect(err).NotTo(HaveOccurred())

	c := client.New(ts.URL)
	result, err := c.SeedJobPassed(saved.ID, "my-job")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result.Job).To(Equal("my-job"))
	assert.Expect(result.RunID).NotTo(BeEmpty())
}

func TestIntegrationGetPipeline(t *testing.T) {
	t.Parallel()

	t.Run("returns pipeline by ID", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		result, err := c.GetPipeline(saved.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.ID).To(Equal(saved.ID))
		assert.Expect(result.Name).To(Equal("my-pipeline"))
	})

	t.Run("returns not found error", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		_, err := c.GetPipeline("non-existent")
		assert.Expect(err).To(HaveOccurred())

		var notFoundErr *client.NotFoundError
		assert.Expect(errors.As(err, &notFoundErr)).To(BeTrue())
	})
}

func TestIntegrationGetRunStatus(t *testing.T) {
	t.Parallel()

	t.Run("returns run status", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := store.SaveRun(context.Background(), saved.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		result, err := c.GetRunStatus(run.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.ID).To(Equal(run.ID))
	})

	t.Run("returns not found error", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		_, err := c.GetRunStatus("non-existent")
		assert.Expect(err).To(HaveOccurred())

		var notFoundErr *client.NotFoundError
		assert.Expect(errors.As(err, &notFoundErr)).To(BeTrue())
	})
}

func TestIntegrationGetRunTasks(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	store, ts := newTestServer(t, server.RouterOptions{})

	saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(context.Background(), saved.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
	assert.Expect(err).NotTo(HaveOccurred())

	c := client.New(ts.URL)
	result, err := c.GetRunTasks(run.ID)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())
}

func TestIntegrationStopRun(t *testing.T) {
	t.Parallel()

	t.Run("returns not found error for unknown run", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{})

		c := client.New(ts.URL)
		_, err := c.StopRun("non-existent")
		assert.Expect(err).To(HaveOccurred())

		var notFoundErr *client.NotFoundError
		assert.Expect(errors.As(err, &notFoundErr)).To(BeTrue())
	})

	t.Run("returns error when run is already completed", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, ts := newTestServer(t, server.RouterOptions{})

		saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := store.SaveRun(context.Background(), saved.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())

		err = store.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
		assert.Expect(err).NotTo(HaveOccurred())

		c := client.New(ts.URL)
		_, err = c.StopRun(run.ID)
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestIntegrationListGates(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	store, ts := newTestServer(t, server.RouterOptions{
		AllowedFeatures: "gates",
	})

	saved, err := store.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(context.Background(), saved.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
	assert.Expect(err).NotTo(HaveOccurred())

	c := client.New(ts.URL)
	result, err := c.ListGates(run.ID)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())
}

func TestIntegrationListDrivers(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	_, ts := newTestServer(t, server.RouterOptions{})

	c := client.New(ts.URL)
	result, err := c.ListDrivers()
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())
}

func TestIntegrationListFeatures(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	_, ts := newTestServer(t, server.RouterOptions{})

	c := client.New(ts.URL)
	result, err := c.ListFeatures()
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())
}

func TestIntegrationBasicAuth(t *testing.T) {
	t.Parallel()

	t.Run("succeeds with correct credentials", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{
			BasicAuthUsername: "admin",
			BasicAuthPassword: "secret",
		})

		c := client.New(ts.URL, client.WithBasicAuth("admin", "secret"))
		result, err := c.ListPipelines()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(BeEmpty())
	})

	t.Run("returns AuthRequiredError without credentials", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{
			BasicAuthUsername: "admin",
			BasicAuthPassword: "secret",
		})

		c := client.New(ts.URL)
		_, err := c.ListPipelines()
		assert.Expect(err).To(HaveOccurred())

		var authErr *client.AuthRequiredError
		assert.Expect(errors.As(err, &authErr)).To(BeTrue())
	})

	t.Run("credentials embedded in URL", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, ts := newTestServer(t, server.RouterOptions{
			BasicAuthUsername: "admin",
			BasicAuthPassword: "secret",
		})

		serverURL := "http://admin:secret@" + ts.Listener.Addr().String()
		c := client.New(serverURL)
		result, err := c.ListPipelines()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.Items).To(BeEmpty())
	})
}
