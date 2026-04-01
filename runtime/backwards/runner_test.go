package backwards_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	configpkg "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/native"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func debugLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func loadConfig(t *testing.T, path string) *configpkg.Config {
	t.Helper()

	assert := NewGomegaWithT(t)

	contents, err := os.ReadFile(path)
	assert.Expect(err).NotTo(HaveOccurred())

	var cfg configpkg.Config

	err = yaml.UnmarshalWithOptions(contents, &cfg, yaml.Strict())
	assert.Expect(err).NotTo(HaveOccurred())

	return &cfg
}

type driverFactory struct {
	name string
	new  func(namespace string, logger *slog.Logger) (orchestra.Driver, error)
}

var drivers = []driverFactory{
	{
		name: "native",
		new: func(namespace string, logger *slog.Logger) (orchestra.Driver, error) {
			return native.New(context.Background(), native.Config{Namespace: namespace}, logger)
		},
	},
	{
		name: "docker",
		new: func(namespace string, logger *slog.Logger) (orchestra.Driver, error) {
			return docker.New(context.Background(), docker.Config{Namespace: namespace}, logger)
		},
	},
}

func TestTryStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/try.yml")

			logger := discardLogger()

			driver, err := df.new("test-try-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-try", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestDoStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/do.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-do-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-do", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("ensure-task"))
		})
	}
}

func TestOnAbortStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/on_abort.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-abort-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-abort", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("Task abort-task aborted"))
			assert.Expect(logs.String()).To(ContainSubstring("on-abort-task"))
		})
	}
}

func TestOnSuccessStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/on_success.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-on-success-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-on-success", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("on-success-task"))
		})
	}
}

func TestEnsureStep(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "../../backwards/steps/ensure.yml")

			var logs strings.Builder

			logger := debugLogger(&logs)

			driver, err := df.new("test-ensure-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-ensure", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run")
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(logs.String()).To(ContainSubstring("ensure-task"))
			assert.Expect(logs.String()).To(ContainSubstring("step.ensure.failed"))
		})
	}
}
