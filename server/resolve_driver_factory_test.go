package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func newTestExecService(t *testing.T) (*ExecutionService, storage.Driver) {
	t.Helper()

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatalf("could not create temp file: %v", err)
	}
	t.Cleanup(func() { _ = buildFile.Close() })

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "ns", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("could not create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := NewExecutionService(store, slog.New(slog.NewTextHandler(io.Discard, nil)), 1, nil)

	return svc, store
}

func TestResolveDriverFactory(t *testing.T) {
	t.Parallel()

	t.Run("pipeline with no driver uses DefaultDriver and server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		svc, store := newTestExecService(t)
		svc.DefaultDriver = "native"
		svc.DriverConfigs = map[string]orchestra.DriverConfig{
			"native": native.ServerConfig{},
			"docker": docker.ServerConfig{Host: "tcp://remote:2376"},
		}

		pipeline, err := store.SavePipeline(context.Background(), "no-driver", "export const pipeline = async () => {};", "", "")
		assert.Expect(err).NotTo(HaveOccurred())

		factory := svc.resolveDriverFactory(pipeline, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driver, err := factory("test-ns")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(driver.Name()).To(Equal("native"))
		_ = driver.Close()
	})

	t.Run("pipeline with explicit driver uses matching server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		svc, store := newTestExecService(t)
		svc.DefaultDriver = "docker"
		svc.DriverConfigs = map[string]orchestra.DriverConfig{
			"native": native.ServerConfig{},
			"docker": docker.ServerConfig{},
		}

		pipeline, err := store.SavePipeline(context.Background(), "explicit-native", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		factory := svc.resolveDriverFactory(pipeline, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driver, err := factory("test-ns")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(driver.Name()).To(Equal("native"))
		_ = driver.Close()
	})

	t.Run("pipeline with driver not in server configs falls back to empty config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		svc, store := newTestExecService(t)
		svc.DefaultDriver = "docker"
		svc.DriverConfigs = map[string]orchestra.DriverConfig{
			"docker": docker.ServerConfig{},
		}

		// native works with empty config since it needs nothing
		pipeline, err := store.SavePipeline(context.Background(), "unconfigured-driver", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		factory := svc.resolveDriverFactory(pipeline, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driver, err := factory("test-ns")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(driver.Name()).To(Equal("native"))
		_ = driver.Close()
	})

	t.Run("pipeline-specific driver secrets override server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		svc, store := newTestExecService(t)
		svc.DefaultDriver = "docker"
		svc.DriverConfigs = map[string]orchestra.DriverConfig{
			"native": native.ServerConfig{},
			"docker": docker.ServerConfig{Host: "tcp://server-default:2376"},
		}

		secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		assert.Expect(err).NotTo(HaveOccurred())
		t.Cleanup(func() { _ = secretsMgr.Close() })
		svc.SecretsManager = secretsMgr

		pipeline, err := store.SavePipeline(context.Background(), "custom-docker", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Set pipeline-specific driver config as JSON secret
		scope := secrets.PipelineScope(pipeline.ID)
		cfgJSON, _ := json.Marshal(docker.ServerConfig{Host: "tcp://pipeline-custom:2376"})
		err = secretsMgr.Set(context.Background(), scope, "driver_config", string(cfgJSON))
		assert.Expect(err).NotTo(HaveOccurred())

		factory := svc.resolveDriverFactory(pipeline, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driver, err := factory("test-ns")
		assert.Expect(err).NotTo(HaveOccurred())
		// Docker driver was created — it doesn't expose its host, but it didn't error
		// and didn't fall through to a different driver.
		assert.Expect(driver.Name()).To(Equal("docker"))
		_ = driver.Close()
	})

	t.Run("pipeline secrets are not merged with server config", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		svc, store := newTestExecService(t)
		svc.DefaultDriver = "native"
		svc.DriverConfigs = map[string]orchestra.DriverConfig{
			"native": native.ServerConfig{},
			"docker": docker.ServerConfig{Host: "tcp://server:2376"},
		}

		secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		assert.Expect(err).NotTo(HaveOccurred())
		t.Cleanup(func() { _ = secretsMgr.Close() })
		svc.SecretsManager = secretsMgr

		pipeline, err := store.SavePipeline(context.Background(), "partial-config", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Set pipeline-specific driver config as JSON — overrides server config entirely.
		scope := secrets.PipelineScope(pipeline.ID)
		cfgJSON, _ := json.Marshal(native.ServerConfig{})
		err = secretsMgr.Set(context.Background(), scope, "driver_config", string(cfgJSON))
		assert.Expect(err).NotTo(HaveOccurred())

		factory := svc.resolveDriverFactory(pipeline, slog.New(slog.NewTextHandler(io.Discard, nil)))
		// native ignores config, so it works even with partial config
		driver, err := factory("test-ns")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(driver.Name()).To(Equal("native"))
		_ = driver.Close()
	})

	t.Run("multiple server driver configs are independent", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		svc, store := newTestExecService(t)
		svc.DefaultDriver = "docker"
		svc.DriverConfigs = map[string]orchestra.DriverConfig{
			"native": native.ServerConfig{},
			"docker": docker.ServerConfig{},
		}

		// Pipeline A uses native
		pipelineA, err := store.SavePipeline(context.Background(), "pipeline-a", "export const pipeline = async () => {};", "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Pipeline B uses docker (default)
		pipelineB, err := store.SavePipeline(context.Background(), "pipeline-b", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		factoryA := svc.resolveDriverFactory(pipelineA, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driverA, err := factoryA("ns-a")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(driverA.Name()).To(Equal("native"))

		factoryB := svc.resolveDriverFactory(pipelineB, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driverB, err := factoryB("ns-b")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(driverB.Name()).To(Equal("docker"))

		_ = driverA.Close()
		_ = driverB.Close()
	})
}
