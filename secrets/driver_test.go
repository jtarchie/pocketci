package secrets_test

import (
	"context"
	"log/slog"
	"os/exec"
	"testing"

	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/secrets"
	secretss3 "github.com/jtarchie/pocketci/secrets/s3"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

type driverFactory struct {
	name string
	new  func(t *testing.T) secrets.Manager
}

func buildDriverFactories(t *testing.T) []driverFactory {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)

	factories := []driverFactory{
		{
			name: "sqlite",
			new: func(t *testing.T) secrets.Manager {
				t.Helper()

				mgr, err := secretssqlite.New(secretssqlite.Config{
					Path:       ":memory:",
					Passphrase: "test-encryption-key-for-testing",
				}, logger)
				if err != nil {
					t.Fatalf("failed to initialize sqlite secrets backend: %v", err)
				}

				t.Cleanup(func() { _ = mgr.Close() })

				return mgr
			},
		},
	}

	if _, err := exec.LookPath("minio"); err == nil {
		server := testhelpers.StartMinIO(t)
		t.Cleanup(server.Stop)

		s3cfg := &s3config.Config{
			Bucket:          server.Bucket(),
			Endpoint:        server.Endpoint(),
			Region:          "us-east-1",
			AccessKeyID:     server.AccessKeyID(),
			SecretAccessKey: server.SecretAccessKey(),
			ForcePathStyle:  true,
			Key:             "test-encryption-passphrase",
		}

		// Probe once to see if SSE is supported
		_, probeErr := secretss3.New(secretss3.Config{Config: *s3cfg}, logger)
		if probeErr == nil {
			factories = append(factories, driverFactory{
				name: "s3",
				new: func(t *testing.T) secrets.Manager {
					t.Helper()

					mgr, err := secretss3.New(secretss3.Config{Config: *s3cfg}, logger)
					if err != nil {
						t.Skipf("S3 secrets SSE probe failed: %v", err)
					}

					t.Cleanup(func() { _ = mgr.Close() })

					return mgr
				},
			})
		}
	}

	return factories
}

func TestSecretDrivers(t *testing.T) {
	for _, factory := range buildDriverFactories(t) {
		factory := factory

		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()

			t.Run("set and get", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				err := mgr.Set(ctx, secrets.GlobalScope, "API_KEY", "my-secret-value")
				assert.Expect(err).NotTo(HaveOccurred())

				value, err := mgr.Get(ctx, secrets.GlobalScope, "API_KEY")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(value).To(Equal("my-secret-value"))
			})

			t.Run("get nonexistent returns ErrNotFound", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				_, err := mgr.Get(ctx, secrets.GlobalScope, "DOES_NOT_EXIST")
				assert.Expect(err).To(MatchError(secrets.ErrNotFound))
			})

			t.Run("delete existing secret", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				err := mgr.Set(ctx, secrets.GlobalScope, "TO_DELETE", "value")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Delete(ctx, secrets.GlobalScope, "TO_DELETE")
				assert.Expect(err).NotTo(HaveOccurred())

				_, err = mgr.Get(ctx, secrets.GlobalScope, "TO_DELETE")
				assert.Expect(err).To(MatchError(secrets.ErrNotFound))
			})

			t.Run("delete nonexistent returns ErrNotFound", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				err := mgr.Delete(ctx, secrets.GlobalScope, "NOPE")
				assert.Expect(err).To(MatchError(secrets.ErrNotFound))
			})

			t.Run("scope isolation", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				err := mgr.Set(ctx, secrets.GlobalScope, "SHARED_KEY", "global-value")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, secrets.PipelineScope("pipeline-1"), "SHARED_KEY", "pipeline-1-value")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, secrets.PipelineScope("pipeline-2"), "SHARED_KEY", "pipeline-2-value")
				assert.Expect(err).NotTo(HaveOccurred())

				val, err := mgr.Get(ctx, secrets.GlobalScope, "SHARED_KEY")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(val).To(Equal("global-value"))

				val, err = mgr.Get(ctx, secrets.PipelineScope("pipeline-1"), "SHARED_KEY")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(val).To(Equal("pipeline-1-value"))

				val, err = mgr.Get(ctx, secrets.PipelineScope("pipeline-2"), "SHARED_KEY")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(val).To(Equal("pipeline-2-value"))
			})

			t.Run("overwrite updates value", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				err := mgr.Set(ctx, secrets.GlobalScope, "ROTATE_ME", "value-v1")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, secrets.GlobalScope, "ROTATE_ME", "value-v2")
				assert.Expect(err).NotTo(HaveOccurred())

				val, err := mgr.Get(ctx, secrets.GlobalScope, "ROTATE_ME")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(val).To(Equal("value-v2"))
			})

			t.Run("special characters in values", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				specialValues := map[string]string{
					"DOLLAR":    "$VAR_NAME",
					"STAR":      "*.wildcard",
					"BACKSLASH": `C:\path\to\file`,
					"NEWLINES":  "line1\nline2\nline3",
					"UNICODE":   "hello 🔐 world",
					"EMPTY":     "",
				}

				for key, val := range specialValues {
					err := mgr.Set(ctx, secrets.GlobalScope, key, val)
					assert.Expect(err).NotTo(HaveOccurred())

					got, err := mgr.Get(ctx, secrets.GlobalScope, key)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got).To(Equal(val), "mismatch for key %s", key)
				}
			})

			t.Run("ListByScope returns all keys in a scope", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				scope := secrets.PipelineScope("list-test")

				err := mgr.Set(ctx, scope, "B_KEY", "val-b")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, scope, "A_KEY", "val-a")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, secrets.GlobalScope, "GLOBAL_KEY", "val-g")
				assert.Expect(err).NotTo(HaveOccurred())

				keys, err := mgr.ListByScope(ctx, scope)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(keys).To(Equal([]string{"A_KEY", "B_KEY"}))
			})

			t.Run("ListByScope returns empty for empty scope", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				keys, err := mgr.ListByScope(ctx, secrets.PipelineScope("nonexistent"))
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(keys).To(BeEmpty())
			})

			t.Run("DeleteByScope removes all secrets in scope", func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				mgr := factory.new(t)
				ctx := context.Background()

				scope := secrets.PipelineScope("del-scope-test")

				err := mgr.Set(ctx, scope, "KEY1", "val1")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, scope, "KEY2", "val2")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.Set(ctx, secrets.GlobalScope, "GLOBAL", "val-g")
				assert.Expect(err).NotTo(HaveOccurred())

				err = mgr.DeleteByScope(ctx, scope)
				assert.Expect(err).NotTo(HaveOccurred())

				keys, err := mgr.ListByScope(ctx, scope)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(keys).To(BeEmpty())

				val, err := mgr.Get(ctx, secrets.GlobalScope, "GLOBAL")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(val).To(Equal("val-g"))
			})
		})
	}
}
