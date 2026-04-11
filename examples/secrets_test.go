package main_test

import (
	"path/filepath"
	"testing"

	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

func TestSecretsBasic(t *testing.T) {
	t.Parallel()

	drivers := []string{"docker", "native"}

	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			if driver == "native" {
				t.Parallel()
			}

			assert := NewGomegaWithT(t)

			examplePath, err := filepath.Abs("both/secrets-basic.ts")
			assert.Expect(err).NotTo(HaveOccurred())

			runner := testhelpers.Runner{
				Pipeline:                examplePath,
				Driver:                  driver,
				StorageSQLitePath:       ":memory:",
				SecretsSQLitePassphrase: "test-passphrase",
				Secret:                  []string{"API_KEY=super-secret-value-12345"},
			}
			err = runner.Run(nil)
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestSecretsMissingFails(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	examplePath, err := filepath.Abs("both/secrets-basic.ts")
	assert.Expect(err).NotTo(HaveOccurred())

	runner := testhelpers.Runner{
		Pipeline:                examplePath,
		Driver:                  "native",
		StorageSQLitePath:       ":memory:",
		SecretsSQLitePassphrase: "test-passphrase",
	}
	err = runner.Run(nil)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("API_KEY"))
}

func TestSecretsInvalidFlag(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	examplePath, err := filepath.Abs("both/secrets-basic.ts")
	assert.Expect(err).NotTo(HaveOccurred())

	runner := testhelpers.Runner{
		Pipeline:                examplePath,
		Driver:                  "native",
		StorageSQLitePath:       ":memory:",
		SecretsSQLitePassphrase: "test-passphrase",
		Secret:                  []string{"INVALID_NO_EQUALS"},
	}
	err = runner.Run(nil)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("expected KEY=VALUE"))
}

func TestSecretsGlobal(t *testing.T) {
	t.Parallel()

	drivers := []string{"docker", "native"}

	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			if driver == "native" {
				t.Parallel()
			}

			assert := NewGomegaWithT(t)

			// Uses secrets-global.ts which verifies "global-secret-value-99999" is redacted
			examplePath, err := filepath.Abs("both/secrets-global.ts")
			assert.Expect(err).NotTo(HaveOccurred())

			// Set API_KEY as a global secret (not pipeline-scoped)
			// The pipeline should still find it via global fallback
			// and the value should be redacted from output
			runner := testhelpers.Runner{
				Pipeline:                examplePath,
				Driver:                  driver,
				StorageSQLitePath:       ":memory:",
				SecretsSQLitePassphrase: "test-passphrase",
				GlobalSecret:            []string{"API_KEY=global-secret-value-99999"},
			}
			err = runner.Run(nil)
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestSecretsGlobalOverriddenByPipeline(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	examplePath, err := filepath.Abs("both/secrets-basic.ts")
	assert.Expect(err).NotTo(HaveOccurred())

	// Set API_KEY at both global and pipeline scope
	// Pipeline scope should win
	runner := testhelpers.Runner{
		Pipeline:                examplePath,
		Driver:                  "native",
		StorageSQLitePath:       ":memory:",
		SecretsSQLitePassphrase: "test-passphrase",
		Secret:                  []string{"API_KEY=pipeline-secret"},
		GlobalSecret:            []string{"API_KEY=global-secret"},
	}
	err = runner.Run(nil)
	assert.Expect(err).NotTo(HaveOccurred())
}
