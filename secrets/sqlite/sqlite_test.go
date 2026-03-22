package sqlite_test

import (
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/secrets/sqlite"
	. "github.com/onsi/gomega"
)

func TestSQLiteBackend(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)

	t.Run("invalid DSN errors when passphrase is empty", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		_, err := sqlite.New(sqlite.Config{Path: ":memory:", Passphrase: ""}, logger)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("Passphrase"))
	})

	t.Run("valid config creates manager", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		mgr, err := sqlite.New(sqlite.Config{Path: ":memory:", Passphrase: "test-key"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(mgr).NotTo(BeNil())
		_ = mgr.Close()
	})

	t.Run("empty path defaults to in-memory", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		mgr, err := sqlite.New(sqlite.Config{Passphrase: "test-key"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(mgr).NotTo(BeNil())
		_ = mgr.Close()
	})
}
