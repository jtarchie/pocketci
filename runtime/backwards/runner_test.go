package backwards_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/goccy/go-yaml"
	configpkg "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra/native"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

func TestTryStep(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	cfg := loadConfig(t, "../../backwards/steps/try.yml")

	logger := discardLogger()

	driver, err := native.New(context.Background(), native.Config{Namespace: "test-try"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = driver.Close() }()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-try", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = store.Close() }()

	runner := backwards.New(cfg, driver, store, logger, "test-run")
	err = runner.Run(context.Background())
	assert.Expect(err).NotTo(HaveOccurred())
}
