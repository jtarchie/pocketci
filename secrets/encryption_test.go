package secrets_test

import (
	"testing"

	"github.com/jtarchie/pocketci/secrets"
	. "github.com/onsi/gomega"
)

func TestEncryptor(t *testing.T) {
	t.Parallel()

	makeEncryptor := func(t *testing.T, passphrase string) *secrets.Encryptor {
		t.Helper()

		assert := NewGomegaWithT(t)

		params, err := secrets.DefaultKDFParams()
		assert.Expect(err).NotTo(HaveOccurred())

		key, err := secrets.DeriveKey(passphrase, params)
		assert.Expect(err).NotTo(HaveOccurred())

		enc, err := secrets.NewEncryptor(key)
		assert.Expect(err).NotTo(HaveOccurred())

		return enc
	}

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc := makeEncryptor(t, "test-passphrase-for-encryption")
		aad := []byte("global\x00MY_SECRET")
		plaintext := []byte("hello secret world")

		ciphertext, err := enc.Encrypt(plaintext, aad)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(ciphertext).NotTo(Equal(plaintext))

		decrypted, err := enc.Decrypt(ciphertext, aad)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(decrypted).To(Equal(plaintext))
	})

	t.Run("empty plaintext", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc := makeEncryptor(t, "test-key")

		ciphertext, err := enc.Encrypt([]byte(""), nil)
		assert.Expect(err).NotTo(HaveOccurred())

		decrypted, err := enc.Decrypt(ciphertext, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(decrypted)).To(Equal(""))
	})

	t.Run("wrong key fails decryption", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc1 := makeEncryptor(t, "key-one")
		enc2 := makeEncryptor(t, "key-two")

		ciphertext, err := enc1.Encrypt([]byte("secret data"), nil)
		assert.Expect(err).NotTo(HaveOccurred())

		_, err = enc2.Decrypt(ciphertext, nil)
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("invalid key length", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		_, err := secrets.NewEncryptor([]byte("too-short"))
		assert.Expect(err).To(MatchError(secrets.ErrInvalidKey))
	})

	t.Run("ciphertext too short", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc := makeEncryptor(t, "test-key")

		_, err := enc.Decrypt([]byte("x"), nil)
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("same plaintext produces different ciphertext", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc := makeEncryptor(t, "test-key")
		plaintext := []byte("same input")

		ct1, err := enc.Encrypt(plaintext, nil)
		assert.Expect(err).NotTo(HaveOccurred())

		ct2, err := enc.Encrypt(plaintext, nil)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(ct1).NotTo(Equal(ct2))
	})

	t.Run("different salts produce different keys", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		params1, err := secrets.DefaultKDFParams()
		assert.Expect(err).NotTo(HaveOccurred())

		params2, err := secrets.DefaultKDFParams()
		assert.Expect(err).NotTo(HaveOccurred())

		key1, err := secrets.DeriveKey("same-passphrase", params1)
		assert.Expect(err).NotTo(HaveOccurred())

		key2, err := secrets.DeriveKey("same-passphrase", params2)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(key1).NotTo(Equal(key2))
	})

	t.Run("same params produce same key", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		params, err := secrets.DefaultKDFParams()
		assert.Expect(err).NotTo(HaveOccurred())

		key1, err := secrets.DeriveKey("fixed-passphrase", params)
		assert.Expect(err).NotTo(HaveOccurred())

		key2, err := secrets.DeriveKey("fixed-passphrase", params)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(key1).To(Equal(key2))
		assert.Expect(key1).To(HaveLen(32))
	})

	t.Run("wrong AAD fails decryption", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc := makeEncryptor(t, "test-key")

		ciphertext, err := enc.Encrypt([]byte("data"), []byte("scope\x00key"))
		assert.Expect(err).NotTo(HaveOccurred())

		_, err = enc.Decrypt(ciphertext, []byte("scope\x00other-key"))
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("correct AAD succeeds", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		enc := makeEncryptor(t, "test-key")
		aad := []byte("global\x00MY_SECRET")

		ct, err := enc.Encrypt([]byte("value"), aad)
		assert.Expect(err).NotTo(HaveOccurred())

		pt, err := enc.Decrypt(ct, aad)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(pt)).To(Equal("value"))
	})

	t.Run("empty salt errors", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		params := secrets.KDFParams{Algorithm: "argon2id", Time: 3, Memory: 65536, Threads: 4, KeyLen: 32}
		_, err := secrets.DeriveKey("passphrase", params)
		assert.Expect(err).To(HaveOccurred())
	})
}
