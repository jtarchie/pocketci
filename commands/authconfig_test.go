package commands_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/commands"
)

func TestAuthConfig(t *testing.T) {
	t.Parallel()
	t.Run("load returns empty config when file does not exist", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		cfg, err := commands.LoadAuthConfig(filepath.Join(t.TempDir(), "nonexistent.config"))
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(cfg.Servers).To(BeEmpty())
	})

	t.Run("save and load round-trip", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		path := filepath.Join(t.TempDir(), "auth.config")

		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"https://ci.example.com":    {Token: "tok-abc"},
				"https://other.example.com": {Token: "tok-xyz"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		loaded, err := commands.LoadAuthConfig(path)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(loaded.Servers).To(HaveLen(2))
		assert.Expect(loaded.Servers["https://ci.example.com"].Token).To(Equal("tok-abc"))
		assert.Expect(loaded.Servers["https://other.example.com"].Token).To(Equal("tok-xyz"))
	})

	t.Run("file permissions are 0600", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		path := filepath.Join(t.TempDir(), "auth.config")

		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"http://localhost:8080": {Token: "test"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		info, err := os.Stat(path)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	t.Run("creates parent directories", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		path := filepath.Join(t.TempDir(), "nested", "deep", "auth.config")

		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"http://localhost": {Token: "t"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		loaded, err := commands.LoadAuthConfig(path)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(loaded.Servers["http://localhost"].Token).To(Equal("t"))
	})

	t.Run("resolveAuthToken prefers explicit token", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		// Write config with a token
		path := filepath.Join(t.TempDir(), "auth.config")
		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"http://localhost:8080": {Token: "config-token"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		// Explicit token wins over config
		result := commands.ResolveAuthToken("explicit-token", path, "http://localhost:8080")
		assert.Expect(result).To(Equal("explicit-token"))
	})

	t.Run("resolveAuthToken falls back to config file", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		path := filepath.Join(t.TempDir(), "auth.config")
		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"http://localhost:8080": {Token: "config-token"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		// No explicit token — falls back to config
		result := commands.ResolveAuthToken("", path, "http://localhost:8080")
		assert.Expect(result).To(Equal("config-token"))
	})

	t.Run("resolveAuthToken returns empty when no match", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		path := filepath.Join(t.TempDir(), "auth.config")
		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"http://localhost:8080": {Token: "config-token"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		result := commands.ResolveAuthToken("", path, "http://other-server:9090")
		assert.Expect(result).To(BeEmpty())
	})

	t.Run("resolveAuthToken normalizes trailing slash", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		path := filepath.Join(t.TempDir(), "auth.config")
		cfg := &commands.AuthConfig{
			Servers: map[string]commands.AuthEntry{
				"http://localhost:8080": {Token: "tok"},
			},
		}

		err := commands.SaveAuthConfig(path, cfg)
		assert.Expect(err).NotTo(HaveOccurred())

		// URL with trailing slash should match stored URL without trailing slash
		result := commands.ResolveAuthToken("", path, "http://localhost:8080/")
		assert.Expect(result).To(Equal("tok"))
	})
}
