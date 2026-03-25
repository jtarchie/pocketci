// Package s3 provides an S3-backed secrets manager for PocketCI.
//
// # Security model
//
// Secrets are protected by application-layer AES-256-GCM encryption (key
// derived from Config.Key via Argon2id with a per-deployment random salt),
// applied before any bytes leave the process.
// An optional S3 Server-Side Encryption layer may be configured via
// Config.EncryptMode (sse-s3, sse-kms, or sse-c).
package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/secrets"
)

// Config holds the configuration for the S3 secrets backend.
// It embeds s3config.Config for all S3 connection fields, plus Key
// for application-layer AES-256-GCM encryption.
type Config struct {
	s3config.Config
}

// S3 implements secrets.Manager using an S3-compatible object store as the
// backend. Every stored object is AES-256-GCM encrypted before upload.
type S3 struct {
	*s3config.Client
	encryptor *secrets.Encryptor
	logger    *slog.Logger
}

// secretRecord is the JSON structure persisted in each S3 object.
// EncryptedValue is base64-encoded AES-256-GCM ciphertext.
type secretRecord struct {
	EncryptedValue string `json:"encrypted_value"`
	Version        string `json:"version"`
	UpdatedAt      string `json:"updated_at"`
}

// kdfParamsRecord is the JSON structure stored as the KDF params sentinel object.
type kdfParamsRecord struct {
	Algorithm string `json:"algorithm"`
	Salt      []byte `json:"salt"`
	Time      uint32 `json:"time"`
	Memory    uint32 `json:"memory"`
	Threads   uint8  `json:"threads"`
	KeyLen    uint32 `json:"key_len"`
}

// New creates a new S3-backed secrets manager.
//
// cfg.Key is mandatory: it is the passphrase for application-layer AES-256-GCM
// encryption. cfg.EncryptMode is optional; when set to sse-s3, sse-kms, or sse-c
// a construction-time probe verifies that the S3 provider accepts the encryption headers.
func New(cfg Config, logger *slog.Logger) (secrets.Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	logger = logger.WithGroup("secrets.s3")

	if cfg.Key == "" {
		return nil, errors.New("s3 secrets driver requires Key for application-layer encryption")
	}

	ctx := context.Background()

	client, err := s3config.NewClient(ctx, &cfg.Config)
	if err != nil {
		return nil, err
	}

	// Build a temporary S3 shell (no encryptor yet) to load/init KDF params.
	shell := &S3{Client: client, logger: logger}

	params, err := loadOrInitKDFParamsS3(ctx, shell)
	if err != nil {
		return nil, fmt.Errorf("could not load KDF params from S3: %w", err)
	}

	key, err := secrets.DeriveKey(cfg.Key, params)
	if err != nil {
		return nil, fmt.Errorf("could not derive key: %w", err)
	}

	encryptor, err := secrets.NewEncryptor(key)
	if err != nil {
		return nil, fmt.Errorf("could not create encryptor: %w", err)
	}

	mgr := &S3{
		Client:    client,
		encryptor: encryptor,
		logger:    logger,
	}

	// Probe SSE: when encrypt= is configured, upload a tiny sentinel object and
	// verify that the S3 provider accepts the encryption headers.
	if cfg.EncryptMode != "" {
		if err := mgr.probeSSE(ctx); err != nil {
			return nil, fmt.Errorf("s3 secrets SSE probe failed — provider does not support encrypt=%q: %w", cfg.EncryptMode, err)
		}
	}

	logger.Info("secrets.s3.initialized", "bucket", cfg.Bucket, "prefix", cfg.Prefix, "encrypt", cfg.EncryptMode)

	return mgr, nil
}

// kdfParamsKey returns the S3 object key used to store the KDF params sentinel.
func (s *S3) kdfParamsKey() string {
	parts := []string{}

	if p := s.Prefix(); p != "" {
		parts = append(parts, p)
	}

	parts = append(parts, "secrets", "__kdf_params__", "__kdf_params__.json")

	return strings.Join(parts, "/")
}

// loadOrInitKDFParamsS3 reads KDF params from the reserved sentinel object.
// On first startup (object not found), it generates a new random salt, uploads
// the params, and returns them.
func loadOrInitKDFParamsS3(ctx context.Context, s *S3) (secrets.KDFParams, error) {
	objKey := s.kdfParamsKey()

	data, err := s.GetBytes(ctx, objKey)
	if err != nil && !s3config.IsNotFound(err) {
		return secrets.KDFParams{}, fmt.Errorf("could not fetch KDF params: %w", err)
	}

	if err == nil {
		var rec kdfParamsRecord

		if jsonErr := json.Unmarshal(data, &rec); jsonErr != nil {
			return secrets.KDFParams{}, fmt.Errorf("could not unmarshal KDF params: %w", jsonErr)
		}

		return secrets.KDFParams{
			Algorithm: rec.Algorithm,
			Salt:      rec.Salt,
			Time:      rec.Time,
			Memory:    rec.Memory,
			Threads:   rec.Threads,
			KeyLen:    rec.KeyLen,
		}, nil
	}

	// First startup: generate and upload new KDF params.
	params, genErr := secrets.DefaultKDFParams()
	if genErr != nil {
		return secrets.KDFParams{}, fmt.Errorf("could not generate KDF params: %w", genErr)
	}

	rec := kdfParamsRecord{
		Algorithm: params.Algorithm,
		Salt:      params.Salt,
		Time:      params.Time,
		Memory:    params.Memory,
		Threads:   params.Threads,
		KeyLen:    params.KeyLen,
	}

	payload, marshalErr := json.Marshal(rec)
	if marshalErr != nil {
		return secrets.KDFParams{}, fmt.Errorf("could not marshal KDF params: %w", marshalErr)
	}

	if putErr := s.PutBytes(ctx, objKey, payload, "application/json"); putErr != nil {
		return secrets.KDFParams{}, fmt.Errorf("could not upload KDF params: %w", putErr)
	}

	return params, nil
}

// probeSSE writes a tiny sentinel object with SSE headers and verifies the
// provider accepts them. The sentinel is deleted immediately after upload.
func (s *S3) probeSSE(ctx context.Context) error {
	sentinelKey := s.scopePrefix("__probe__") + "__sse_check__.json"

	uploadErr := s.PutBytes(ctx, sentinelKey, []byte(`{"probe":true}`), "application/json")

	// Always attempt cleanup — even if upload failed the key might exist.
	_ = s.DeleteKey(ctx, sentinelKey)

	if uploadErr != nil {
		return fmt.Errorf("SSE probe upload failed: %w", uploadErr)
	}

	return nil
}

// scopePrefix returns the S3 key prefix for a given scope directory.
// Each path segment of the scope is individually URL-escaped.
// Format: [prefix/]secrets/[escaped-scope]/
func (s *S3) scopePrefix(scope string) string {
	parts := []string{}

	if p := s.Prefix(); p != "" {
		parts = append(parts, p)
	}

	scopeSegments := strings.Split(scope, "/")
	escaped := make([]string, len(scopeSegments))

	for i, seg := range scopeSegments {
		escaped[i] = url.PathEscape(seg)
	}

	parts = append(parts, "secrets", strings.Join(escaped, "/"))

	return strings.Join(parts, "/") + "/"
}

// objectKey returns the S3 key for a specific scope+key combination.
// Format: [prefix/]secrets/[escaped-scope]/[url.PathEscape(key)].json
func (s *S3) objectKey(scope, key string) string {
	return s.scopePrefix(scope) + url.PathEscape(key) + ".json"
}

// aadFor returns the additional authenticated data for a given scope and key.
// This binds each ciphertext to its storage slot, preventing cross-slot swaps.
func aadFor(scope, key string) []byte {
	return []byte(scope + "\x00" + key)
}

// upload writes a secretRecord to S3 using multipart upload with SSE.
func (s *S3) upload(ctx context.Context, key string, rec secretRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("could not marshal secret record: %w", err)
	}

	return s.PutBytes(ctx, key, data, "application/json")
}

// download retrieves and deserialises a secretRecord from S3.
func (s *S3) download(ctx context.Context, key string) (*secretRecord, error) {
	data, err := s.GetBytes(ctx, key)
	if err != nil {
		if s3config.IsNotFound(err) {
			return nil, secrets.ErrNotFound
		}

		return nil, fmt.Errorf("could not get secret: %w", err)
	}

	var rec secretRecord

	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("could not unmarshal secret record: %w", err)
	}

	return &rec, nil
}

// Get retrieves a plaintext secret by scope and key.
func (s *S3) Get(ctx context.Context, scope string, key string) (string, error) {
	rec, err := s.download(ctx, s.objectKey(scope, key))
	if err != nil {
		return "", err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(rec.EncryptedValue)
	if err != nil {
		return "", fmt.Errorf("could not decode encrypted value for %q in scope %q: %w", key, scope, err)
	}

	plaintext, err := s.encryptor.Decrypt(ciphertext, aadFor(scope, key))
	if err != nil {
		return "", fmt.Errorf("could not decrypt secret %q in scope %q: %w", key, scope, err)
	}

	return string(plaintext), nil
}

// Set stores or updates an encrypted secret.
func (s *S3) Set(ctx context.Context, scope string, key string, value string) error {
	encrypted, err := s.encryptor.Encrypt([]byte(value), aadFor(scope, key))
	if err != nil {
		return fmt.Errorf("could not encrypt secret: %w", err)
	}

	objKey := s.objectKey(scope, key)

	// Determine the next version by checking whether the object already exists.
	version := "v1"

	existing, err := s.download(ctx, objKey)
	if err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return fmt.Errorf("could not check existing secret: %w", err)
	}

	if existing != nil {
		version = incrementVersion(existing.Version)
	}

	rec := secretRecord{
		EncryptedValue: base64.StdEncoding.EncodeToString(encrypted),
		Version:        version,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.upload(ctx, objKey, rec); err != nil {
		return fmt.Errorf("could not store secret: %w", err)
	}

	s.logger.Info("secret.set", "scope", scope, "key", key, "version", version)

	return nil
}

// Delete removes a secret. Returns ErrNotFound when the secret does not exist.
func (s *S3) Delete(ctx context.Context, scope string, key string) error {
	objKey := s.objectKey(scope, key)

	// Verify it exists before attempting deletion to return ErrNotFound correctly.
	if _, err := s.download(ctx, objKey); err != nil {
		return err
	}

	if err := s.DeleteKey(ctx, objKey); err != nil {
		return fmt.Errorf("could not delete secret: %w", err)
	}

	s.logger.Info("secret.deleted", "scope", scope, "key", key)

	return nil
}

// ListByScope returns all secret keys within a scope, sorted alphabetically.
func (s *S3) ListByScope(ctx context.Context, scope string) ([]string, error) {
	prefix := s.scopePrefix(scope)

	keys, err := s.ListKeys(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("could not list secrets by scope: %w", err)
	}

	// Strip the scope prefix and ".json" suffix to recover the original key names.
	result := make([]string, 0, len(keys))

	for _, k := range keys {
		name := strings.TrimPrefix(k, prefix)
		name = strings.TrimSuffix(name, ".json")

		unescaped, err := url.PathUnescape(name)
		if err != nil {
			unescaped = name
		}

		result = append(result, unescaped)
	}

	return result, nil
}

// DeleteByScope removes all secrets in the given scope.
func (s *S3) DeleteByScope(ctx context.Context, scope string) error {
	prefix := s.scopePrefix(scope)

	keys, err := s.ListKeys(ctx, prefix)
	if err != nil {
		return fmt.Errorf("could not list secrets for scope deletion: %w", err)
	}

	for _, k := range keys {
		if err := s.DeleteKey(ctx, k); err != nil {
			return fmt.Errorf("could not delete secret %q: %w", k, err)
		}
	}

	s.logger.Info("secrets.deleted_by_scope", "scope", scope)

	return nil
}

// Close is a no-op; the S3 client holds no persistent connections.
func (s *S3) Close() error {
	return nil
}

// incrementVersion increments a version string like "v1" → "v2".
func incrementVersion(version string) string {
	var num int

	_, err := fmt.Sscanf(version, "v%d", &num)
	if err != nil {
		return "v1"
	}

	return fmt.Sprintf("v%d", num+1)
}
