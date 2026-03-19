package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestResolveDriverFactory(t *testing.T) {
	t.Parallel()

	t.Run("pipeline with no driver uses DefaultDriver and server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "no-driver", "export const pipeline = async () => {};", "", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:   5,
			DefaultDriver: "native",
			DriverConfigs: map[string]orchestra.DriverConfig{
				"native": native.ServerConfig{},
				"docker": docker.ServerConfig{Host: "tcp://remote:2376"},
			},
		})

		execService := router.ExecutionService()
		run, err := execService.TriggerPipeline(context.Background(), pipeline)
		assert.Expect(err).NotTo(HaveOccurred())
		execService.Wait()

		finalRun, err := client.GetRun(context.Background(), run.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
	})

	t.Run("pipeline with explicit driver uses matching server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "explicit-native", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:   5,
			DefaultDriver: "docker",
			DriverConfigs: map[string]orchestra.DriverConfig{
				"native": native.ServerConfig{},
				"docker": docker.ServerConfig{},
			},
		})

		execService := router.ExecutionService()
		run, err := execService.TriggerPipeline(context.Background(), pipeline)
		assert.Expect(err).NotTo(HaveOccurred())
		execService.Wait()

		finalRun, err := client.GetRun(context.Background(), run.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
	})

	t.Run("pipeline with driver not in server configs falls back to empty config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		// native works with empty config since it needs nothing
		pipeline, err := client.SavePipeline(context.Background(), "unconfigured-driver", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:   5,
			DefaultDriver: "docker",
			DriverConfigs: map[string]orchestra.DriverConfig{
				"docker": docker.ServerConfig{},
			},
		})

		execService := router.ExecutionService()
		run, err := execService.TriggerPipeline(context.Background(), pipeline)
		assert.Expect(err).NotTo(HaveOccurred())
		execService.Wait()

		finalRun, err := client.GetRun(context.Background(), run.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
	})

	t.Run("pipeline-specific driver secrets override server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test"}, slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		t.Cleanup(func() { _ = secretsMgr.Close() })

		// Save pipeline as native driver
		pipeline, err := client.SavePipeline(context.Background(), "secret-override", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Set pipeline-specific driver config as JSON secret
		scope := secrets.PipelineScope(pipeline.ID)
		cfgJSON, _ := json.Marshal(native.ServerConfig{})
		err = secretsMgr.Set(context.Background(), scope, "driver_config", string(cfgJSON))
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    5,
			DefaultDriver:  "native",
			SecretsManager: secretsMgr,
			DriverConfigs: map[string]orchestra.DriverConfig{
				"native": native.ServerConfig{},
				"docker": docker.ServerConfig{Host: "tcp://server-default:2376"},
			},
		})

		execService := router.ExecutionService()
		run, err := execService.TriggerPipeline(context.Background(), pipeline)
		assert.Expect(err).NotTo(HaveOccurred())
		execService.Wait()

		finalRun, err := client.GetRun(context.Background(), run.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
	})

	t.Run("multiple server driver configs are independent", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		// Pipeline A uses native
		pipelineA, err := client.SavePipeline(context.Background(), "pipeline-a", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:   5,
			DefaultDriver: "docker",
			DriverConfigs: map[string]orchestra.DriverConfig{
				"native": native.ServerConfig{},
				"docker": docker.ServerConfig{},
			},
		})

		execService := router.ExecutionService()
		run, err := execService.TriggerPipeline(context.Background(), pipelineA)
		assert.Expect(err).NotTo(HaveOccurred())
		execService.Wait()

		finalRun, err := client.GetRun(context.Background(), run.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
	})
}
