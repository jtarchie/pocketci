package commands_test

import (
	"log/slog"
	"testing"

	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	. "github.com/onsi/gomega"
)

// TestServerSecretsPassphraseRequired verifies that the SQLite secrets backend
// (which the server defaults to) rejects an empty passphrase. This ensures that
// removing the default:"testing" struct tag from Server.SecretsSQLitePassphrase
// causes a startup failure rather than silently using an empty key.
func TestServerSecretsPassphraseRequired(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)

	_, err := secretssqlite.New(secretssqlite.Config{
		Path:       ":memory:",
		Passphrase: "",
	}, logger)

	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("Passphrase"))
}
