package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/server/auth"
	. "github.com/onsi/gomega"
)

func TestEvaluateAccess(t *testing.T) {
	t.Parallel()
	user := auth.User{
		Email:         "alice@example.com",
		Name:          "Alice",
		NickName:      "alice",
		Provider:      "github",
		UserID:        "12345",
		Organizations: []string{"myorg", "otherorg"},
		Groups:        []string{"admin"},
	}

	t.Run("empty expression allows all", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeTrue())
	})

	t.Run("matching email", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("Email == \"alice@example.com\"", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeTrue())
	})

	t.Run("non-matching email", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("Email == \"bob@example.com\"", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeFalse())
	})

	t.Run("organization membership", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("\"myorg\" in Organizations", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeTrue())
	})

	t.Run("organization not member", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("\"secretorg\" in Organizations", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeFalse())
	})

	t.Run("group membership", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("\"admin\" in Groups", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeTrue())
	})

	t.Run("provider check", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess("Provider == \"github\"", user)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeTrue())
	})

	t.Run("complex expression", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		allowed, err := auth.EvaluateAccess(
			"Provider == \"github\" && \"myorg\" in Organizations",
			user,
		)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(allowed).To(BeTrue())
	})

	t.Run("invalid expression returns error", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		_, err := auth.EvaluateAccess("invalid syntax !!!", user)
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestValidateExpression(t *testing.T) {
	t.Parallel()
	t.Run("valid expression", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		err := auth.ValidateExpression("Email == \"test@example.com\"")
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("valid complex expression", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		err := auth.ValidateExpression("\"myorg\" in Organizations && Provider == \"github\"")
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("invalid expression", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		err := auth.ValidateExpression("this is not valid !!!")
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestToken(t *testing.T) {
	t.Parallel()
	secret := "test-secret-key-at-least-32-bytes-long"

	user := &auth.User{
		Email:    "alice@example.com",
		Name:     "Alice",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}

	t.Run("generate and validate", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		token, err := auth.GenerateToken(user, secret, 24*time.Hour, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(token).NotTo(BeEmpty())

		// JWT tokens have 3 dot-separated parts: header.payload.signature
		assert.Expect(strings.Count(token, ".")).To(Equal(2))

		validator := auth.TokenValidator(secret)
		validatedUser, err := validator(token)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(validatedUser.Email).To(Equal("alice@example.com"))
		assert.Expect(validatedUser.NickName).To(Equal("alice"))
		assert.Expect(validatedUser.Provider).To(Equal("github"))
		assert.Expect(validatedUser.UserID).To(Equal("12345"))
	})

	t.Run("expired token", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		token, err := auth.GenerateToken(user, secret, -1*time.Hour, nil)
		assert.Expect(err).NotTo(HaveOccurred())

		validator := auth.TokenValidator(secret)
		_, err = validator(token)
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("invalid token", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		validator := auth.TokenValidator(secret)
		_, err := validator("not-a-valid-token")
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("wrong secret", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		token, err := auth.GenerateToken(user, secret, 24*time.Hour, nil)
		assert.Expect(err).NotTo(HaveOccurred())

		validator := auth.TokenValidator("wrong-secret-key")
		_, err = validator(token)
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestConfig(t *testing.T) {
	t.Parallel()
	t.Run("no providers", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		cfg := &auth.Config{}
		assert.Expect(cfg.HasOAuthProviders()).To(BeFalse())
		assert.Expect(cfg.EnabledProviders()).To(BeEmpty())
	})

	t.Run("github only", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		cfg := &auth.Config{
			GithubClientID:     "id",
			GithubClientSecret: "secret",
		}
		assert.Expect(cfg.HasOAuthProviders()).To(BeTrue())
		assert.Expect(cfg.EnabledProviders()).To(ConsistOf("github"))
	})

	t.Run("all providers", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		cfg := &auth.Config{
			GithubClientID:        "id",
			GithubClientSecret:    "secret",
			GitlabClientID:        "id",
			GitlabClientSecret:    "secret",
			MicrosoftClientID:     "id",
			MicrosoftClientSecret: "secret",
		}
		assert.Expect(cfg.HasOAuthProviders()).To(BeTrue())
		assert.Expect(cfg.EnabledProviders()).To(ConsistOf("github", "gitlab", "microsoftonline"))
	})

	t.Run("partial provider config ignored", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		cfg := &auth.Config{
			GithubClientID: "id",
		}
		assert.Expect(cfg.HasOAuthProviders()).To(BeFalse())
		assert.Expect(cfg.EnabledProviders()).To(BeEmpty())
	})
}
