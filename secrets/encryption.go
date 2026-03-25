package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// ErrInvalidKey is returned when the encryption key has an invalid length.
var ErrInvalidKey = errors.New("encryption key must be 32 bytes (AES-256)")

// KDFParams holds the parameters for Argon2id key derivation.
// A KDFParams value must be persisted alongside encrypted data so that
// the same key can be re-derived on subsequent startups.
type KDFParams struct {
	Algorithm string // always "argon2id"
	Salt      []byte
	Time      uint32
	Memory    uint32
	Threads   uint8
	KeyLen    uint32
}

// DefaultKDFParams returns a KDFParams with recommended Argon2id settings
// and a freshly generated 16-byte random salt.
func DefaultKDFParams() (KDFParams, error) {
	salt := make([]byte, 16)

	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return KDFParams{}, fmt.Errorf("could not generate KDF salt: %w", err)
	}

	return KDFParams{
		Algorithm: "argon2id",
		Salt:      salt,
		Time:      3,
		Memory:    65536,
		Threads:   4,
		KeyLen:    32,
	}, nil
}

// DeriveKey derives a 32-byte AES key from a passphrase using Argon2id.
// The KDFParams (including the salt) must be persisted and reused on every
// subsequent startup to reproduce the same key.
func DeriveKey(passphrase string, params KDFParams) ([]byte, error) {
	if len(params.Salt) == 0 {
		return nil, errors.New("KDF salt must not be empty")
	}

	return argon2.IDKey(
		[]byte(passphrase),
		params.Salt,
		params.Time,
		params.Memory,
		params.Threads,
		params.KeyLen,
	), nil
}

// Encryptor provides AES-256-GCM encryption and decryption.
type Encryptor struct {
	aead cipher.AEAD
}

// NewEncryptor creates an Encryptor from a 32-byte key.
func NewEncryptor(key []byte) (*Encryptor, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("could not create cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("could not create GCM: %w", err)
	}

	return &Encryptor{aead: aead}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with the provided AAD.
// aad (additional authenticated data) binds the ciphertext to its storage
// slot (e.g. scope+key); pass nil if no binding is needed.
// The nonce is prepended to the ciphertext.
func (e *Encryptor) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())

	_, err := io.ReadFull(rand.Reader, nonce)
	if err != nil {
		return nil, fmt.Errorf("could not generate nonce: %w", err)
	}

	return e.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Decrypt decrypts ciphertext that was encrypted with Encrypt.
// aad must match the value passed to Encrypt; a mismatch causes
// authentication failure.
// Expects nonce prepended to ciphertext.
func (e *Encryptor) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	nonceSize := e.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short: %w", errors.ErrUnsupported)
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := e.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("could not decrypt: %w", err)
	}

	return plaintext, nil
}
