package sqlite_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jtarchie/pocketci/secrets"
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

	t.Run("kdf params persisted across reopen", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		dbPath := filepath.Join(t.TempDir(), "secrets.db")
		ctx := context.Background()

		// First open: generates and stores KDF params, writes a secret.
		mgr1, err := sqlite.New(sqlite.Config{Path: dbPath, Passphrase: "reopen-passphrase"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())

		err = mgr1.Set(ctx, secrets.GlobalScope, "MY_KEY", "my-value")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(mgr1.Close()).NotTo(HaveOccurred())

		// Second open: must reuse the stored KDF params to decrypt the value.
		mgr2, err := sqlite.New(sqlite.Config{Path: dbPath, Passphrase: "reopen-passphrase"}, logger)
		assert.Expect(err).NotTo(HaveOccurred())

		val, err := mgr2.Get(ctx, secrets.GlobalScope, "MY_KEY")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(val).To(Equal("my-value"))
		assert.Expect(mgr2.Close()).NotTo(HaveOccurred())
	})
}
